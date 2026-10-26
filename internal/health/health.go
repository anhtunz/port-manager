// Package health kiểm tra sức khoẻ HTTP của từng cổng dev đang mở.
//
// Vì sao cần: một service "đang chạy" (process còn sống, cổng đã LISTEN) vẫn
// có thể trả HTTP 500 hoặc treo không trả lời. App cần phân biệt rõ 3 tình
// huống để user biết chính xác service nào hỏng:
//
//   - ok          — server trả HTTP < 400 (2xx/3xx)
//   - http_error  — server trả HTTP >= 400 (lỗi do chính server)
//   - no_response — không nhận được HTTP nào (timeout / cổng không mở)
//   - skipped     — cố ý không probe (vd cổng gRPC, service compose)
//
// Nguyên tắc thiết kế (ràng buộc của hạng mục F2.2):
//
//   - Timeout ngắn (mặc định 1500ms) — probe không được làm chậm app.
//   - KHÔNG đụng cổng không mở: caller chỉ nên truyền cổng đang LISTEN
//     (dùng IsListening / FilterOpen để lọc trước).
//   - net/http với client TÁI DÙNG (connection pool) để không mở rồi đóng
//     TCP cho mỗi lần probe.
//   - Redirect TẮT: đi theo redirect sẽ làm probe rời khỏi cổng đang kiểm
//     tra (vd 3000 → 8080) → kết quả nói về cổng khác. Dùng ErrUseLastResponse
//     để nhận đúng mã 3xx mà không theo.
//   - Phân biệt "không phản hồi" với "trả về mã lỗi" — đây là điều mà
//     status code không cho biết (timeout không có status code).
//
// Package này không phụ thuộc package nào khác của repo (không import
// internal/config hay internal/scan) để app chỉ cần tự dựng Target từ
// dữ liệu scan sẵn có.
package health

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Trạng thái probe — đúng 4 giá trị trong hợp đồng API (frontend vẽ chip
// theo class health-<state>).
const (
	StateOK         = "ok"
	StateHTTPError  = "http_error"
	StateNoResponse = "no_response"
	StateSkipped    = "skipped"
)

const (
	// DefaultTimeout: timeout cho 1 lần probe. Ngắn để app không bị treo
	// khi service treo (server nhận request nhưng không trả lời).
	DefaultTimeout = 1500 * time.Millisecond

	// DefaultMaxConcurrent: số request HTTP đồng thời tối đa khi probe
	// nhiều cổng — đủ nhanh mà không làm máy nghẽn.
	DefaultMaxConcurrent = 8
)

// HealthEntry là kết quả probe của 1 cổng.
//
// Tên field camelCase và thứ tự khai báo là HỢP ĐỒNG với frontend (Wails
// marshal theo đúng thứ tự struct). Nếu repo có package internal/api định
// nghĩa lại HealthEntry thì nên khai báo alias
// `type HealthEntry = health.HealthEntry` để không phải chuyển đổi tay.
type HealthEntry struct {
	ID        string `json:"id"`
	Port      int    `json:"port"`
	State     string `json:"state"`
	Code      int    `json:"code"`
	LatencyMs int    `json:"latencyMs"`
	Detail    string `json:"detail"`
}

// Entry là alias ngắn của HealthEntry cho code gọi gọn hơn (health.Entry).
type Entry = HealthEntry

// Target là một cổng cần probe.
type Target struct {
	// ID: id service sở hữu cổng — frontend gắn kết quả theo (id, port).
	ID string

	// Port: cổng cần probe.
	Port int

	// Path: path để probe (vd "/healthz"). Rỗng → "/".
	Path string

	// Skip: true → không gọi mạng, trả về state skipped (vd cổng gRPC,
	// service compose, cổng không mở).
	Skip bool

	// Reason: lý do bỏ qua, hiển thị ở trường detail.
	Reason string
}

// Checker giữ client HTTP dùng lại + giới hạn số request đồng thời.
//
// Dùng 1 Checker cho cả vòng probe: nhờ vậy các request cùng host dùng chung
// connection pool (TCP không phải mở/đóng lại mỗi lần) và tổng số request
// chạy song song vẫn bị chặn trên.
type Checker struct {
	client        *http.Client
	timeout       time.Duration
	maxConcurrent int

	// sem chặn số request đồng thời (mỗi lần probe giữ 1 ô trong suốt thời
	// gian request).
	sem chan struct{}
}

// NewChecker tạo Checker với timeout và số request đồng thời.
// timeout <= 0 → DefaultTimeout; maxConcurrent <= 0 → DefaultMaxConcurrent.
func NewChecker(timeout time.Duration, maxConcurrent int) *Checker {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if maxConcurrent <= 0 {
		maxConcurrent = DefaultMaxConcurrent
	}
	return &Checker{
		client: &http.Client{
			Timeout: timeout,
			// Redirect tắt — xem giải thích ở doc package.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
			Transport: &http.Transport{
				Proxy: nil,
				DialContext: (&net.Dialer{
					Timeout:   timeout,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				MaxIdleConns:          maxConcurrent * 2,
				MaxIdleConnsPerHost:   2,
				IdleConnTimeout:       30 * time.Second,
				TLSHandshakeTimeout:   timeout,
				ExpectContinueTimeout: time.Second,
			},
		},
		timeout:       timeout,
		maxConcurrent: maxConcurrent,
		sem:           make(chan struct{}, maxConcurrent),
	}
}

// Timeout trả timeout đang dùng của Checker.
func (c *Checker) Timeout() time.Duration { return c.timeout }

// Probe kiểm tra 1 cổng, trả về 1 HealthEntry.
//
// Không bao giờ trả về lỗi: lỗi mạng được quy thành state no_response để
// 1 cổng hỏng không làm hỏng cả vòng probe.
func (c *Checker) Probe(ctx context.Context, t Target) HealthEntry {
	e := HealthEntry{ID: t.ID, Port: t.Port}

	// Cổng bị bỏ qua → không gọi mạng.
	if t.Skip {
		e.State = StateSkipped
		e.Detail = skipDetail(t)
		return e
	}
	if t.Port <= 0 || t.Port > 65535 {
		e.State = StateSkipped
		e.Detail = "cổng không hợp lệ"
		return e
	}

	// Chặn số request đồng thời (xem Checker.sem).
	c.sem <- struct{}{}
	defer func() { <-c.sem }()

	// Timeout riêng cho lần probe này, kế thừa deadline của caller.
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	url := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(t.Port)) + normalizePath(t.Path)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		e.State = StateNoResponse
		e.Detail = "không tạo được request: " + err.Error()
		return e
	}
	req.Header.Set("User-Agent", "port-manager-health/1")

	start := time.Now()
	resp, err := c.client.Do(req)
	e.LatencyMs = int(time.Since(start).Milliseconds())

	if err != nil {
		// Không có status code → KHÔNG phải http_error, mà là no_response.
		e.State = StateNoResponse
		e.Detail = c.describeError(err)
		return e
	}
	defer resp.Body.Close()
	// Đọc bỏ (giới hạn 4KB) để connection quay lại pool — không đọc thì mỗi
	// probe lại phải mở TCP mới, mất ý nghĩa của client tái dùng.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	e.Code = resp.StatusCode
	if resp.StatusCode >= 400 {
		e.State = StateHTTPError
		e.Detail = fmt.Sprintf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	} else {
		e.State = StateOK
		e.Detail = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return e
}

// ProbeAll probe nhiều cổng SONG SONG, có giới hạn số request đồng thời
// (Checker.maxConcurrent).
//
// Kết quả trả về CÙNG THỨ TỰ với đầu vào để app gắn chip theo (id, port).
// Trả về mảng rỗng (không nil) khi không có target nào.
//
// Sau mỗi vòng, mọi kết nối nhàn rỗi trong pool bị đóng. Vì sao: app từng bị
// `kill -9` nhầm bởi script dọn cổng của service khác (`lsof -ti:3005 | xargs
// kill -9` trong Makefile của assistant-service) — `lsof -ti:<port>` liệt kê
// cả CLIENT đang giữ kết nối tới cổng đó, và app giữ keep-alive 30s sau khi
// probe. Đóng kết nối ngay khi xong vòng thì app không còn xuất hiện trong
// danh sách của lsof giữa hai vòng probe. Trong một vòng, các request cùng
// host vẫn dùng lại kết nối như trước.
func (c *Checker) ProbeAll(ctx context.Context, ts []Target) []HealthEntry {
	out := make([]HealthEntry, len(ts))
	var wg sync.WaitGroup
	for i := range ts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out[i] = c.Probe(ctx, ts[i])
		}(i)
	}
	wg.Wait()
	c.client.CloseIdleConnections()
	return out
}

