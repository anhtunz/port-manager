package health

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// portOf lấy port thật của httptest server.
func portOf(srv *httptest.Server) int {
	return srv.Listener.Addr().(*net.TCPAddr).Port
}

// serverOK trả 200 cho mọi path.
func serverOK() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
}

// serverCode trả mã cố định.
func serverCode(code int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
	}))
}

// serverTreo giữ request lại (không trả lời) cho tới khi client bỏ cuộc.
func serverTreo() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
}

// ---------------------------------------------------------------------------
// 4 trạng thái
// ---------------------------------------------------------------------------

// Probe vào server trả 200 → ok, có mã HTTP và độ trễ.
func TestProbeOK(t *testing.T) {
	srv := serverOK()
	defer srv.Close()

	c := NewChecker(1500*time.Millisecond, 8)
	e := c.Probe(context.Background(), Target{ID: "wm-be", Port: portOf(srv)})

	if e.State != StateOK {
		t.Fatalf("state = %q, muốn %q (detail %q)", e.State, StateOK, e.Detail)
	}
	if e.Code != 200 {
		t.Fatalf("code = %d, muốn 200", e.Code)
	}
	if e.LatencyMs < 0 {
		t.Fatalf("latencyMs = %d, phải >= 0", e.LatencyMs)
	}
	if e.ID != "wm-be" || e.Port != portOf(srv) {
		t.Fatalf("id/port sai: %q/%d", e.ID, e.Port)
	}
	if !strings.Contains(e.Detail, "200") {
		t.Fatalf("detail = %q, phải nhắc mã 200", e.Detail)
	}
}

// Server trả 500 → http_error (KHÁC no_response: có mã lỗi do server trả).
func TestProbeHTTPError(t *testing.T) {
	srv := serverCode(http.StatusInternalServerError)
	defer srv.Close()

	c := NewChecker(1500*time.Millisecond, 8)
	e := c.Probe(context.Background(), Target{ID: "auth-be", Port: portOf(srv)})

	if e.State != StateHTTPError {
		t.Fatalf("state = %q, muốn %q", e.State, StateHTTPError)
	}
	if e.Code != 500 {
		t.Fatalf("code = %d, muốn 500", e.Code)
	}
	if !strings.Contains(e.Detail, "500") {
		t.Fatalf("detail = %q, phải nhắc mã 500", e.Detail)
	}
}

// Server đã tắt (cổng không mở) → no_response, không có mã HTTP.
func TestProbeNoResponseCongDong(t *testing.T) {
	srv := serverOK()
	port := portOf(srv)
	srv.Close() // cổng không còn listener

	c := NewChecker(1500*time.Millisecond, 8)
	e := c.Probe(context.Background(), Target{ID: "wm-be", Port: port})

	if e.State != StateNoResponse {
		t.Fatalf("state = %q, muốn %q (detail %q)", e.State, StateNoResponse, e.Detail)
	}
	if e.Code != 0 {
		t.Fatalf("code = %d, phải 0 (không có HTTP response)", e.Code)
	}
	if !strings.Contains(e.Detail, "cổng không mở") {
		t.Fatalf("detail = %q, phải nói cổng không mở", e.Detail)
	}
}

// Server treo không trả lời → hết timeout → no_response (không phải http_error).
func TestProbeNoResponseTimeout(t *testing.T) {
	srv := serverTreo()
	defer srv.Close()

	c := NewChecker(150*time.Millisecond, 8)
	e := c.Probe(context.Background(), Target{ID: "fe", Port: portOf(srv)})

	if e.State != StateNoResponse {
		t.Fatalf("state = %q, muốn %q (detail %q)", e.State, StateNoResponse, e.Detail)
	}
	if e.Code != 0 {
		t.Fatalf("code = %d, phải 0", e.Code)
	}
	if e.LatencyMs < 100 {
		t.Fatalf("latencyMs = %d, phải >= 100 (đã chờ hết timeout)", e.LatencyMs)
	}
	if !strings.Contains(e.Detail, "không phản hồi") {
		t.Fatalf("detail = %q, phải nói không phản hồi", e.Detail)
	}
}

// Target bị skip → skipped, KHÔNG gọi mạng (server không nhận request nào).
func TestProbeSkippedKhongGoiMang(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewChecker(1500*time.Millisecond, 8)
	e := c.Probe(context.Background(), Target{
		ID:     "auth-be",
		Port:   portOf(srv),
		Skip:   true,
		Reason: "cổng gRPC — không probe HTTP",
	})

	if e.State != StateSkipped {
		t.Fatalf("state = %q, muốn %q", e.State, StateSkipped)
	}
	if e.Code != 0 {
		t.Fatalf("code = %d, phải 0", e.Code)
	}
	if !strings.Contains(e.Detail, "gRPC") {
		t.Fatalf("detail = %q, phải giữ lý do bỏ qua", e.Detail)
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("server nhận %d request, phải 0 (skipped không gọi mạng)", n)
	}
}

