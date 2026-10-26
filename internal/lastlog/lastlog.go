// Package lastlog đọc dòng lỗi cuối cùng trong file log của service.
//
// Vì sao package riêng: App.Scan() gọi hàm này cho MỌI service mỗi vòng quét
// (chu kỳ 3s). Log của service chạy lâu có thể vài chục MB (đã thấy
// telegram-emulator-be.log ~45MB) → không thể đọc cả file mỗi vòng quét.
// Ở đây chỉ đọc 256KB cuối, và chỉ xét đoạn SAU marker "=== bật lúc … ==="
// cuối cùng để không báo nhầm lỗi của lần chạy trước (R8, R10).
//
// Package cố tình KHÔNG phụ thuộc internal/proc: nhận đường dẫn file log
// (string) nên app.go tự ghép filepath.Join(logDir, id+".log") rồi truyền vào.
//
// API chính:
//
//	LastError(path) string           — dòng lỗi cuối cùng ("" nếu không có lỗi)
//	LastErrorLine(path) (Line, bool) — dòng lỗi cuối cùng kèm số dòng
//	Errors(path, n) []string         — n dòng lỗi cuối cùng, theo thứ tự thời gian
//	IsErrorLine(line) bool           — dòng log này có phải dòng lỗi không
//
// Hàm cấp package dùng Reader dùng chung (đọc lại tối đa 1 lần /
// DefaultInterval cho 1 file). Ai muốn tự quản lý cache/đồng hồ thì dùng
// NewReader(interval) + các method trên Reader.
package lastlog

import (
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	// TailBytes — số byte cuối file log được đọc (256KB). Ràng buộc R8:
	// log có thể nhiều chục MB, không được nạp cả file vào RAM.
	TailBytes = 256 * 1024

	// MaxLineLen — độ dài tối đa (ký tự) của dòng lỗi trả về. Dài hơn →
	// cắt còn MaxLineLen ký tự rồi thêm "…" (mục 6.1 case 7).
	MaxLineLen = 200

	// DefaultInterval — khoảng cách tối thiểu giữa 2 lần đọc lại cùng 1 file
	// log. Ràng buộc R17: log đang được ghi thì (mtime,size) đổi liên tục →
	// cache theo (mtime,size) luôn miss → đọc 256KB × N service mỗi 3s làm
	// app chậm và đĩa nóng. Nên: tối đa đọc lại 1 lần / 10s cho 1 service,
	// nhưng lần đọc kế tiếp vẫn phải thấy dòng lỗi mới.
	DefaultInterval = 10 * time.Second

	// StartMarker — tiền tố marker "bắt đầu lần chạy" mà internal/proc ghi
	// vào log (internal/proc/proc.go: `=== bật lúc 2006-01-02 15:04:05 ===`).
	// Dùng marker này để chỉ xét lần chạy hiện tại.
	StartMarker = "=== bật lúc"

	// maxCollect — số dòng lỗi tối đa giữ lại (mới nhất đứng đầu) cho Errors().
	maxCollect = 100

	// maxStrongLookback — số dòng lỗi "yếu" được phép bỏ qua khi đi tìm dòng
	// lỗi mạnh hơn gần đó. Chặn việc báo một lỗi fatal cũ (nằm sâu trong log)
	// trong khi dòng lỗi gần nhất chỉ là cảnh báo nhẹ — nhưng vẫn đủ để bắt
	// trường hợp "ERROR chi tiết" rồi "make: *** Error 1" ngay sau đó.
	maxStrongLookback = 5
)

// Line là một dòng log tìm được trong phần cuối file.
type Line struct {
	Text    string `json:"text"`    // nội dung dòng (đã trim + cắt nếu dài hơn MaxLineLen)
	No      int    `json:"no"`      // số dòng trong file (1-based); 0 = không xác định (file > TailBytes)
	FromEnd int    `json:"fromEnd"` // số dòng tính từ cuối file (1 = dòng cuối cùng) — luôn chính xác
}

// IsEmpty báo dòng này rỗng (không tìm thấy lỗi nào).
func (l Line) IsEmpty() bool { return l.Text == "" }

// ---------------------------------------------------------------------------
// Phân loại dòng log
// ---------------------------------------------------------------------------

// Mức độ "lỗi" của một dòng log.
const (
	sevNone   = iota // không phải dòng lỗi
	sevWeak          // có dấu hiệu lỗi nhẹ ("error", "err")
	sevStrong        // lỗi rõ ràng ("fatal", "failed", "connection refused", level=error…)
)