// defaultChecker dùng chung cho hàm cấp package — nhờ vậy client HTTP (và
// connection pool) được tái dùng giữa các lần gọi rời rạc.
var (
	defaultCheckerOnce sync.Once
	defaultChecker     *Checker
)

func sharedChecker() *Checker {
	defaultCheckerOnce.Do(func() {
		defaultChecker = NewChecker(DefaultTimeout, DefaultMaxConcurrent)
	})
	return defaultChecker
}

// Probe probe 1 cổng bằng Checker dùng chung (timeout mặc định).
// Đóng kết nối nhàn rỗi sau khi xong — cùng lý do với Checker.ProbeAll.
func Probe(ctx context.Context, t Target) HealthEntry {
	c := sharedChecker()
	e := c.Probe(ctx, t)
	c.client.CloseIdleConnections()
	return e
}

// ProbeAll probe nhiều cổng song song bằng Checker dùng chung.
//
// timeout <= 0 → dùng DefaultTimeout. Truyền timeout khác sẽ tạo Checker riêng
// (client riêng) cho vòng probe đó.
func ProbeAll(ctx context.Context, ts []Target, timeout time.Duration) []HealthEntry {
	c := sharedChecker()
	if timeout > 0 && timeout != c.timeout {
		c = NewChecker(timeout, c.maxConcurrent)
	}
	return c.ProbeAll(ctx, ts)
}

