package proc

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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
	mgr, err := NewManager(filepath.Join(dir, "logs"), filepath.Join(dir, "state"))
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
	mgr, err := NewManager(filepath.Join(dir, "logs"), filepath.Join(dir, "state"))
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

// TestRestartLifecycle: bật → restart → service vẫn chạy với process mới.
// Đây là đường "tắt rồi bật lại" mà user cần khi service fail lúc khởi động.
func TestRestartLifecycle(t *testing.T) {
	const testPort = 5178
	dir := t.TempDir()
	mgr, err := NewManager(filepath.Join(dir, "logs"), filepath.Join(dir, "state"))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	svc := config.Service{
		ID:    "restart-svc",
		Name:  "restart",
		Ports: []int{testPort},
		Dir:   dir,
		Start: "python3 -m http.server " + strconv.Itoa(testPort) + " --bind 127.0.0.1",
	}
	t.Cleanup(func() {
		if mgr.IsManaged("restart-svc") {
			_ = mgr.Stop(svc, nil, dir)
		}
	})
	if err := mgr.Start(svc, dir); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !waitPort(testPort, 5*time.Second) {
		t.Fatal("port 5178 không mở sau 5s")
	}
	before := mgr.ManagedInfos()["restart-svc"]
	if !before.Alive || before.PID == 0 {
		t.Fatalf("phải biết pid của service do app quản lý: %+v", before)
	}

	if err := mgr.Restart(svc, listenersOf(t, testPort), dir); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if !waitPort(testPort, 5*time.Second) {
		t.Fatal("port 5178 không mở lại sau Restart")
	}
	after := mgr.ManagedInfos()["restart-svc"]
	if !after.Alive {
		t.Fatal("service phải còn sống sau Restart")
	}
	if after.PID == before.PID {
		t.Fatalf("Restart phải tạo process mới (pid vẫn %d)", after.PID)
	}
}

// TestStartRefusesWhenAlreadyRunning: bấm "Bật" lần hai khi service đang chạy
// phải báo lỗi rõ (kèm pid), không được spawn thêm bản thứ hai.
func TestStartRefusesWhenAlreadyRunning(t *testing.T) {
	dir := t.TempDir()
	mgr, err := NewManager(filepath.Join(dir, "logs"), filepath.Join(dir, "state"))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	svc := config.Service{ID: "dup-svc", Name: "dup", Ports: []int{5178}, Dir: dir, Start: "sleep 30"}
	t.Cleanup(func() {
		if mgr.IsManaged("dup-svc") {
			_ = mgr.Stop(svc, nil, dir)
		}
	})
	if err := mgr.Start(svc, dir); err != nil {
		t.Fatalf("Start: %v", err)
	}
	err = mgr.Start(svc, dir)
	if err == nil {
		t.Fatal("Start lần hai phải bị từ chối")
	}
	if !strings.Contains(err.Error(), "đang chạy") {
		t.Fatalf("lỗi phải nói rõ service đang chạy, nhận: %v", err)
	}
}