// Mức log đọc được từ dòng (JSON "level":"error" hoặc dạng key=value).
const (
	levelError    = "error"
	levelNotError = "noterror"
)

// logLevelJSON bắt mức log kiểu JSON: {"level":"error",…}. Dùng `(?i)` vì
// logger có thể ghi "Level"/"LEVEL". Key phải đứng riêng (có dấu nháy kép)
// để không nhầm với "sublevel".
var logLevelJSON = regexp.MustCompile(`(?i)"(?:level|levelname|severity|log_level)"\s*:\s*"([^"]*)"`)

// logLevelKV bắt mức log kiểu key=value (slog, logfmt): level=error msg=…
// Cần ranh giới trước "level" để không khớp "sublevel=error".
var logLevelKV = regexp.MustCompile(`(?i)(?:^|[\s\[(,;])level\s*[=:]\s*"?([a-zA-Z_]+)"?`)

// Mẫu dòng lỗi rõ ràng (mạnh) — ưu tiên khi tìm dòng lỗi "có ý nghĩa nhất":
// lỗi chắc chắn là lỗi (mức log=error, crash, không kết nối được, …).
var strongPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bfatal\b`),
	regexp.MustCompile(`(?i)\bpanic\b`),
	regexp.MustCompile(`(?i)\bexceptions?\b`),
	regexp.MustCompile(`(?i)\bfailed\b`),
	regexp.MustCompile(`(?i)\bfailure\b`),
	regexp.MustCompile(`(?i)\brefus\w*`), // refused / refusing
	regexp.MustCompile(`(?i)\bcannot\b`),
	regexp.MustCompile(`(?i)\bcan\s+not\b`),
	regexp.MustCompile(`(?i)\bcan'?t\b`),
	regexp.MustCompile(`(?i)\bunable\b`),
	regexp.MustCompile(`(?i)\bdenied\b`),
	regexp.MustCompile(`(?i)\bcrash\w*`),
	regexp.MustCompile(`(?i)\btraceback\b`),
	regexp.MustCompile(`(?i)\bstack\s+trace\b`),
	regexp.MustCompile(`(?i)segmentation\s+fault`),
	regexp.MustCompile(`(?i)core\s+dumped`),
	regexp.MustCompile(`(?i)out\s+of\s+memory`),
	regexp.MustCompile(`(?i)deadline\s+exceeded`),
	regexp.MustCompile(`(?i)\buncaught\b`),
	regexp.MustCompile(`(?i)\bunhandled\b`),
	regexp.MustCompile(`(?i)\bexit\s+(?:code|status)\s+[1-9]`),
	// Mã lỗi hệ đối tượng (Node/Python/Go): EADDRINUSE, ECONNREFUSED…
	regexp.MustCompile(`(?i)\be(?:addrinuse|addrnotavail|connrefused|connreset|connaborted|conntimeout|acces|perm|exist|notdir|isdir|nospc|mfile|nfile|timedout|ai_again|pipe)\b`),
}

// Mẫu dòng lỗi nhẹ (yếu) — dòng chỉ nhắc "error"/"err"/"fail" mà không nói rõ.
// Vẫn là lỗi (để không bỏ sót), nhưng khi có dòng lỗi rõ ràng gần đó thì ưu
// tiên dòng rõ ràng.
var weakPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\berrors?\b`),
	regexp.MustCompile(`(?i)\berr\b`),
	regexp.MustCompile(`(?i)\bfails?\b`),
}

// Mẫu loại trừ — dòng nhắc "error" nhưng KHÔNG phải lỗi:
// "0 errors", "no error", "err: <nil>", "error: null"…
var excludePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(?:0|no|zero)\s+(?:errors?|failures?)\b`),
	regexp.MustCompile(`(?i)\bno\s+errors?\b`),
	regexp.MustCompile(`(?i)\bwithout\s+(?:any\s+)?errors?\b`),
	regexp.MustCompile(`(?i)\berror[-\s]?free\b`),
	regexp.MustCompile(`(?i)\bnot\s+an\s+error\b`),
	regexp.MustCompile(`(?i)"?\b(?:err|error)"?\s*[:=]\s*(?:<nil>|\bnil\b|\bnull\b|\bnone\b|""|'')`),
	regexp.MustCompile(`(?i)"?\b(?:err|error)"?\s*[:=]\s*0\b`),
}

