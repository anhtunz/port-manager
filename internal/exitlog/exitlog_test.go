package exitlog

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s := New(t.TempDir())
	base := time.Date(2026, 10, 25, 9, 0, 0, 0, time.Local)
	n := 0
	s.now = func() time.Time { // mỗi lần gọi tiến 1 phút để phân biệt trước/sau
		n++
		return base.Add(time.Duration(n) * time.Minute)
	}
	return s
}

func TestFirstRunIsNone(t *testing.T) {
	s := newTestStore(t)
	info, err := s.BeginSession("0.7.0", false)
	if err != nil {
		t.Fatalf("BeginSession: %v", err)
	}
	if info.Kind != KindNone {
		t.Errorf("lần đầu phải none, được %+v", info)
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), SessionFile)); err != nil {
		t.Errorf("phải có file session sau BeginSession: %v", err)
	}
}

func TestCleanQuit(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.BeginSession("0.7.0", false); err != nil {
		t.Fatal(err)
	}
	if err := s.EndSession(KindQuit, "", ""); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), SessionFile)); !os.IsNotExist(err) {
		t.Errorf("session phải bị xoá sau EndSession, err=%v", err)
	}
	info, _ := s.BeginSession("0.7.0", false)
	if info.Kind != KindQuit {
		t.Errorf("sau thoát sạch phải quit, được %+v", info)
	}
	if info.PID != os.Getpid() {
		t.Errorf("pid phiên trước = %d, muốn %d", info.PID, os.Getpid())
	}
}

func TestAbruptDeath(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.BeginSession("0.7.0", true); err != nil {
		t.Fatal(err)
	}
	// Không EndSession = mô phỏng kill -9. Mở lại:
	info, _ := s.BeginSession("0.7.0", false)
	if info.Kind != KindAbrupt {
		t.Fatalf("phải abrupt, được %+v", info)
	}
	if !strings.Contains(info.Detail, "kill -9") {
		t.Errorf("detail phải gợi ý kill -9: %q", info.Detail)
	}
	if info.PID != os.Getpid() {
		t.Errorf("pid = %d", info.PID)
	}
	// Kết luận abrupt phải được lưu thành lastexit để Last() đọc được.
	if got := s.Last(); got.Kind != KindAbrupt {
		t.Errorf("Last() sau abrupt = %+v", got)
	}
	// Lần mở KẾ TIẾP (phiên hiện tại thoát sạch) không được báo abrupt nữa.
	_ = s.EndSession(KindQuit, "", "")
	info, _ = s.BeginSession("0.7.0", false)
	if info.Kind != KindQuit {
		t.Errorf("sau khi thoát sạch phải quit, được %+v", info)
	}
}

func TestSignal(t *testing.T) {
	s := newTestStore(t)
	_, _ = s.BeginSession("0.7.0", false)
	if err := s.RecordSignal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	info, _ := s.BeginSession("0.7.0", false)
	if info.Kind != KindSignal || info.Signal != "SIGTERM" {
		t.Errorf("phải signal SIGTERM, được %+v", info)
	}
	if !strings.Contains(info.Detail, "launchctl bootout") {
		t.Errorf("detail SIGTERM thiếu gợi ý: %q", info.Detail)
	}
}

// Chết giữa EndSession: đã ghi lastexit nhưng chưa xoá session → phải tin
// lastexit (mới hơn), không kết luận abrupt.
func TestLastExitNewerThanSessionWins(t *testing.T) {
	s := newTestStore(t)
	_, _ = s.BeginSession("0.7.0", false)
	rec := exitRecord{At: s.now().Format(time.RFC3339), Kind: KindSignal, Signal: "SIGHUP", PID: os.Getpid()}
	if err := s.writeJSON(s.lastExitPath(), rec); err != nil {
		t.Fatal(err)
	}
	info, _ := s.BeginSession("0.7.0", false)
	if info.Kind != KindSignal || info.Signal != "SIGHUP" {
		t.Errorf("phải tin lastexit mới hơn, được %+v", info)
	}
}

func TestSignalName(t *testing.T) {
	cases := map[os.Signal]string{
		syscall.SIGTERM: "SIGTERM",
		syscall.SIGINT:  "SIGINT",
		syscall.SIGHUP:  "SIGHUP",
		syscall.SIGUSR1: "USER DEFINED SIGNAL 1",
	}
	for sig, want := range cases {
		if got := SignalName(sig); got != want {
			t.Errorf("SignalName(%v) = %q, muốn %q", sig, got, want)
		}
	}
}

func TestCorruptFilesAreIgnored(t *testing.T) {
	s := newTestStore(t)
	_ = os.MkdirAll(s.Dir(), 0o755)
	_ = os.WriteFile(filepath.Join(s.Dir(), SessionFile), []byte("{không phải json"), 0o644)
	_ = os.WriteFile(filepath.Join(s.Dir(), LastExitFile), []byte("x"), 0o644)
	info, err := s.BeginSession("0.7.0", false)
	if err != nil {
		t.Fatalf("file hỏng không được làm BeginSession lỗi: %v", err)
	}
	if info.Kind != KindNone {
		t.Errorf("file hỏng → none, được %+v", info)
	}
}
