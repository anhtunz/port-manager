package proc

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"smatec/port-manager/internal/config"
	"smatec/port-manager/internal/scan"
)

// Managed là một service do app tự bật — giữ process group để tắt được cả cây
// make/npm/go run (killing từng pid con không sạch vì go run sinh process con).
type Managed struct {
	ID      string
	PGID    int // = pid của process leader
	LogFile *os.File
	Started time.Time

	done    chan struct{}
	exitErr error
}

type Manager struct {
	mu     sync.Mutex
	procs  map[string]*Managed
	logDir string

	shellPathOnce sync.Once
	shellPathVal  string
}

func NewManager(logDir string) (*Manager, error) {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, fmt.Errorf("tạo thư mục log: %w", err)
	}
	return &Manager{procs: map[string]*Managed{}, logDir: logDir}, nil
}

// ShellPath lấy PATH từ login shell. App GUI launch từ Finder chỉ nhận PATH
// tối thiểu (/usr/bin:/bin:...) — không thấy go/make/npm/docker nằm ở
// /usr/local/bin, /opt/homebrew/bin, ~/go/bin... nên phải bơm PATH của shell.
func (m *Manager) ShellPath() string {
	m.shellPathOnce.Do(func() {
		for _, sh := range []string{"/bin/zsh", "/bin/bash"} {
			out, err := exec.Command(sh, "-lc", "echo $PATH").Output()
			if err == nil {
				if p := strings.TrimSpace(string(out)); p != "" {
					m.shellPathVal = p
					return
				}
			}
		}
		m.shellPathVal = os.Getenv("PATH")
	})
	return m.shellPathVal
}

func (m *Manager) env() []string {
	return append(os.Environ(), "PATH="+m.ShellPath())
}

func (m *Manager) IsManaged(id string) bool {
	m.mu.Lock()
	mg, ok := m.procs[id]
	m.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case <-mg.done:
		return false
	default:
		return true
	}
}

func (m *Manager) logPath(id string) string {
	return filepath.Join(m.logDir, id+".log")
}

// Start bật service. Compose → chạy sync (up -d tự trả khi container lên).
// Service thường → spawn /bin/sh -c <start> trong process group riêng,
// stdout/stderr ghi vào logs/<id>.log. Nếu process chết ngay trong 1.5s thì
// coi như lỗi bật, trả kèm log để UI hiện lý do thật.
func (m *Manager) Start(s config.Service, workDir string) error {
	if s.IsCompose() {
		cmdText := fmt.Sprintf("docker compose -f %s up -d %s", shellQuote(s.ComposeFile), shellQuote(s.ComposeService))
		out, err := m.runSync(cmdText, workDir)
		if err != nil {
			return fmt.Errorf("docker compose up: %v\n%s", err, out)
		}
		return nil
	}
	if m.IsManaged(s.ID) {
		return fmt.Errorf("service %s đang chạy do app quản lý", s.ID)
	}
	if s.Start == "" {
		return fmt.Errorf("service %s chưa cấu hình lệnh start", s.ID)
	}

	lf, err := os.Create(m.logPath(s.ID))
	if err != nil {
		return fmt.Errorf("mở log: %w", err)
	}
	cmd := exec.Command("/bin/sh", "-c", s.Start)
	cmd.Dir = workDir
	cmd.Env = m.env()
	// Setpgid: group riêng cho cả cây process — kill cả nhóm khi tắt
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = lf
	cmd.Stderr = lf
	if err := cmd.Start(); err != nil {
		lf.Close()
		return fmt.Errorf("spawn: %w", err)
	}
	mg := &Managed{ID: s.ID, PGID: cmd.Process.Pid, LogFile: lf, Started: time.Now(), done: make(chan struct{})}
	m.mu.Lock()
	m.procs[s.ID] = mg
	m.mu.Unlock()
	go func() {
		mg.exitErr = cmd.Wait()
		close(mg.done)
	}()

	select {
	case <-mg.done:
		return fmt.Errorf("service thoát ngay khi bật (%v). Log:\n%s", mg.exitErr, m.TailLog(s.ID, 15))
	case <-time.After(1500 * time.Millisecond):
		return nil
	}
}

// Stop tắt service. listeners = pid đang lắng nghe cổng của service (từ scan).
// Service do app bật → kill cả process group; chạy ngoài app (user bật từ
// terminal) → chỉ kill đúng pid listener, không đụng cây process khác.
func (m *Manager) Stop(s config.Service, listeners []scan.Listener, workDir string) error {
	if s.IsCompose() {
		cmdText := fmt.Sprintf("docker compose -f %s stop %s", shellQuote(s.ComposeFile), shellQuote(s.ComposeService))
		out, err := m.runSync(cmdText, workDir)
		if err != nil {
			return fmt.Errorf("docker compose stop: %v\n%s", err, out)
		}
		return nil
	}

	m.mu.Lock()
	mg := m.procs[s.ID]
	m.mu.Unlock()
	if mg != nil {
		select {
		case <-mg.done: // đã chết — chỉ dọn trạng thái
		default:
			_ = syscall.Kill(-mg.PGID, syscall.SIGTERM)
			if !waitGroupGone(mg.PGID, 3*time.Second) {
				_ = syscall.Kill(-mg.PGID, syscall.SIGKILL)
			}
			select {
			case <-mg.done:
			case <-time.After(2 * time.Second):
			}
		}
		m.mu.Lock()
		delete(m.procs, s.ID)
		m.mu.Unlock()
		mg.LogFile.Close()
		return nil
	}

	pids := map[int]bool{}
	for _, l := range listeners {
		pids[l.PID] = true
	}
	if len(pids) == 0 {
		return fmt.Errorf("không tìm thấy process nào đang lắng nghe")
	}
	for pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	if !waitPIDsGone(pids, 2*time.Second) {
		for pid := range pids {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	return nil
}

// KillPID dùng cho mục "Cổng lạ": kill đúng 1 pid do user chỉ định.
func (m *Manager) KillPID(pid int) error {
	_ = syscall.Kill(pid, syscall.SIGTERM)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		return fmt.Errorf("kill -9 %d: %w", pid, err)
	}
	return nil
}

// TailLog trả n dòng cuối log của service.
func (m *Manager) TailLog(id string, n int) string {
	f, err := os.Open(m.logPath(id))
	if err != nil {
		return ""
	}
	defer f.Close()
	// log vite/go run có thể phình to — chỉ đọc 256KB cuối
	const max = 256 * 1024
	if st, err := f.Stat(); err == nil && st.Size() > max {
		f.Seek(-max, io.SeekEnd)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func (m *Manager) runSync(cmdText, dir string) (string, error) {
	cmd := exec.Command("/bin/sh", "-c", cmdText)
	cmd.Dir = dir
	cmd.Env = m.env()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func waitGroupGone(pgid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if syscall.Kill(-pgid, 0) != nil { // ESRCH — group đã chết
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func waitPIDsGone(pids map[int]bool, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		alive := false
		for pid := range pids {
			if syscall.Kill(pid, 0) == nil {
				alive = true
				break
			}
		}
		if !alive {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
