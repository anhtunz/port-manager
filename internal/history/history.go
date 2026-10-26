// Package history lưu lịch sử chạy của service: mỗi sự kiện (bật, tắt,
// restart, crash, kẹt, lỗi, sửa cấu hình) ghi thành MỘT DÒNG JSONL trong
// file riêng của service.
//
// Vì sao JSONL: app có thể bị tắt đột ngột (kill -9, treo máy). Ghi append
// từng dòng có vẻ an toàn hơn một file JSON duy nhất — file JSON phải ghi
// lại toàn bộ, tắt giữa chừng là mất sạch; JSONL chỉ mất (nhiều nhất) dòng
// đang ghi dở, và các dòng còn lại vẫn đọc được.
//
// Vì sao tách file theo từng service: đọc lịch sử của 1 service không phải
// quét toàn bộ; và xoá/rotate 1 service không đụng service khác.
package history

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

//
// ── Hợp đồng kind (enum dùng chung với proc/supervisor) ────────────────────
//

// Kind của sự kiện quan sát được. Đây là enum hợp đồng — mọi nơi phát sự
// kiện (proc, supervisor, app) chỉ được dùng các giá trị này.
const (
	KindStart   = "start"   // bật thành công
	KindStop    = "stop"    // dừng có chủ ý
	KindRestart = "restart" // tắt rồi bật lại
	KindCrash   = "crash"   // process chết ngoài ý muốn
	KindStuck   = "stuck"   // còn sống nhưng không lên cổng
	KindFail    = "fail"    // hết lượt restart
	KindEdit    = "edit"    // người dùng sửa cấu hình service
)

// IsValidKind báo kind có nằm trong enum hợp đồng không.
func IsValidKind(kind string) bool {
	switch kind {
	case KindStart, KindStop, KindRestart, KindCrash, KindStuck, KindFail, KindEdit:
		return true
	}
	return false
}

// Kinds trả về toàn bộ kind hợp lệ (thứ tự ổn định).
func Kinds() []string {
	return []string{KindStart, KindStop, KindRestart, KindCrash, KindStuck, KindFail, KindEdit}
}

//
// ── Kiểu dữ liệu ──────────────────────────────────────────────────────────
//

// Event là một sự kiện trong lịch sử chạy của service.
//
// Tên field JSON là hợp đồng với frontend (HistoryEvent) — đổi là đổi hợp
// đồng, không được đổi.
type Event struct {
	At     string `json:"at"`     // thời điểm sự kiện (RFC3339, giờ địa phương)
	Kind   string `json:"kind"`   // start|stop|restart|crash|stuck|fail|edit
	Detail string `json:"detail"` // mô tả tự do, có thể rỗng
}

//
// ── Hằng số mặc định ──────────────────────────────────────────────────────
//

const (
	// FileSuffix: hậu tố file lịch sử của 1 service.
	FileSuffix = ".history.jsonl"

	// MaxSize: ngưỡng dung lượng 1 file lịch sử — vượt ngưỡng thì rotate.
	MaxSize int64 = 256 * 1024

	// KeepLines: số dòng giữ lại sau khi rotate.
	KeepLines = 200

	// DefaultLimit: số sự kiện trả về mặc định khi đọc lịch sử.
	DefaultLimit = 200
)

//
// ── Store ─────────────────────────────────────────────────────────────────
//

// Store ghi/đọc lịch sử chạy của các service trong một thư mục.
//
// Mỗi service có một file: <dir>/<id>.history.jsonl
type Store struct {
	dir string

	mu        sync.Mutex
	maxSize   int64
	keepLines int
	now       func() time.Time
}

// History là tên thay thế của Store (hợp đồng gọi hàm cũ trong tài liệu kế
// hoạch) — cùng kiểu, cùng hàm.
type History = Store

// Options tuỳ chọn cho Store (dùng cho test + chỉnh ngưỡng rotate).
type Options struct {
	// MaxSize: ngưỡng dung lượng file trước khi rotate. <=0 → MaxSize.
	MaxSize int64
	// KeepLines: số dòng giữ lại sau rotate. <=0 → KeepLines.
	KeepLines int
	// Now: nguồn thời gian (test thay bằng hàm cố định). nil → time.Now.
	Now func() time.Time
}

// New tạo Store ghi lịch sử vào thư mục dir.
//
// dir thường là ~/.port-manager/history (xem DefaultDir) — nhưng bất kỳ
// thư mục nào cũng được, kể cả thư mục state (~/.port-manager/state) nếu
// muốn lịch sử nằm cạnh state file. Thư mục chỉ được tạo khi ghi.
func New(dir string) *Store {
	return NewWithOptions(dir, Options{})
}