// IsListening trả true nếu cổng đang có listener trên 127.0.0.1.
//
// Dùng để lọc cổng trước khi probe — không đụng cổng không mở.
func IsListening(port int, timeout time.Duration) bool {
	if port <= 0 || port > 65535 {
		return false
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// FilterOpen đánh dấu các target có cổng KHÔNG mở thành skipped (kèm lý do),
// để ProbeAll không gửi HTTP request vào cổng không có listener.
//
// Target đã Skip sẵn thì giữ nguyên. Target có cổng đang mở thì giữ nguyên.
// Hàm không sửa input — trả về bản sao.
func FilterOpen(ts []Target, timeout time.Duration) []Target {
	out := make([]Target, len(ts))
	copy(out, ts)
	for i := range out {
		if out[i].Skip {
			continue
		}
		if !IsListening(out[i].Port, timeout) {
			out[i].Skip = true
			if out[i].Reason == "" {
				out[i].Reason = "cổng không mở"
			}
		}
	}
	return out
}

// normalizePath chuẩn hoá path để probe: rỗng → "/", thiếu "/" ở đầu → thêm.
func normalizePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		return "/" + p
	}
	return p
}

// skipDetail trả mô tả ngắn cho trạng thái skipped.
func skipDetail(t Target) string {
	if r := strings.TrimSpace(t.Reason); r != "" {
		return r
	}
	return "bỏ qua theo cấu hình"
}

// describeError dịch lỗi mạng thành mô tả ngắn cho người dùng.
//
// Phân biệt 3 trường hợp: timeout (server treo), connection refused (cổng
// không mở) và lỗi khác.
func (c *Checker) describeError(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "đã hủy"
	case errors.Is(err, context.DeadlineExceeded), os.IsTimeout(err):
		return fmt.Sprintf("không phản hồi trong %s", c.timeout)
	case errors.Is(err, syscall.ECONNREFUSED):
		return "cổng không mở (không có listener)"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Timeout() {
		return fmt.Sprintf("không phản hồi trong %s", c.timeout)
	}
	return "lỗi kết nối: " + err.Error()
}