// TestStopViaStateFileAfterAppRestart: app đóng rồi mở lại (hoặc CLI chạy ở
// process riêng) — process group lấy từ state file trên đĩa. Đây là đường duy
// nhất tắt được service kẹt lúc khởi động (chưa lên cổng → không có listener).
func TestStopViaStateFileAfterAppRestart(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	mgr, err := NewManager(filepath.Join(dir, "logs"), stateDir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	// Service kẹt: process sống nhưng không lên cổng nào.
	svc := config.Service{ID: "stuck-svc", Name: "stuck", Ports: []int{5178}, Dir: dir, Start: "sleep 300"}
	if err := mgr.Start(svc, dir); err != nil {
		t.Fatalf("Start: %v", err)
	}
	pid := mgr.ManagedInfos()["stuck-svc"].PID
	if pid == 0 {
		t.Fatal("phải biết pid sau Start")
	}

	// Manager mới = app vừa mở lại (map trong RAM rỗng, chỉ còn state file).
	mgr2, err := NewManager(filepath.Join(dir, "logs"), stateDir)
	if err != nil {
		t.Fatalf("NewManager (lần 2): %v", err)
	}
	mi := mgr2.ManagedInfos()["stuck-svc"]
	if !mi.Alive || mi.PID != pid {
		t.Fatalf("manager mới phải nhận ra service còn sống qua state file: %+v", mi)
	}
	// Không có listener nào (service chưa lên cổng) → vẫn phải tắt được.
	if err := mgr2.Stop(svc, nil, dir); err != nil {
		t.Fatalf("Stop qua state file: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return // đã chết — ok
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("process %d vẫn còn sống sau Stop", pid)
}

// TestStartRefusesServiceFromPreviousSession: app mở lại, service do app trước
// đó bật vẫn còn sống → Start phải từ chối (không spawn thêm bản thứ hai,
// cổng sẽ đụng).
func TestStartRefusesServiceFromPreviousSession(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	mgr, err := NewManager(filepath.Join(dir, "logs"), stateDir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	svc := config.Service{ID: "prev-svc", Name: "prev", Ports: []int{5178}, Dir: dir, Start: "sleep 300"}
	if err := mgr.Start(svc, dir); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		if mgr.IsManaged("prev-svc") {
			_ = mgr.Stop(svc, nil, dir)
		}
	})

	mgr2, err := NewManager(filepath.Join(dir, "logs"), stateDir)
	if err != nil {
		t.Fatalf("NewManager (lần 2): %v", err)
	}
	err = mgr2.Start(svc, dir)
	if err == nil {
		t.Fatal("Start phải bị từ chối khi service của phiên trước còn sống")
	}
	if !strings.Contains(err.Error(), "Restart") {
		t.Fatalf("lỗi phải hướng user tới Restart, nhận: %v", err)
	}
}

// TestLogAppendAcrossRestarts: log của lần chạy trước phải còn lại sau khi
// bật lại — đó là thứ cần nhìn khi service fail lúc khởi động.
func TestLogAppendAcrossRestarts(t *testing.T) {
	dir := t.TempDir()
	mgr, err := NewManager(filepath.Join(dir, "logs"), filepath.Join(dir, "state"))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	svc := config.Service{ID: "log-svc", Name: "log", Ports: []int{5178}, Dir: dir, Start: "echo LAN1_DAU; sleep 10"}
	if err := mgr.Start(svc, dir); err != nil {
		t.Fatalf("Start: %v", err)
	}
	_ = mgr.Stop(svc, nil, dir)
	svc2 := config.Service{ID: "log-svc", Name: "log", Ports: []int{5178}, Dir: dir, Start: "echo LAN2_SAU; sleep 10"}
	if err := mgr.Start(svc2, dir); err != nil {
		t.Fatalf("Start lần 2: %v", err)
	}
	t.Cleanup(func() {
		if mgr.IsManaged("log-svc") {
			_ = mgr.Stop(svc2, nil, dir)
		}
	})
	log := mgr.TailLog("log-svc", 50)
	if !strings.Contains(log, "LAN1_DAU") {
		t.Fatalf("log lần chạy trước bị mất khi bật lại:\n%s", log)
	}
	if !strings.Contains(log, "LAN2_SAU") {
		t.Fatalf("log lần chạy sau thiếu:\n%s", log)
	}
}

// listenersOf lấy listener hiện tại trên 1 port (dùng cho Restart).
func listenersOf(t *testing.T, port int) []scan.Listener {
	t.Helper()
	ls, err := scan.Listeners()
	if err != nil {
		t.Fatalf("Listeners: %v", err)
	}
	var out []scan.Listener
	for _, l := range ls {
		if l.Port == port {
			out = append(out, l)
		}
	}
	return out
}

// TestStateAliveDetectsReusedPid: pid bị hệ điều hành tái sử dụng cho process
// khác → state file cũ không được dùng để kill (tránh giết nhầm process của
// người khác). Dấu hiệu: pgid khác, hoặc thời điểm bắt đầu process khác.
func TestStateAliveDetectsReusedPid(t *testing.T) {
	now := time.Now()
	groups := map[int]int{100: 100, 200: 250}
	psInfo := map[int]scan.ProcInfo{
		100: {Command: "sleep 300", StartedAt: now.Add(-time.Minute)},
		200: {Command: "vim", StartedAt: now.Add(-time.Minute)},
	}
	// Đúng process ta spawn
	if !stateAlive(procState{PGID: 100, Started: now.Add(-time.Minute).UnixMilli(), ProcStart: now.Add(-time.Minute).UnixMilli()}, groups, psInfo) {
		t.Fatal("process đúng phải được coi là còn sống")
	}
	// Pid bị tái sử dụng: pgid đã khác
	if stateAlive(procState{PGID: 200, Started: now.Add(-time.Minute).UnixMilli(), ProcStart: now.Add(-time.Minute).UnixMilli()}, groups, psInfo) {
		t.Fatal("pid thuộc process group khác phải bị loại")
	}
	// Pid bị tái sử dụng: cùng pgid nhưng thời điểm bắt đầu khác
	if stateAlive(procState{PGID: 100, Started: now.Add(-time.Minute).UnixMilli(), ProcStart: now.Add(-10 * time.Minute).UnixMilli()}, groups, psInfo) {
		t.Fatal("process khác thời điểm bắt đầu phải bị loại")
	}
	// Pid không còn tồn tại
	if stateAlive(procState{PGID: 999, Started: now.UnixMilli()}, groups, psInfo) {
		t.Fatal("pid không tồn tại phải bị loại")
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

func TestSmartPathNonEmpty(t *testing.T) {
	if SmartPath() == "" {
		t.Fatal("SmartPath không được rỗng")
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("không phải môi trường darwin/unix")
	}
}

func TestMergePathLists(t *testing.T) {
	got := mergePathLists("/a:/b", "/b:/c:", "", "/d:/a")
	want := []string{"/a", "/b", "/c", "/d"}
	if strings.Join(got, ":") != strings.Join(want, ":") {
		t.Fatalf("mergePathLists = %v, muốn %v", got, want)
	}
}

// TestStartKeepsStdinOpen: hồi quy cho lỗi "FE Umi không khởi động được".
// Lệnh `cat` đọc stdin tới khi EOF — nếu stdin bị đóng (để nil → /dev/null,
// hoặc đóng pipe sớm) thì cat thoát ngay. Với fix (pipe giữ mở) process phải
// còn sống sau cửa sổ start 1.5s và sau thêm 1s nữa.
func TestStartKeepsStdinOpen(t *testing.T) {
	dir := t.TempDir()
	mgr, err := NewManager(filepath.Join(dir, "logs"), filepath.Join(dir, "state"))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	svc := config.Service{ID: "stdin-svc", Name: "stdin", Ports: []int{}, Dir: dir, Start: "cat"}
	t.Cleanup(func() {
		if mgr.IsManaged("stdin-svc") {
			_ = mgr.Stop(svc, nil, dir)
		}
	})
	if err := mgr.Start(svc, dir); err != nil {
		t.Fatalf("Start phải thành công — stdin bị EOF khiến process chết sớm: %v", err)
	}
	time.Sleep(1 * time.Second)
	if !mgr.IsManaged("stdin-svc") {
		t.Fatal("process đã chết — stdin nhận EOF (sẽ lặp lại lỗi tailwind của umi)")
	}
}

// TestStartChildHoldsStdinWriteEnd: cây process con phải giữ được đầu ghi của
// pipe stdin (fd 3) để stdin không bao giờ EOF, kể cả khi app/CLI đã thoát.
// Kiểm tra bằng cách cho child tự dò fd 3.
func TestStartChildHoldsStdinWriteEnd(t *testing.T) {
	dir := t.TempDir()
	mgr, err := NewManager(filepath.Join(dir, "logs"), filepath.Join(dir, "state"))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	svc := config.Service{
		ID:    "fd3-svc",
		Name:  "fd3",
		Ports: []int{},
		Dir:   dir,
		Start: "if [ -e /dev/fd/3 ]; then echo HAS_FD3; else echo NO_FD3; fi",
	}
	// Lệnh này thoát ngay → Start trả lỗi, nhưng ta chỉ cần đọc log.
	_ = mgr.Start(svc, dir)
	log := mgr.TailLog("fd3-svc", 20)
	if !strings.Contains(log, "HAS_FD3") {
		t.Fatalf("child không giữ fd 3 (đầu ghi stdin) — stdin sẽ EOF khi app thoát. Log:\n%s", log)
	}
}