// NewWithOptions tạo Store với tuỳ chọn (giống New nhưng chỉnh được ngưỡng
// rotate và nguồn thời gian).
func NewWithOptions(dir string, opts Options) *Store {
	s := &Store{
		dir:       dir,
		maxSize:   opts.MaxSize,
		keepLines: opts.KeepLines,
		now:       opts.Now,
	}
	if s.maxSize <= 0 {
		s.maxSize = MaxSize
	}
	if s.keepLines <= 0 {
		s.keepLines = KeepLines
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

// NewDefault tạo Store ở thư mục lịch sử mặc định (~/.port-manager/history).
func NewDefault() *Store { return New(DefaultDir()) }

// DefaultDir trả thư mục lịch sử mặc định: ~/.port-manager/history.
//
// Luôn ở HOME (không phụ thuộc cwd) để app GUI và CLI dùng chung một chỗ.
func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".port-manager", "history")
}

// Dir trả thư mục ghi lịch sử.
func (s *Store) Dir() string { return s.dir }

// Path trả đường dẫn file lịch sử của service id.
func (s *Store) Path(id string) string {
	return filepath.Join(s.dir, id+FileSuffix)
}

//
// ── Ghi ───────────────────────────────────────────────────────────────────
//

// Record ghi một sự kiện cho service id, đóng dấu thời gian lúc ghi.
//
// Trả lỗi (không panic) khi: id không hợp lệ, kind ngoài enum hợp đồng,
// hoặc không ghi được file. Lỗi ghi lịch sử KHÔNG được làm sập app — caller
// (observer) chỉ cần bỏ qua hoặc log, xem RecordOK.
func (s *Store) Record(id, kind, detail string) error {
	return s.RecordAt(id, kind, detail, s.now())
}

// RecordAt giống Record nhưng dùng mốc thời gian cho trước (test, replay).
func (s *Store) RecordAt(id, kind, detail string, at time.Time) error {
	if err := validID(id); err != nil {
		return err
	}
	if !IsValidKind(kind) {
		return fmt.Errorf("loại sự kiện không hợp lệ %q (hợp lệ: %s)", kind, strings.Join(Kinds(), ", "))
	}
	line, err := json.Marshal(Event{At: at.Format(time.RFC3339), Kind: kind, Detail: detail})
	if err != nil {
		return fmt.Errorf("mã hoá sự kiện: %w", err)
	}
	line = append(line, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.dir == "" {
		return fmt.Errorf("chưa có thư mục lịch sử")
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("tạo thư mục lịch sử: %w", err)
	}
	// O_APPEND: mỗi lần ghi luôn bám cuối file → ghi song song (app + CLI)
	// không đè lên nhau. Một Write duy nhất cho trọn dòng → không có trường
	// hợp nửa dòng nếu app bị kill giữa lúc ghi.
	f, err := os.OpenFile(s.Path(id), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("mở file lịch sử: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("ghi lịch sử: %w", err)
	}
	// Sync: đẩy xuống đĩa ngay để app bị tắt đột ngột không mất sự kiện vừa ghi.
	if err := f.Sync(); err != nil {
		return fmt.Errorf("ghi lịch sử ra đĩa: %w", err)
	}
	// Vượt ngưỡng → rotate (giữ N dòng cuối). Lỗi rotate không làm mất dữ
	// liệu đã ghi nên chỉ bỏ qua, không trả lỗi cho caller.
	if st, err := f.Stat(); err == nil && st.Size() > s.maxSize {
		_ = s.rotate(id)
	}
	return nil
}

// RecordOK ghi sự kiện và BỎ QUA lỗi — dành cho observer, nơi lỗi ghi lịch
// sử không được làm sập app.
func (s *Store) RecordOK(id, kind, detail string) {
	_ = s.Record(id, kind, detail)
}

// RecordEvent ghi sự kiện đã dựng sẵn (dùng khi cần giữ đúng mốc thời gian
// do bên khác sinh ra).
func (s *Store) RecordEvent(id string, ev Event) error {
	at := ev.At
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return fmt.Errorf("mốc thời gian không hợp lệ %q: %w", at, err)
	}
	return s.RecordAt(id, ev.Kind, ev.Detail, t)
}

//
// ── Đọc ───────────────────────────────────────────────────────────────────
//

// List trả lịch sử của service id, MỚI NHẤT TRƯỚC, tối đa limit sự kiện.
//
// limit <= 0 → trả tất cả. Service chưa có lịch sử → trả slice rỗng (không
// phải nil) để frontend không phải xử lý null. Dòng hỏng (app bị kill giữa
// lúc ghi, file bị sửa tay) bị bỏ qua, không panic.
func (s *Store) List(id string, limit int) []Event {
	if validID(id) != nil {
		return []Event{} // id không hợp lệ → không đọc ra ngoài thư mục lịch sử
	}
	evs := s.readEvents(s.Path(id))
	sortNewestFirst(evs)
	if limit > 0 && len(evs) > limit {
		evs = evs[:limit]
	}
	return evs
}

// ListAll trả lịch sử của TẤT CẢ service trong thư mục, mỗi service một
// khoá (id), mỗi danh sách mới nhất trước và giới hạn bởi limit.
//
// Service chưa có lịch sử thì không xuất hiện trong map.
func (s *Store) ListAll(limit int) map[string][]Event {
	out := map[string][]Event{}
	for _, id := range s.Services() {
		evs := s.List(id, limit)
		if len(evs) == 0 {
			continue
		}
		out[id] = evs
	}
	return out
}

// ListAllEvents trả TẤT CẢ sự kiện của mọi service gộp lại, mới nhất trước,
// tối đa limit sự kiện.
func (s *Store) ListAllEvents(limit int) []Event {
	var all []Event
	for _, id := range s.Services() {
		all = append(all, s.List(id, 0)...)
	}
	sortNewestFirst(all)
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all
}

// Services trả danh sách id service có lịch sử (đã sắp xếp).
func (s *Store) Services() []string {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), FileSuffix) {
			continue
		}
		ids = append(ids, strings.TrimSuffix(e.Name(), FileSuffix))
	}
	sort.Strings(ids)
	return ids
}