// Port không hợp lệ → skipped, không gọi mạng.
func TestProbePortKhongHopLe(t *testing.T) {
	c := NewChecker(1500*time.Millisecond, 8)
	for _, p := range []int{0, -1, 70000} {
		e := c.Probe(context.Background(), Target{ID: "x", Port: p})
		if e.State != StateSkipped {
			t.Fatalf("port %d: state = %q, muốn %q", p, e.State, StateSkipped)
		}
	}
}

// ---------------------------------------------------------------------------
// ProbeAll: song song + giới hạn đồng thời
// ---------------------------------------------------------------------------

// ProbeAll chạy SONG SONG: 8 server mỗi server chậm 300ms → tổng < 1.5s
// (nếu chạy tuần tự thì ~2.4s).
func TestProbeAllSongSong(t *testing.T) {
	var servers []*httptest.Server
	var targets []Target
	for i := 0; i < 8; i++ {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(300 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		}))
		servers = append(servers, srv)
		targets = append(targets, Target{ID: "svc-" + strconv.Itoa(i), Port: portOf(srv)})
	}
	defer func() {
		for _, s := range servers {
			s.Close()
		}
	}()

	start := time.Now()
	out := ProbeAll(context.Background(), targets, 2*time.Second)
	elapsed := time.Since(start)

	if len(out) != len(targets) {
		t.Fatalf("len(out) = %d, muốn %d", len(out), len(targets))
	}
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("probe mất %s — không chạy song song", elapsed)
	}
	// Thứ tự kết quả phải khớp thứ tự input.
	for i := range out {
		if out[i].ID != targets[i].ID || out[i].Port != targets[i].Port {
			t.Fatalf("thứ tự sai ở %d: %+v", i, out[i])
		}
		if out[i].State != StateOK {
			t.Fatalf("target %d: state = %q (%s)", i, out[i].State, out[i].Detail)
		}
	}
}

// Số request đồng thời bị giới hạn theo maxConcurrent.
func TestProbeAllGioiHanDongThoi(t *testing.T) {
	var cur, max int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&cur, 1)
		for {
			m := atomic.LoadInt32(&max)
			if n <= m || atomic.CompareAndSwapInt32(&max, m, n) {
				break
			}
		}
		time.Sleep(80 * time.Millisecond)
		atomic.AddInt32(&cur, -1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	targets := make([]Target, 12)
	for i := range targets {
		targets[i] = Target{ID: "svc-" + strconv.Itoa(i), Port: portOf(srv)}
	}

	c := NewChecker(2*time.Second, 4)
	c.ProbeAll(context.Background(), targets)

	if m := atomic.LoadInt32(&max); m > 4 {
		t.Fatalf("có %d request chạy song song, giới hạn là 4", m)
	}
	if m := atomic.LoadInt32(&max); m < 2 {
		t.Fatalf("chỉ %d request chạy song song — mất tính song song", m)
	}
}

// ProbeAll với danh sách rỗng → mảng rỗng (không nil).
func TestProbeAllRong(t *testing.T) {
	out := ProbeAll(context.Background(), nil, DefaultTimeout)
	if out == nil {
		t.Fatal("out = nil, phải là mảng rỗng")
	}
	if len(out) != 0 {
		t.Fatalf("len(out) = %d, phải 0", len(out))
	}
}

// ---------------------------------------------------------------------------
// Path probe
// ---------------------------------------------------------------------------

// Path lấy từ target (health_path): server chỉ trả 200 đúng path /healthz.
func TestProbePathTuTarget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewChecker(1500*time.Millisecond, 8)

	ok := c.Probe(context.Background(), Target{ID: "wm-be", Port: portOf(srv), Path: "/healthz"})
	if ok.State != StateOK || ok.Code != 200 {
		t.Fatalf("path /healthz: state=%q code=%d, muốn ok/200", ok.State, ok.Code)
	}

	ko := c.Probe(context.Background(), Target{ID: "wm-be", Port: portOf(srv), Path: "/khong-ton-tai"})
	if ko.State != StateHTTPError || ko.Code != 404 {
		t.Fatalf("path sai: state=%q code=%d, muốn http_error/404", ko.State, ko.Code)
	}
}

// Path rỗng → probe vào "/" (service không khai health_path).
func TestProbePathRongRaRoot(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewChecker(1500*time.Millisecond, 8)
	e := c.Probe(context.Background(), Target{ID: "wm-be", Port: portOf(srv)})

	if e.State != StateOK {
		t.Fatalf("state = %q, muốn ok", e.State)
	}
	if gotPath != "/" {
		t.Fatalf("path = %q, muốn \"/\"", gotPath)
	}
}

// Redirect tắt: không đi theo, nhận đúng mã 3xx.
func TestProbeKhongTheoRedirect(t *testing.T) {
	var finalHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final" {
			atomic.AddInt32(&finalHits, 1)
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, "/final", http.StatusFound)
	}))
	defer srv.Close()

	c := NewChecker(1500*time.Millisecond, 8)
	e := c.Probe(context.Background(), Target{ID: "fe", Port: portOf(srv)})

	if e.Code != http.StatusFound {
		t.Fatalf("code = %d, muốn 302 (redirect phải tắt)", e.Code)
	}
	if e.State != StateOK {
		t.Fatalf("state = %q, muốn ok (server có trả lời)", e.State)
	}
	if n := atomic.LoadInt32(&finalHits); n != 0 {
		t.Fatalf("đã theo redirect (%d lần) — cấu hình redirect phải tắt", n)
	}
}

