package lastlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeLog ghi nội dung vào file log tạm, trả về đường dẫn.
func writeLog(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "svc.log")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// appendLog nối thêm vào file log tạm (giả lập process đang ghi log).
func appendLog(t *testing.T, p, content string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
}

// reader tạo Reader không giới hạn tần suất đọc lại (cho test chức năng).
func reader() *Reader { return NewReader(0) }

// ---------------------------------------------------------------------------
// Mục 6.1 case (1) — không có file
// ---------------------------------------------------------------------------

func TestMissingFileReturnsEmpty(t *testing.T) {
	r := reader()
	p := filepath.Join(t.TempDir(), "khong-co.log")
	if got := r.LastError(p); got != "" {
		t.Fatalf("file không tồn tại phải trả \"\", nhận %q", got)
	}
	if _, ok := r.LastErrorLine(p); ok {
		t.Fatal("file không tồn tại phải trả ok=false")
	}
	if got := r.Errors(p, 5); len(got) != 0 {
		t.Fatalf("Errors phải rỗng, nhận %v", got)
	}
}

// ---------------------------------------------------------------------------
// Mục 6.1 case (2) — log không có lỗi
// ---------------------------------------------------------------------------

func TestLogWithoutErrorReturnsEmpty(t *testing.T) {
	r := reader()
	p := writeLog(t, "=== bật lúc 2026-10-22 10:00:00 ===\n"+
		"server đang lắng nghe ở cổng 3000\n"+
		"2026-10-22 10:00:01 GET /api/health 200\n"+
		"{\"level\":\"info\",\"msg\":\"started\"}\n")
	if got := r.LastError(p); got != "" {
		t.Fatalf("log không có lỗi phải trả \"\", nhận %q", got)
	}
}

func TestEmptyFileReturnsEmpty(t *testing.T) {
	r := reader()
	p := writeLog(t, "")
	if got := r.LastError(p); got != "" {
		t.Fatalf("file rỗng phải trả \"\", nhận %q", got)
	}
}

// ---------------------------------------------------------------------------
// Mục 6.1 case (3) — dòng "level":"error"
// ---------------------------------------------------------------------------