// Dòng chỉ chứa mốc thời gian — bỏ qua (không phải thông tin lỗi).
var timestampOnly = []*regexp.Regexp{
	// 2026-10-22 15:04:05.123 / 2026-10-22T15:04:05Z / 2026-10-22
	regexp.MustCompile(`^\d{4}[-/]\d{2}[-/]\d{2}([T ]\d{1,2}:\d{2}(:\d{2}(\.\d+)?)?)?([Zz]|[+-]\d{2}:?\d{2})?$`),
	// 15:04:05 / 15:04:05.123 / 3:04 PM
	regexp.MustCompile(`^\d{1,2}:\d{2}(:\d{2}(\.\d+)?)?(\s?[APap][Mm])?$`),
}

// IsErrorLine báo một dòng log có phải dòng lỗi hay không.
//
// Dòng rỗng và dòng chỉ chứa mốc thời gian → không phải lỗi. Dòng có mức log
// rõ ràng thì mức quyết định: level=error/fatal/panic → lỗi; level=warn/info/
// debug → KHÔNG phải lỗi (kể cả khi dòng đó có field "error" — trường hợp
// mục 6.1 case 4). Dòng không có mức log thì khớp theo từ khoá.
func IsErrorLine(line string) bool {
	return classify(line) != sevNone
}

// classify phân loại dòng log: sevNone / sevWeak / sevStrong.
func classify(line string) int {
	t := strings.TrimSpace(line)
	if t == "" {
		return sevNone
	}
	if isTimestampOnly(t) {
		return sevNone
	}

	// Mức log (nếu có) là nguồn sự thật — ưu tiên cao nhất.
	if lvl, ok := logLevel(t); ok {
		switch lvl {
		case levelError:
			return sevStrong
		case levelNotError:
			return sevNone
		}
	}

	for _, re := range excludePatterns {
		if re.MatchString(t) {
			return sevNone
		}
	}
	for _, re := range strongPatterns {
		if re.MatchString(t) {
			return sevStrong
		}
	}
	for _, re := range weakPatterns {
		if re.MatchString(t) {
			return sevWeak
		}
	}
	return sevNone
}

// logLevel trả mức log của dòng ("error" / "warn"…) nếu đọc được.
//
// Hỗ trợ 3 dạng hay gặp:
//   - JSON:      {"level":"error","msg":"…"}
//   - key=value: level=error msg=…   (slog, logfmt)
//   - tab:       2026-10-11T…+0700\tERROR\tfile.go:51\tmsg\t{…}  (zap console)
func logLevel(line string) (string, bool) {
	if m := logLevelJSON.FindStringSubmatch(line); m != nil {
		return normalizeLevel(m[1])
	}
	if m := logLevelKV.FindStringSubmatch(line); m != nil {
		return normalizeLevel(m[1])
	}
	// zap: mức log nằm ở cột thứ 2, phân tách bằng tab.
	if fields := strings.Split(line, "\t"); len(fields) >= 2 {
		if lvl, ok := normalizeLevel(fields[1]); ok {
			return lvl, true
		}
	}
	return "", false
}

// normalizeLevel chuẩn hoá tên mức log về 2 nhóm: lỗi / không lỗi.
func normalizeLevel(v string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "error", "err", "fatal", "panic", "critical", "crit", "emerg", "emergency", "alert", "severe", "dpanic":
		return levelError, true
	case "warn", "warning", "info", "information", "informational", "notice", "debug", "trace", "log", "verbose":
		return levelNotError, true
	}
	return "", false
}

// isTimestampOnly báo dòng chỉ chứa mốc thời gian.
func isTimestampOnly(line string) bool {
	t := strings.TrimSpace(line)
	t = strings.Trim(t, "[](){}<>")
	t = strings.TrimSpace(t)
	if t == "" {
		return false
	}
	for _, re := range timestampOnly {
		if re.MatchString(t) {
			return true
		}
	}
	return false
}

// truncate cắt dòng dài còn MaxLineLen ký tự rồi thêm "…".
func truncate(s string) string {
	rs := []rune(s)
	if len(rs) <= MaxLineLen {
		return s
	}
	return string(rs[:MaxLineLen]) + "…"
}

// ---------------------------------------------------------------------------
// Đọc file log
// ---------------------------------------------------------------------------

// candidate là 1 dòng lỗi tìm được kèm mức độ mạnh yếu.
type candidate struct {
	Line
	sev int
}

