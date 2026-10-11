package proc

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"smatec/port-manager/internal/config"
	"smatec/port-manager/internal/scan"
)

// TestStartStopLifecycle: bật một server thật trên port 5178 (dải fe-* dành cho
// dev thử nghiệm), verify cổng mở → tắt → verify cổng giải phóng, không để lại
// process. Port 5178 nằm trong dải reserved nên không vi phạm quy hoạch port.
func TestStartStopLifecycle(t *testing.T) {
	const testPort = 5178
	dir := t.TempDir()
	mgr, err := NewManager(filepath.Join(dir, "logs"))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	svc := config.Service{
		ID:    "test-svc",
		Name:  "test",
		Ports: []int{testPort},
		Dir:   dir,
		Start: "python3 -m http.server " + strconv.Itoa(testPort) + " --bind 127.0.0.1",
	}
	// Cleanup chạy cả khi t.Fatal giữa chừng — tránh sót process chiếm port
	t.Cleanup(func() {
		if mgr.IsManaged("test-svc") {
			_ = mgr.Stop(svc, nil, dir)
		}
	})
	if err := mgr.Start(svc, dir); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !mgr.IsManaged("test-svc") {
		t.Fatal("service phải được đánh dấu là do app quản lý sau khi Start")
	}
	if !waitPort(testPort, 5*time.Second) {
		t.Fatal("port 5178 không mở sau 5s")
	}
	// python http.server ghi startup line vào stdout block-buffer khi redirect
	// ra file → không assert nội dung, chỉ assert file log được tạo
	if _, err := os.Stat(mgr.logPath("test-svc")); err != nil {
		t.Fatalf("file log chưa được tạo: %v", err)
	}

	ls, err := scan.Listeners()
	if err != nil {
		t.Fatalf("Listeners: %v", err)
	}
	var found []scan.Listener
	for _, l := range ls {
		if l.Port == testPort {
			found = append(found, l)
		}
	}
	if len(found) == 0 {
		t.Fatal("scan không thấy listener trên 5178")
	}

	if err := mgr.Stop(svc, found, dir); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if mgr.IsManaged("test-svc") {
		t.Fatal("service vẫn bị đánh dấu managed sau Stop")
	}
	if waitPort(testPort, 2*time.Second) {
		var leftover []int
		ls2, _ := scan.Listeners()
		for _, l := range ls2 {
			if l.Port == testPort {
				leftover = append(leftover, l.PID)
			}
		}
		t.Fatalf("port 5178 vẫn mở sau Stop — process còn sót: %v", leftover)
	}
}

func TestStartFailFast(t *testing.T) {
	dir := t.TempDir()
	mgr, err := NewManager(filepath.Join(dir, "logs"))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	svc := config.Service{ID: "bad", Ports: []int{5178}, Dir: dir, Start: "exit 3"}
	err = mgr.Start(svc, dir)
	if err == nil {
		t.Fatal("Start phải lỗi khi command thoát ngay")
	}
	if mgr.IsManaged("bad") {
		t.Fatal("service chết phải không còn managed")
	}
}

func waitPort(port int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
		if err == nil {
			c.Close()
			return true
		}
		time.Sleep(150 * time.Millisecond)
	}
	return false
}

func TestShellPathNonEmpty(t *testing.T) {
	dir := t.TempDir()
	mgr, _ := NewManager(filepath.Join(dir, "logs"))
	if mgr.ShellPath() == "" {
		t.Fatal("ShellPath không được rỗng")
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("không phải môi trường darwin/unix")
	}
}
