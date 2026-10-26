package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// store mới trong thư mục tạm — KHÔNG bao giờ đụng ~/.port-manager thật.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	return New(t.TempDir())
}

// at cố định để test so sánh chắc chắn.
func at(sec int) time.Time {
	return time.Date(2026, 10, 22, 10, 0, sec, 0, time.Local)
}

// (1) Ghi 3 sự kiện → đọc lại đúng thứ tự & kind.
func TestRecordAndList(t *testing.T) {
	s := newTestStore(t)

	if err := s.Record("portal-fe", KindStart, "bật lúc 10:00"); err != nil {
		t.Fatalf("Record start: %v", err)
	}
	if err := s.Record("portal-fe", KindStop, "tắt có chủ ý"); err != nil {
		t.Fatalf("Record stop: %v", err)
	}
	if err := s.Record("portal-fe", KindCrash, "thoát với code 1"); err != nil {
		t.Fatalf("Record crash: %v", err)
	}

	evs := s.List("portal-fe", 0)
	if len(evs) != 3 {
		t.Fatalf("cần 3 sự kiện, nhận %d: %+v", len(evs), evs)
	}
	// Mới nhất trước.
	want := []string{KindCrash, KindStop, KindStart}
	for i, k := range want {
		if evs[i].Kind != k {
			t.Fatalf("sự kiện %d: cần %q, nhận %q", i, k, evs[i].Kind)
		}
	}
	if evs[0].Detail != "thoát với code 1" {
		t.Fatalf("detail sai: %q", evs[0].Detail)
	}
	if evs[2].At == "" {
		t.Fatal("thiếu mốc thời gian")
	}
	if _, err := time.Parse(time.RFC3339, evs[2].At); err != nil {
		t.Fatalf("mốc thời gian không phải RFC3339: %q (%v)", evs[2].At, err)
	}
}