// readEvents đọc 1 file JSONL, bỏ qua dòng hỏng. Không bao giờ trả nil.
func (s *Store) readEvents(path string) []Event {
	evs := []Event{}
	f, err := os.Open(path)
	if err != nil {
		return evs
	}
	defer f.Close()

	// File lịch sử bình thường ≤ 256KB. Nếu file phình to (rotate lỗi, ai đó
	// đổ dữ liệu vào) → chỉ đọc phần cuối, tránh nạp hết vào RAM.
	const maxRead = 4 * 1024 * 1024
	if st, err := f.Stat(); err == nil && st.Size() > maxRead {
		if _, err := f.Seek(-maxRead, io.SeekEnd); err != nil {
			return evs
		}
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue // dòng hỏng → bỏ qua
		}
		if ev.Kind == "" && ev.At == "" && ev.Detail == "" {
			continue // dòng rỗng hợp lệ ("{}") → bỏ qua
		}
		evs = append(evs, ev)
	}
	return evs
}

// sortNewestFirst sắp xếp theo mốc thời gian giảm dần.
//
// File ghi theo thứ tự thời gian tăng dần, nên đảo slice trước rồi mới sort
// ổn định (stable): các sự kiện trong CÙNG một giây (độ phân giải giây của
// RFC3339) sẽ giữ đúng thứ tự ghi — dòng ghi sau đứng trước.
//
// Dòng không parse được mốc thời gian nằm cuối danh sách.
func sortNewestFirst(evs []Event) {
	// Đảo: file order (cũ → mới) thành (mới → cũ).
	for i, j := 0, len(evs)-1; i < j; i, j = i+1, j-1 {
		evs[i], evs[j] = evs[j], evs[i]
	}
	sort.SliceStable(evs, func(i, j int) bool {
		ti, oki := parseAt(evs[i].At)
		tj, okj := parseAt(evs[j].At)
		switch {
		case oki && okj:
			return ti.After(tj)
		case oki:
			return true
		case okj:
			return false
		}
		return false
	})
}

func parseAt(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

//
// ── Rotate ────────────────────────────────────────────────────────────────
//

// rotate giữ lại N dòng cuối của file lịch sử (mặc định 200) rồi ghi lại
// file (ghi file tạm + rename → thay thế nguyên tử, không để lại file nửa vời).
func (s *Store) rotate(id string) error {
	path := s.Path(id)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := splitLines(data)
	if len(lines) > s.keepLines {
		lines = lines[len(lines)-s.keepLines:]
	}
	var buf strings.Builder
	for _, l := range lines {
		buf.WriteString(l)
		buf.WriteByte('\n')
	}
	tmp, err := os.CreateTemp(s.dir, ".history-rotate-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // dọn nếu rename lỗi
	if _, err := tmp.WriteString(buf.String()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// splitLines tách nội dung file thành các dòng (bỏ dòng rỗng).
func splitLines(data []byte) []string {
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		out = append(out, l)
	}
	return out
}

// validID kiểm tra id service có dùng được làm tên file không.
func validID(id string) error {
	if id == "" {
		return fmt.Errorf("thiếu id service")
	}
	if id == "." || id == ".." || strings.ContainsAny(id, "/\\\x00") {
		return fmt.Errorf("id service không hợp lệ %q", id)
	}
	return nil
}