// ---------------------------------------------------------------------------
// Không đụng cổng không mở + tái dùng client
// ---------------------------------------------------------------------------

// IsListening đúng với cổng đang mở / đã đóng.
func TestIsListening(t *testing.T) {
	srv := serverOK()
	defer srv.Close()
	port := portOf(srv)

	if !IsListening(port, 500*time.Millisecond) {
		t.Fatal("cổng đang mở nhưng IsListening = false")
	}
	srv.Close()
	if IsListening(port, 500*time.Millisecond) {
		t.Fatal("cổng đã đóng nhưng IsListening = true")
	}
}

// FilterOpen đánh dấu cổng không mở thành skipped → không gọi mạng vào cổng đó.
func TestFilterOpen(t *testing.T) {
	srv := serverOK()
	defer srv.Close()
	open := portOf(srv)

	closed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedPort := portOf(closed)
	closed.Close()

	in := []Target{
		{ID: "a", Port: open},
		{ID: "b", Port: closedPort},
	}
	out := FilterOpen(in, 300*time.Millisecond)

	if out[0].Skip {
		t.Fatal("cổng đang mở bị đánh dấu skip")
	}
	if !out[1].Skip {
		t.Fatal("cổng không mở phải được đánh dấu skip")
	}
	if out[1].Reason == "" {
		t.Fatal("target skip phải có lý do")
	}
	// Input không bị sửa.
	if in[1].Skip {
		t.Fatal("FilterAll sửa cả input — phải trả bản sao")
	}

	// Probe cổng đã đánh dấu skip → không gọi mạng.
	c := NewChecker(1500*time.Millisecond, 8)
	e := c.Probe(context.Background(), out[1])
	if e.State != StateSkipped {
		t.Fatalf("state = %q, muốn skipped", e.State)
	}
}

// Client tái dùng: 2 probe liên tiếp vào cùng server chỉ mở 1 kết nối TCP.
func TestClientTaiDungungKetNoi(t *testing.T) {
	var conns int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.Config.ConnState = func(c net.Conn, s http.ConnState) {
		if s == http.StateNew {
			atomic.AddInt32(&conns, 1)
		}
	}
	srv.Start()
	defer srv.Close()

	c := NewChecker(1500*time.Millisecond, 8)
	for i := 0; i < 2; i++ {
		e := c.Probe(context.Background(), Target{ID: "wm-be", Port: portOf(srv)})
		if e.State != StateOK {
			t.Fatalf("probe %d: state = %q", i, e.State)
		}
	}
	if n := atomic.LoadInt32(&conns); n != 1 {
		t.Fatalf("mở %d kết nối TCP cho 2 probe — client chưa tái dùng connection", n)
	}
}

// ---------------------------------------------------------------------------
// Hợp đồng JSON
// ---------------------------------------------------------------------------

// JSON đúng tên field camelCase và đúng thứ tự key.
func TestHealthEntryJSON(t *testing.T) {
	b, err := json.Marshal(HealthEntry{
		ID:        "x",
		Port:      3000,
		State:     StateOK,
		Code:      200,
		LatencyMs: 12,
		Detail:    "",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"x","port":3000,"state":"ok","code":200,"latencyMs":12,"detail":""}`
	if string(b) != want {
		t.Fatalf("JSON = %s\nmuốn  = %s", b, want)
	}
}

// ---------------------------------------------------------------------------
// Không giữ kết nối nhàn rỗi sau vòng probe
// ---------------------------------------------------------------------------

// Sau ProbeAll, server không còn kết nối nào mở từ client — đây là điều kiện
// để `lsof -ti:<port>` của script dọn cổng không bắt nhầm app.
func TestProbeAllDongKetNoiNhanRoi(t *testing.T) {
	var open int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		switch st {
		case http.StateNew:
			atomic.AddInt32(&open, 1)
		case http.StateClosed, http.StateHijacked:
			atomic.AddInt32(&open, -1)
		}
	}
	srv.Start()
	defer srv.Close()

	c := NewChecker(time.Second, 4)
	out := c.ProbeAll(context.Background(), []Target{
		{ID: "a", Port: portOf(srv)},
		{ID: "b", Port: portOf(srv)},
		{ID: "c", Port: portOf(srv)},
	})
	for _, e := range out {
		if e.State != StateOK {
			t.Fatalf("probe %s = %s (%s), muốn ok", e.ID, e.State, e.Detail)
		}
	}
	// Đóng ở phía client; server nhận FIN và chuyển StateClosed trong vài ms.
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&open) > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := atomic.LoadInt32(&open); n != 0 {
		t.Fatalf("còn %d kết nối mở sau ProbeAll — pool keep-alive chưa được đóng", n)
	}
}