// result là kết quả 1 lần đọc file log.
type result struct {
	last Line   // dòng lỗi hay nhất gần nhất
	errs []Line // các dòng lỗi, MỚI NHẤT ĐỨNG ĐẦU
}

// readLog đọc 256KB cuối file và trả dòng lỗi hay nhất + danh sách dòng lỗi.
//
// Chỉ đọc phần cuối file (R8): file lớn hơn TailBytes thì seek tới
// size-TailBytes, bỏ dòng đầu bị cắt dở. Chỉ xét đoạn sau marker
// "=== bật lúc … ===" cuối cùng (R10) — nếu marker nằm ngoài vùng đọc
// (log vừa 1 lần chạy dài hơn 256KB) thì coi như toàn bộ vùng đọc thuộc
// lần chạy hiện tại.
func readLog(path string, size int64) result {
	f, err := os.Open(path)
	if err != nil {
		return result{}
	}
	defer f.Close()

	start := int64(0)
	if size > TailBytes {
		start = size - TailBytes
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return result{}
	}
	n := size - start
	if n <= 0 {
		return result{}
	}
	buf := make([]byte, n)
	read, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return result{}
	}
	buf = buf[:read]

	lines := strings.Split(string(buf), "\n")
	// Dòng cuối rỗng do newline cuối file — bỏ.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	// Đọc giữa file → dòng đầu là nửa dòng, không phải dòng hoàn chỉnh.
	partial := start > 0
	if partial && len(lines) > 0 {
		lines = lines[1:]
	}

	// Chỉ xét từ sau marker lần chạy cuối cùng.
	begin := 0
	for i, l := range lines {
		if isStartMarker(l) {
			begin = i + 1
		}
	}

	total := len(lines)
	var cands []candidate
	for i := total - 1; i >= begin && len(cands) < maxCollect; i-- {
		text := strings.TrimSpace(strings.TrimRight(lines[i], "\r"))
		if text == "" {
			continue
		}
		sev := classify(text)
		if sev == sevNone {
			continue
		}
		cands = append(cands, candidate{
			Line: Line{
				Text:    truncate(text),
				No:      absoluteNo(partial, i),
				FromEnd: total - i,
			},
			sev: sev,
		})
	}
	if len(cands) == 0 {
		return result{}
	}

	// Chọn dòng lỗi "có ý nghĩa nhất gần nhất": ưu tiên dòng lỗi rõ ràng
	// (mức log=error, fatal, failed, connection refused…) trong khoảng
	// maxStrongLookback dòng lỗi gần nhất; nếu không có thì lấy dòng lỗi
	// gần nhất dù chỉ "yếu".
	last := cands[0].Line
	weakSeen := 0
	for _, c := range cands {
		if c.sev == sevStrong {
			last = c.Line
			break
		}
		weakSeen++
		if weakSeen > maxStrongLookback {
			break
		}
	}

	errs := make([]Line, 0, len(cands))
	for _, c := range cands {
		errs = append(errs, c.Line)
	}
	return result{last: last, errs: errs}
}

// absoluteNo trả số dòng tuyệt đối trong file, hoặc 0 nếu không xác định
// (đọc từ giữa file → không biết trước đó có bao nhiêu dòng, mà đếm thì
// phải đọc cả file — vi phạm ràng buộc "chỉ đọc phần cuối").
func absoluteNo(partial bool, idx int) int {
	if partial {
		return 0
	}
	return idx + 1
}

// isStartMarker báo dòng có phải marker "bắt đầu lần chạy" không.
func isStartMarker(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), StartMarker)
}

// ---------------------------------------------------------------------------
// Reader — có giới hạn tần suất đọc lại (R17)
// ---------------------------------------------------------------------------

// entry là cache của 1 file log.
type entry struct {
	result result
	readAt time.Time
	mtime  time.Time
	size   int64
	reads  int // số lần thực sự mở file để đọc
}

// Reader đọc dòng lỗi cuối cùng, có cache + giới hạn tần suất đọc lại.
//
// Vì sao cần giới hạn: App.Scan() gọi mỗi 3s cho mọi service; log đang được
// ghi thì (mtime,size) đổi liên tục → nếu cache chỉ theo (mtime,size) thì
// cache luôn miss → đọc 256KB × N service mỗi vòng quét (R17). Ở đây đọc lại
// tối đa 1 lần / interval cho 1 file, nhưng lần đọc kế tiếp vẫn thấy dòng
// lỗi mới.
type Reader struct {
	mu       sync.Mutex
	interval time.Duration
	now      func() time.Time
	entries  map[string]*entry
}