// File lịch sử phải là JSONL: 1 dòng = 1 JSON object.
func TestFileIsJSONL(t *testing.T) {
	s := newTestStore(t)
	if err := s.RecordAt("wm-be", KindStart, "ok", at(1)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAt("wm-be", KindRestart, "lại lần nữa", at(2)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(s.Path("wm-be"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("cần 2 dòng, nhận %d: %q", len(lines), string(data))
	}
	for _, l := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("dòng không phải JSON: %q (%v)", l, err)
		}
	}
}

// Tên field JSON phải đúng hợp đồng: at, kind, detail.
func TestJSONFieldNames(t *testing.T) {
	b, err := json.Marshal(Event{At: "2026-10-22T10:00:00+07:00", Kind: KindStart, Detail: "chi tiết"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"at":"2026-10-22T10:00:00+07:00","kind":"start","detail":"chi tiết"}`
	if string(b) != want {
		t.Fatalf("JSON sai hợp đồng:\n nhận %s\n cần  %s", b, want)
	}
}

// (2) List với limit → N sự kiện mới nhất.
func TestListLimit(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 10; i++ {
		if err := s.RecordAt("svc", KindStart, fmt.Sprintf("lần %d", i), at(i)); err != nil {
			t.Fatal(err)
		}
	}
	evs := s.List("svc", 3)
	if len(evs) != 3 {
		t.Fatalf("cần 3 sự kiện, nhận %d", len(evs))
	}
	// Mới nhất trước → lần 9, 8, 7.
	for i, wantDetail := range []string{"lần 9", "lần 8", "lần 7"} {
		if evs[i].Detail != wantDetail {
			t.Fatalf("sự kiện %d: cần %q, nhận %q", i, wantDetail, evs[i].Detail)
		}
	}
	// limit <= 0 → trả tất cả.
	if all := s.List("svc", 0); len(all) != 10 {
		t.Fatalf("limit=0 phải trả tất cả, nhận %d", len(all))
	}
}

// (3) File > 256KB → rotate, giữ 200 dòng cuối.
func TestRotateKeepsLastLines(t *testing.T) {
	s := newTestStore(t)

	// Ghi thẳng vào file cho nhanh (mỗi Record đều fsync — 300 lần thì chậm).
	f, err := os.Create(s.Path("svc"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 300; i++ {
		line, _ := json.Marshal(Event{
			At:     at(i % 60).Format(time.RFC3339),
			Kind:   KindStart,
			Detail: fmt.Sprintf("dòng %03d %s", i, strings.Repeat("x", 900)),
		})
		if _, err := f.Write(append(line, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()

	st, err := os.Stat(s.Path("svc"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() <= MaxSize {
		t.Fatalf("file test phải vượt ngưỡng %d, mới %d", MaxSize, st.Size())
	}

	// Một lần ghi nữa → phát hiện vượt ngưỡng → rotate.
	if err := s.Record("svc", KindCrash, "sự kiện mới nhất"); err != nil {
		t.Fatal(err)
	}

	evs := s.List("svc", 0)
	if len(evs) != KeepLines {
		t.Fatalf("sau rotate cần %d dòng, nhận %d", KeepLines, len(evs))
	}
	// Sự kiện mới nhất phải còn, và ở đầu danh sách (mới nhất trước).
	if evs[0].Kind != KindCrash || evs[0].Detail != "sự kiện mới nhất" {
		t.Fatalf("sự kiện mới nhất bị mất: %+v", evs[0])
	}
	// Dòng cũ đã bị cắt bỏ (dòng 0 nằm ngoài 200 dòng cuối).
	for _, e := range evs {
		if e.Detail == "dòng 000 "+strings.Repeat("x", 900) {
			t.Fatal("dòng quá cũ vẫn còn sau rotate")
		}
	}
	st, err = os.Stat(s.Path("svc"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() > MaxSize {
		t.Fatalf("file sau rotate vẫn vượt ngưỡng: %d", st.Size())
	}
	// Không còn file tạm sót lại trong thư mục.
	entries, err := os.ReadDir(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("còn file tạm sót: %s", e.Name())
		}
	}
}

// (4) File/dòng hỏng → bỏ qua, không panic.
func TestCorruptFileAndLines(t *testing.T) {
	s := newTestStore(t)

	// Dòng hỏng giữa file hợp lệ.
	content := `{"at":"2026-10-22T10:00:00+07:00","kind":"start","detail":"ok"}` + "\n" +
		`{"at":"KHÔNG PHẢI JSON` + "\n" +
		`{"at":"2026-10-22T10:00:03+07:00","kind":"stop","detail":"dừng"}` + "\n"
	if err := os.WriteFile(s.Path("svc"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	evs := s.List("svc", 0)
	if len(evs) != 2 {
		t.Fatalf("cần 2 sự kiện (bỏ dòng hỏng), nhận %d: %+v", len(evs), evs)
	}
	if evs[0].Kind != KindStop {
		t.Fatalf("sai thứ tự: %+v", evs)
	}

	// File toàn rác → không panic, trả về rỗng.
	if err := os.WriteFile(s.Path("rac"), []byte("không phải json\n{\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	evs = s.List("rac", 0)
	if evs == nil {
		t.Fatal("phải trả slice rỗng, không phải nil")
	}
	if len(evs) != 0 {
		t.Fatalf("cần 0 sự kiện, nhận %d", len(evs))
	}
}

// (5) Chưa có lịch sử → [] (không phải null).
func TestEmptyHistory(t *testing.T) {
	s := newTestStore(t)
	evs := s.List("khong-co", 10)
	if evs == nil {
		t.Fatal("phải trả slice rỗng, không phải nil")
	}
	if len(evs) != 0 {
		t.Fatalf("cần 0 sự kiện, nhận %d", len(evs))
	}
	// JSON ra cũng phải là [] chứ không phải null (frontend dùng trực tiếp).
	b, err := json.Marshal(evs)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "[]" {
		t.Fatalf("JSON phải là [], nhận %s", b)
	}
}

// Đọc tất cả service.
func TestListAll(t *testing.T) {
	s := newTestStore(t)
	if err := s.Record("a", KindStart, "x"); err != nil {
		t.Fatal(err)
	}
	if err := s.Record("b", KindStop, "y"); err != nil {
		t.Fatal(err)
	}
	if err := s.Record("b", KindStop, "z"); err != nil {
		t.Fatal(err)
	}
	all := s.ListAll(0)
	if len(all) != 2 {
		t.Fatalf("cần 2 service, nhận %d: %+v", len(all), all)
	}
	if len(all["a"]) != 1 || len(all["b"]) != 2 {
		t.Fatalf("sai số sự kiện: %+v", all)
	}
	// Mỗi service: mới nhất trước.
	if all["b"][0].Detail != "z" {
		t.Fatalf("sai thứ tự trong service b: %+v", all["b"])
	}
	// ListAll có giới hạn → áp dụng cho từng service.
	all = s.ListAll(1)
	if len(all["b"]) != 1 {
		t.Fatalf("limit=1 phải còn 1 sự kiện, nhận %d", len(all["b"]))
	}
	// Gộp phẳng.
	flat := s.ListAllEvents(0)
	if len(flat) != 3 {
		t.Fatalf("cần 3 sự kiện gộp, nhận %d", len(flat))
	}
}

// Ghi song song (app + CLI ghi cùng file) → không dòng nào bị dính vào nhau.
func TestConcurrentRecord(t *testing.T) {
	s := newTestStore(t)
	const goroutines, each = 4, 25

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				if err := s.Record("svc", KindStart, fmt.Sprintf("g%d-i%d", g, i)); err != nil {
					t.Errorf("Record: %v", err)
					return
				}
			}
		}(g)
	}
	wg.Wait()

	data, err := os.ReadFile(s.Path("svc"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != goroutines*each {
		t.Fatalf("cần %d dòng, nhận %d", goroutines*each, len(lines))
	}
	for i, l := range lines {
		var ev Event
		if err := json.Unmarshal([]byte(l), &ev); err != nil {
			t.Fatalf("dòng %d hỏng (ghi song song lẫn nhau): %q (%v)", i, l, err)
		}
	}
}

// Ghi vào thư mục không ghi được → trả lỗi, không panic (app không được sập).
func TestRecordUnwritableDir(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "khong-ton-tai")) // MkdirAll sẽ tạo được
	if err := s.Record("svc", KindStart, "x"); err != nil {
		t.Fatalf("ghi vào thư mục mới phải được: %v", err)
	}

	// Thư mục là MỘT FILE → không tạo được thư mục lịch sử → lỗi, không panic.
	base := t.TempDir()
	blocker := filepath.Join(base, "blocked")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s2 := New(blocker)
	if err := s2.Record("svc", KindStart, "x"); err == nil {
		t.Fatal("phải trả lỗi khi không ghi được file lịch sử")
	}
	// RecordOK không panic khi lỗi.
	s2.RecordOK("svc", KindStart, "x")
}

// id / kind không hợp lệ → lỗi rõ ràng, không ghi file rác.
func TestInvalidInput(t *testing.T) {
	s := newTestStore(t)
	if err := s.Record("", KindStart, "x"); err == nil {
		t.Fatal("id rỗng phải lỗi")
	}
	if err := s.Record("../thoat", KindStart, "x"); err == nil {
		t.Fatal("id chứa đường dẫn phải lỗi")
	}
	if err := s.Record("svc", "exit", "x"); err == nil {
		t.Fatal("kind ngoài enum phải lỗi")
	}
	if err := s.Record("svc", "", "x"); err == nil {
		t.Fatal("kind rỗng phải lỗi")
	}
	entries, _ := os.ReadDir(s.Dir())
	if len(entries) != 0 {
		t.Fatalf("không được tạo file rác: %+v", entries)
	}
}

// id chứa đường dẫn → không đọc ra ngoài thư mục lịch sử.
func TestListRejectsPathTraversal(t *testing.T) {
	s := newTestStore(t)
	outside := filepath.Join(t.TempDir(), "khac"+FileSuffix)
	if err := os.WriteFile(outside, []byte(`{"at":"2026-10-22T10:00:00+07:00","kind":"start","detail":"x"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	evs := s.List("../"+filepath.Base(filepath.Dir(outside))+"/khac", 0)
	if len(evs) != 0 {
		t.Fatalf("không được đọc file ngoài thư mục lịch sử: %+v", evs)
	}
}

// Enum kind đúng hợp đồng.
func TestKindsContract(t *testing.T) {
	want := []string{"start", "stop", "restart", "crash", "stuck", "fail", "edit"}
	got := Kinds()
	if len(got) != len(want) {
		t.Fatalf("cần %d kind, nhận %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("kind %d: cần %q, nhận %q", i, want[i], got[i])
		}
		if !IsValidKind(want[i]) {
			t.Fatalf("kind %q phải hợp lệ", want[i])
		}
	}
	if IsValidKind("exit") {
		t.Fatal("\"exit\" không được nằm trong enum")
	}
}

// Mốc thời gian tăng dần → đọc ra mới nhất trước; nếu ghi lộn thứ tự thời gian
// vẫn sắp xếp theo mốc thời gian.
func TestSortByTimestamp(t *testing.T) {
	s := newTestStore(t)
	if err := s.RecordAt("svc", KindStart, "cũ", at(10)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAt("svc", KindStop, "mới", at(30)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAt("svc", KindRestart, "giữa", at(20)); err != nil {
		t.Fatal(err)
	}
	evs := s.List("svc", 0)
	if len(evs) != 3 {
		t.Fatalf("cần 3 sự kiện, nhận %d", len(evs))
	}
	want := []string{"mới", "giữa", "cũ"}
	for i, w := range want {
		if evs[i].Detail != w {
			t.Fatalf("sự kiện %d: cần %q, nhận %q", i, w, evs[i].Detail)
		}
	}
}

// Đường dẫn file theo id, và thư mục mặc định nằm trong HOME.
func TestPaths(t *testing.T) {
	s := New("/tmp/ví-dụ")
	if got := s.Path("abc"); got != filepath.Join("/tmp/ví-dụ", "abc"+FileSuffix) {
		t.Fatalf("sai đường dẫn: %s", got)
	}
	def := DefaultDir()
	if def == "" || !strings.Contains(def, filepath.Join(".port-manager", "history")) {
		t.Fatalf("thư mục mặc định sai: %q", def)
	}
}

// Store dùng nguồn thời gian tuỳ biến (test được, app thì dùng time.Now).
func TestCustomNow(t *testing.T) {
	fixed := at(42)
	s := NewWithOptions(t.TempDir(), Options{Now: func() time.Time { return fixed }})
	if err := s.Record("svc", KindStart, "x"); err != nil {
		t.Fatal(err)
	}
	evs := s.List("svc", 0)
	if len(evs) != 1 {
		t.Fatalf("cần 1 sự kiện, nhận %d", len(evs))
	}
	if evs[0].At != fixed.Format(time.RFC3339) {
		t.Fatalf("sai mốc thời gian: %q", evs[0].At)
	}
}