func TestJSONLevelErrorIsReturned(t *testing.T) {
	r := reader()
	want := `{"level":"error","msg":"connect ECONNREFUSED 127.0.0.1:5432"}`
	p := writeLog(t, "=== bật lúc 2026-10-22 10:00:00 ===\n"+
		"{\"level\":\"info\",\"msg\":\"booting\"}\n"+
		want+"\n")
	got := r.LastError(p)
	if got != want {
		t.Fatalf("sai dòng lỗi:\n nhận %q\n muốn  %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// Mục 6.1 case (4) — dòng "level":"warn" có field "error" → KHÔNG phải lỗi
// ---------------------------------------------------------------------------

func TestWarnLevelWithErrorFieldIsNotAnError(t *testing.T) {
	r := reader()
	p := writeLog(t, "=== bật lúc 2026-10-22 10:00:00 ===\n"+
		`{"level":"warn","msg":"retry","error":"connection refused"}`+"\n"+
		`{"level":"info","msg":"ok"}`+"\n")
	if got := r.LastError(p); got != "" {
		t.Fatalf("dòng warn có field error không được coi là lỗi, nhận %q", got)
	}
	if IsErrorLine(`{"level":"warn","msg":"retry","error":"connection refused"}`) {
		t.Fatal("IsErrorLine phải trả false cho dòng warn có field error")
	}
}

// ---------------------------------------------------------------------------
// Mục 6.1 case (5) — lỗi của lần chạy trước (trước marker) → bỏ qua
// ---------------------------------------------------------------------------

func TestErrorFromPreviousRunIsIgnored(t *testing.T) {
	r := reader()
	p := writeLog(t, "=== bật lúc 2026-10-22 09:00:00 ===\n"+
		"ERROR lỗi của lần chạy trước\n"+
		"=== bật lúc 2026-10-22 11:00:00 ===\n"+
		"server đang chạy bình thường\n")
	if got := r.LastError(p); got != "" {
		t.Fatalf("lỗi lần chạy trước phải bị bỏ qua, nhận %q", got)
	}
}

func TestErrorOfCurrentRunWinsOverPreviousRun(t *testing.T) {
	r := reader()
	want := "ERROR lỗi của lần chạy hiện tại"
	p := writeLog(t, "=== bật lúc 2026-10-22 09:00:00 ===\n"+
		"ERROR lỗi cũ\n"+
		"=== bật lúc 2026-10-22 11:00:00 ===\n"+
		want+"\n")
	if got := r.LastError(p); got != want {
		t.Fatalf("sai dòng lỗi:\n nhận %q\n muốn  %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// Mục 6.1 case (6) — file > 256KB vẫn tìm được lỗi ở đuôi
// ---------------------------------------------------------------------------

func TestLargeFileStillFindsErrorAtTail(t *testing.T) {
	r := reader()
	var sb strings.Builder
	sb.WriteString("=== bật lúc 2026-10-22 10:00:00 ===\n")
	for sb.Len() < TailBytes+4096 {
		sb.WriteString("dòng log bình thường, không có gì đặc biệt ở đây\n")
	}
	want := `{"level":"error","msg":"EADDRINUSE: address already in use :::3000"}`
	sb.WriteString(want + "\n")
	p := writeLog(t, sb.String())

	got := r.LastError(p)
	if got != want {
		t.Fatalf("file lớn phải tìm được lỗi ở đuôi:\n nhận %q\n muốn  %q", got, want)
	}
}

func TestOnlyTailIsRead(t *testing.T) {
	// Lỗi nằm ở ĐẦU file, file lớn hơn 256KB → không được thấy.
	// Chứng minh chỉ đọc phần cuối, không nạp cả file dài.
	r := reader()
	var sb strings.Builder
	sb.WriteString("ERROR lỗi nằm ở đầu file\n")
	for sb.Len() < TailBytes+4096 {
		sb.WriteString("dòng log bình thường\n")
	}
	p := writeLog(t, sb.String())
	if got := r.LastError(p); got != "" {
		t.Fatalf("chỉ được đọc phần cuối file, nhận %q", got)
	}
}

// ---------------------------------------------------------------------------
// Mục 6.1 case (7) — dòng lỗi dài > 200 ký tự bị cắt + "…"
// ---------------------------------------------------------------------------

func TestLongErrorLineIsTruncated(t *testing.T) {
	r := reader()
	long := "ERROR " + strings.Repeat("x", 400)
	p := writeLog(t, long+"\n")
	got := r.LastError(p)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("dòng dài phải bị cắt + \"…\", nhận %q", got)
	}
	if n := len([]rune(got)); n != MaxLineLen+1 {
		t.Fatalf("dài sau khi cắt = %d, muốn %d", n, MaxLineLen+1)
	}
	if !strings.HasPrefix(got, "ERROR xxx") {
		t.Fatalf("phần giữ lại sai: %q", got)
	}
}

// ---------------------------------------------------------------------------
// Mục 6.1 case (8) — giới hạn tần suất đọc lại (R17)
// ---------------------------------------------------------------------------

// fakeClock đồng hồ giả để test giới hạn tần suất không phụ thuộc thời gian thật.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func TestThrottleLimitsReadsButFindsNewError(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)}
	r := NewReader(10 * time.Second)
	r.now = clock.now

	p := filepath.Join(t.TempDir(), "svc.log")
	if err := os.WriteFile(p, []byte("=== bật lúc 2026-10-22 10:00:00 ===\nserver started\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := r.LastError(p); got != "" {
		t.Fatalf("chưa có lỗi mà báo %q", got)
	}
	if n := r.Reads(p); n != 1 {
		t.Fatalf("số lần đọc = %d, muốn 1", n)
	}

	// File đang được ghi liên tục (size/mtime đổi mỗi lần) → không được đọc
	// lại ở mỗi lần gọi.
	for i := 0; i < 20; i++ {
		appendLog(t, p, "đang chạy...\n")
		if got := r.LastError(p); got != "" {
			t.Fatalf("chưa có lỗi mà báo %q", got)
		}
	}
	if n := r.Reads(p); n != 1 {
		t.Fatalf("phải giữ 1 lần đọc khi file đang được ghi, đọc %d lần", n)
	}

	// Hết khoảng thời gian cho phép → đọc lại và PHẢI thấy dòng lỗi mới.
	clock.advance(11 * time.Second)
	want := `{"level":"error","msg":"crash mới"}`
	appendLog(t, p, want+"\n")
	if got := r.LastError(p); got != want {
		t.Fatalf("sau khi hết hạn phải thấy lỗi mới:\n nhận %q\n muốn  %q", got, want)
	}
	if n := r.Reads(p); n != 2 {
		t.Fatalf("số lần đọc = %d, muốn 2", n)
	}
}

func TestUnchangedFileIsNotReadAgain(t *testing.T) {
	r := reader()
	p := writeLog(t, "ERROR lỗi\n")
	if got := r.LastError(p); got != "ERROR lỗi" {
		t.Fatalf("nhận %q", got)
	}
	if got := r.LastError(p); got != "ERROR lỗi" {
		t.Fatalf("nhận %q", got)
	}
	if n := r.Reads(p); n != 1 {
		t.Fatalf("file không đổi thì không đọc lại, đọc %d lần", n)
	}
}

// ---------------------------------------------------------------------------
// Số dòng (F1.3: dòng lỗi kèm số dòng)
// ---------------------------------------------------------------------------

func TestLastErrorLineCarriesLineNumber(t *testing.T) {
	r := reader()
	p := writeLog(t, "dòng 1\ndòng 2\nERROR hỏng rồi\ndòng 4\n")
	ln, ok := r.LastErrorLine(p)
	if !ok {
		t.Fatal("phải tìm thấy dòng lỗi")
	}
	if ln.Text != "ERROR hỏng rồi" {
		t.Fatalf("sai nội dung: %q", ln.Text)
	}
	if ln.No != 3 {
		t.Fatalf("số dòng = %d, muốn 3", ln.No)
	}
	if ln.FromEnd != 2 {
		t.Fatalf("số dòng tính từ cuối = %d, muốn 2", ln.FromEnd)
	}
}

func TestLargeFileLineNumberUnknown(t *testing.T) {
	// File lớn hơn cửa sổ đọc → không biết số dòng tuyệt đối (không đọc cả
	// file để đếm) nhưng vẫn biết vị trí tính từ cuối file.
	r := reader()
	var sb strings.Builder
	for sb.Len() < TailBytes+4096 {
		sb.WriteString("dòng log bình thường\n")
	}
	sb.WriteString("ERROR lỗi cuối\n")
	p := writeLog(t, sb.String())
	ln, ok := r.LastErrorLine(p)
	if !ok {
		t.Fatal("phải tìm thấy dòng lỗi")
	}
	if ln.No != 0 {
		t.Fatalf("số dòng tuyệt đối phải = 0 khi không xác định, nhận %d", ln.No)
	}
	if ln.FromEnd != 1 {
		t.Fatalf("số dòng tính từ cuối = %d, muốn 1", ln.FromEnd)
	}
}

// ---------------------------------------------------------------------------
// Errors(path, n)
// ---------------------------------------------------------------------------

func TestErrorsReturnsLastNInChronologicalOrder(t *testing.T) {
	r := reader()
	p := writeLog(t, "=== bật lúc 2026-10-22 10:00:00 ===\n"+
		"ERROR lỗi 1\n"+
		"ERROR lỗi 2\n"+
		"ERROR lỗi 3\n")
	got := r.Errors(p, 2)
	want := []string{"ERROR lỗi 2", "ERROR lỗi 3"}
	if len(got) != len(want) {
		t.Fatalf("nhận %v, muốn %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("nhận %v, muốn %v", got, want)
		}
	}
	if all := r.Errors(p, 10); len(all) != 3 {
		t.Fatalf("phải trả cả 3 dòng lỗi, nhận %v", all)
	}
	if none := r.Errors(p, 0); len(none) != 0 {
		t.Fatalf("n=0 phải rỗng, nhận %v", none)
	}
}

// ---------------------------------------------------------------------------
// IsErrorLine — nhận biết mẫu lỗi
// ---------------------------------------------------------------------------

func TestIsErrorLinePatterns(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		// Mẫu lỗi thường gặp (yêu cầu F1.3).
		{"error: something went wrong", true},
		{"err: something went wrong", true},
		{"fatal: cannot continue", true},
		{"panic: runtime error", true},
		{"exception in thread", true},
		{"failed to connect to database", true},
		{"connect ECONNREFUSED 127.0.0.1:5432", true},
		{"cannot bind to port 3000", true},
		{"listen EADDRINUSE: address already in use :::3000", true},
		// Dạng viết hoa không dấu.
		{"ERROR", true},
		{"FATAL", true},
		{"PANIC", true},
		{"Exception", true},
		{"Failed to load config", true},
		// Dòng không phải lỗi.
		{"", false},
		{"   ", false},
		{"server started on port 3000", false},
		{"GET /api/health 200 OK", false},
		{"2026-10-22 15:04:05", false},
		{"15:04:05", false},
		{"[2026-10-22T15:04:05.123Z]", false},
		{"2026-10-22", false},
		// Trường hợp "nhắc lỗi" nhưng không phải lỗi.
		{"compiled with 0 errors", false},
		{"no errors found", false},
		{"err: <nil>", false},
		{`{"err":null}`, false},
		{"error-free build", false},
	}
	for _, c := range cases {
		if got := IsErrorLine(c.line); got != c.want {
			t.Fatalf("IsErrorLine(%q) = %v, muốn %v", c.line, got, c.want)
		}
	}
}

func TestTimestampOnlyLinesAreIgnored(t *testing.T) {
	r := reader()
	p := writeLog(t, "2026-10-22 15:04:05\n15:04:06\n[2026-10-22T15:04:07Z]\n")
	if got := r.LastError(p); got != "" {
		t.Fatalf("dòng mốc thời gian không phải lỗi, nhận %q", got)
	}
}

func TestEmptyLinesAreIgnored(t *testing.T) {
	r := reader()
	p := writeLog(t, "\n   \n\t\n")
	if got := r.LastError(p); got != "" {
		t.Fatalf("dòng rỗng không phải lỗi, nhận %q", got)
	}
}

// ---------------------------------------------------------------------------
// Mức log kiểu zap (mức nằm ở cột 2, phân tách bằng tab) — thấy trong log thật
// (logs/telegram-emulator-be.log)
// ---------------------------------------------------------------------------

func TestZapTabSeparatedLevel(t *testing.T) {
	errLine := "2026-10-11T22:53:41.287+0700\tERROR\temulator/user_manager.go:51\tОшибка создания\t{\"error\": \"UNIQUE constraint failed\"}"
	warnLine := "2026-10-11T22:53:41.287+0700\tWARN\temulator/user_manager.go:51\tretry\t{\"error\": \"UNIQUE constraint failed\"}"

	if !IsErrorLine(errLine) {
		t.Fatal("dòng zap mức ERROR phải là dòng lỗi")
	}
	if IsErrorLine(warnLine) {
		t.Fatal("dòng zap mức WARN không được coi là dòng lỗi")
	}

	r := reader()
	p := writeLog(t, warnLine+"\n")
	if got := r.LastError(p); got != "" {
		t.Fatalf("dòng zap mức WARN không được coi là lỗi, nhận %q", got)
	}
	p = writeLog(t, warnLine+"\n"+errLine+"\n")
	if got := r.LastError(p); got != errLine {
		t.Fatalf("sai dòng lỗi:\n nhận %q\n muốn  %q", got, errLine)
	}
}

// ---------------------------------------------------------------------------
// Chọn dòng lỗi "có ý nghĩa nhất gần nhất"
// ---------------------------------------------------------------------------

func TestPrefersMeaningfulErrorOverWeakOne(t *testing.T) {
	r := reader()
	p := writeLog(t, "=== bật lúc 2026-10-22 10:00:00 ===\n"+
		"panic: hỏng rồi\n"+
		"error nhẹ\n")
	got := r.LastError(p)
	if got != "panic: hỏng rồi" {
		t.Fatalf("phải ưu tiên dòng lỗi rõ ràng, nhận %q", got)
	}
}

func TestFallsBackToMostRecentWeakError(t *testing.T) {
	r := reader()
	p := writeLog(t, "=== bật lúc 2026-10-22 10:00:00 ===\n"+
		"error cũ hơn\n"+
		"error nhẹ nhất\n")
	got := r.LastError(p)
	if got != "error nhẹ nhất" {
		t.Fatalf("phải trả dòng lỗi gần nhất, nhận %q", got)
	}
}

func TestOldStrongErrorIsNotReportedWhenTooManyWeakErrorsFollow(t *testing.T) {
	// Lỗi mạnh nằm quá xa (nhiều dòng lỗi yếu hơn đứng sau) → trả dòng lỗi
	// gần nhất, không báo lỗi cũ.
	r := reader()
	content := "=== bật lúc 2026-10-22 10:00:00 ===\npanic: lỗi cũ\n"
	for i := 0; i < maxStrongLookback+2; i++ {
		content += "error yếu " + string(rune('a'+i)) + "\n"
	}
	p := writeLog(t, content)
	got := r.LastError(p)
	want := "error yếu " + string(rune('a'+maxStrongLookback+1))
	if got != want {
		t.Fatalf("phải trả dòng lỗi gần nhất, nhận %q (muốn %q)", got, want)
	}
}

func TestLevelErrorPreferredOverPlainText(t *testing.T) {
	r := reader()
	p := writeLog(t, "=== bật lúc 2026-10-22 10:00:00 ===\n"+
		"một dòng lỗi plain text\n"+
		`{"level":"error","msg":"có mức log rõ ràng"}`+"\n")
	got := r.LastError(p)
	if got != `{"level":"error","msg":"có mức log rõ ràng"}` {
		t.Fatalf("phải ưu tiên dòng có mức log=error, nhận %q", got)
	}
}