// NewReader tạo Reader với khoảng cách tối thiểu giữa 2 lần đọc lại.
// interval <= 0 → không giới hạn (đọc lại mỗi lần file thay đổi).
func NewReader(interval time.Duration) *Reader {
	return &Reader{
		interval: interval,
		now:      time.Now,
		entries:  map[string]*entry{},
	}
}

// LastError trả dòng lỗi cuối cùng của file log.
// Chuỗi rỗng nếu file không tồn tại hoặc không có dòng lỗi nào.
func (r *Reader) LastError(path string) string {
	return r.lastLine(path).Text
}

// LastErrorLine trả dòng lỗi cuối cùng kèm số dòng.
// ok = false nếu file không tồn tại hoặc không có dòng lỗi nào.
func (r *Reader) LastErrorLine(path string) (Line, bool) {
	ln := r.lastLine(path)
	if ln.IsEmpty() {
		return Line{}, false
	}
	return ln, true
}

// lastLine trả dòng lỗi cuối cùng (rỗng nếu không có).
func (r *Reader) lastLine(path string) Line {
	res, ok := r.load(path)
	if !ok {
		return Line{}
	}
	return res.last
}

// Errors trả n dòng lỗi cuối cùng, theo thứ tự thời gian (cũ → mới).
// Trả về slice rỗng (không phải nil) nếu không có dòng lỗi nào.
func (r *Reader) Errors(path string, n int) []string {
	out := []string{}
	if n <= 0 {
		return out
	}
	res, ok := r.load(path)
	if !ok {
		return out
	}
	// res.errs để mới nhất đứng đầu → lấy n dòng mới nhất rồi đảo lại để trả
	// theo thứ tự thời gian (cũ → mới).
	for i := 0; i < len(res.errs) && len(out) < n; i++ {
		out = append(out, res.errs[i].Text)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// Reads trả số lần thực sự mở file log để đọc — dùng cho test và chẩn đoán.
func (r *Reader) Reads(path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.entries[path]; ok {
		return e.reads
	}
	return 0
}

// load trả kết quả đọc gần nhất, đọc lại file nếu cần (và được phép).
func (r *Reader) load(path string) (result, bool) {
	st, err := os.Stat(path)
	if err != nil {
		// File không tồn tại (hoặc không đọc được) → không có lỗi nào để báo.
		r.mu.Lock()
		delete(r.entries, path)
		r.mu.Unlock()
		return result{}, false
	}
	now := r.now()

	r.mu.Lock()
	e, cached := r.entries[path]
	if cached {
		unchanged := e.mtime.Equal(st.ModTime()) && e.size == st.Size()
		tooSoon := now.Sub(e.readAt) < r.interval
		if unchanged || tooSoon {
			// File không đổi → không cần đọc lại. File đổi nhưng chưa tới hạn
			// → trả kết quả cũ (R17), lần đọc kế tiếp sẽ thấy dòng lỗi mới.
			res := e.result
			r.mu.Unlock()
			return res, true
		}
	}
	r.mu.Unlock()

	res := readLog(path, st.Size())

	r.mu.Lock()
	e, cached = r.entries[path]
	if !cached {
		e = &entry{}
		r.entries[path] = e
	}
	e.result = res
	e.readAt = now
	e.mtime = st.ModTime()
	e.size = st.Size()
	e.reads++
	r.mu.Unlock()
	return res, true
}

// ---------------------------------------------------------------------------
// Hàm cấp package — app.go gọi trực tiếp (hợp đồng mục 3.12)
// ---------------------------------------------------------------------------

// defaultReader — Reader dùng chung, giới hạn đọc lại 1 lần / DefaultInterval.
var defaultReader = NewReader(DefaultInterval)

// LastError trả dòng lỗi cuối cùng trong file log.
// Chuỗi rỗng nếu file không tồn tại hoặc không có dòng lỗi nào.
func LastError(path string) string { return defaultReader.LastError(path) }

// LastErrorLine trả dòng lỗi cuối cùng kèm số dòng.
func LastErrorLine(path string) (Line, bool) { return defaultReader.LastErrorLine(path) }

// Errors trả n dòng lỗi cuối cùng, theo thứ tự thời gian (cũ → mới).
func Errors(path string, n int) []string { return defaultReader.Errors(path, n) }
