package proc

import (
	"encoding/json"
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

	// stdinW: đầu ghi của pipe stdin — giữ mở để cây process không nhận EOF
	// (xem giải thích ở Start). Đóng khi process kết thúc.
	stdinW *os.File

	done    chan struct{}
	exitErr error
}

type Manager struct {
	mu     sync.Mutex
	procs  map[string]*Managed
	logDir string

	// stateDir: nơi lưu state file (pid process group) của service do app bật.
	// Nằm ở HOME để app GUI và CLI dùng chung — xem giải thích ở procState.
	stateDir string

	shellPathOnce sync.Once
	shellPathVal  string
}

// procState: thông tin process group do app quản lý, ghi ra đĩa.
//
// Vì sao cần: app giữ process trong RAM. Mỗi lần chạy CLI là một process riêng,
// và app mở lại cũng quên mất process đã bật → service do app bật mà chưa lên
// cổng (kẹt lúc khởi động) trở thành không tắt được bằng bất kỳ đường nào.
// File này giữ lại pgid để tắt/restart được từ cả app lẫn CLI.
type procState struct {
	PGID      int   `json:"pgid"`
	Started   int64 `json:"started"`   // unix milli — lúc app bật service (dùng để biết đã chạy bao lâu)
	ProcStart int64 `json:"procStart"` // unix milli — thời điểm process thật bắt đầu (ps lstart) — chống pid tái sử dụng
}

func NewManager(logDir, stateDir string) (*Manager, error) {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, fmt.Errorf("tạo thư mục log: %w", err)
	}
	if stateDir != "" {
		if err := os.MkdirAll(stateDir, 0o755); err != nil {
			return nil, fmt.Errorf("tạo thư mục state: %w", err)
		}
	}
	return &Manager{procs: map[string]*Managed{}, logDir: logDir, stateDir: stateDir}, nil
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

// ---------------------------------------------------------------------------
// State file — nhớ process group của service do app bật
// ---------------------------------------------------------------------------

func (m *Manager) statePath(id string) string {
	if m.stateDir == "" {
		return ""
	}
	return filepath.Join(m.stateDir, id+".proc.json")
}

func (m *Manager) writeState(id string, pgid int, started time.Time, procStart int64) {
	p := m.statePath(id)
	if p == "" {
		return
	}
	data, err := json.Marshal(procState{PGID: pgid, Started: started.UnixMilli(), ProcStart: procStart})
	if err != nil {
		return
	}
	_ = os.WriteFile(p, data, 0o644)
}

func (m *Manager) clearState(id string) {
	p := m.statePath(id)
	if p == "" {
		return
	}
	_ = os.Remove(p)
}

func (m *Manager) readState(id string) (procState, bool) {
	p := m.statePath(id)
	if p == "" {
		return procState{}, false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return procState{}, false
	}
	var st procState
	if err := json.Unmarshal(data, &st); err != nil || st.PGID <= 0 {
		return procState{}, false
	}
	return st, true
}

// readAllStates đọc mọi state file trong thư mục state.
func (m *Manager) readAllStates() map[string]procState {
	out := map[string]procState{}
	if m.stateDir == "" {
		return out
	}
	entries, err := os.ReadDir(m.stateDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".proc.json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".proc.json")
		if st, ok := m.readState(id); ok {
			out[id] = st
		}
	}
	return out
}

// stateAlive: process group trong state file còn sống và vẫn là process ta đã
// spawn. Pid bị hệ điều hành tái sử dụng thì pgid gần như chắc chắn khác, và
// thời điểm bắt đầu cũng khác → không kill nhầm process của người khác.
func stateAlive(st procState, groups map[int]int, psInfo map[int]scan.ProcInfo) bool {
	pgid, ok := groups[st.PGID]
	if !ok || pgid != st.PGID {
		return false
	}
	info, inPs := psInfo[st.PGID]
	if st.ProcStart > 0 {
		// Có ghi thời điểm process thật bắt đầu → so khớp tuyệt đối (ps lstart
		// chỉ có độ phân giải giây nên so theo giây là đủ).
		if !inPs || info.StartedAt.IsZero() || info.StartedAt.UnixMilli() != st.ProcStart {
			return false
		}
		return true
	}
	// Không lấy được thời điểm ps lúc spawn → so với lúc app bật, sai số 10s.
	if inPs && !info.StartedAt.IsZero() {
		d := info.StartedAt.Sub(time.UnixMilli(st.Started))
		if d < 0 {
			d = -d
		}
		if d > 10*time.Second {
			return false
		}
	}
	return true
}

// ManagedInfos trả trạng thái process do app quản lý cho mọi service.
// Gộp cả process trong RAM lẫn process đọc được từ state file → app mở lại
// (hoặc CLI chạy ở process riêng) vẫn nhận ra service do app đã bật.
func (m *Manager) ManagedInfos() map[string]scan.ManagedInfo {
	out := map[string]scan.ManagedInfo{}

	m.mu.Lock()
	live := map[string]*Managed{}
	for id, mg := range m.procs {
		select {
		case <-mg.done:
			continue
		default:
			live[id] = mg
		}
	}
	m.mu.Unlock()
	for id, mg := range live {
		out[id] = scan.ManagedInfo{PID: mg.PGID, Started: mg.Started.UnixMilli(), Alive: true}
	}

	states := m.readAllStates()
	if len(states) == 0 {
		return out
	}
	groups := scan.ProcessGroups()
	psInfo := scan.ProcessInfos()
	for id, st := range states {
		if _, ok := out[id]; ok {
			continue
		}
		if !stateAlive(st, groups, psInfo) {
			continue
		}
		out[id] = scan.ManagedInfo{PID: st.PGID, Started: st.Started, Alive: true}
	}
	return out
}

// managedAlive: trạng thái process của 1 service (RAM trước, state file sau).
func (m *Manager) managedAlive(id string) (scan.ManagedInfo, bool) {
	m.mu.Lock()
	mg := m.procs[id]
	m.mu.Unlock()
	if mg != nil {
		select {
		case <-mg.done:
		default:
			return scan.ManagedInfo{PID: mg.PGID, Started: mg.Started.UnixMilli(), Alive: true}, true
		}
	}
	st, ok := m.readState(id)
	if !ok {
		return scan.ManagedInfo{}, false
	}
	if !stateAlive(st, scan.ProcessGroups(), scan.ProcessInfos()) {
		return scan.ManagedInfo{}, false
	}
	return scan.ManagedInfo{PID: st.PGID, Started: st.Started, Alive: true}, true
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

	// Service đang sống mà app không quản lý trong RAM (app vừa mở lại, hoặc
	// CLI) → biết qua state file. Không được spawn thêm bản thứ hai, cổng sẽ
	// đụng. Hướng dẫn user dùng nút Restart.
	if mi, ok := m.managedAlive(s.ID); ok {
		return fmt.Errorf("service %s đang chạy (pid %d, bật lúc %s) — hãy Restart thay vì Bật",
			s.ID, mi.PID, time.UnixMilli(mi.Started).Format("15:04:05"))
	}

	// Log append, không truncate: log của lần chạy trước là thứ cần nhìn nhất
	// khi service fail lúc khởi động rồi bật lại.
	lf, err := os.OpenFile(m.logPath(s.ID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("mở log: %w", err)
	}
	fmt.Fprintf(lf, "\n=== bật lúc %s ===\n", time.Now().Format("2006-01-02 15:04:05"))
	cmd := exec.Command("/bin/sh", "-c", s.Start)
	cmd.Dir = workDir
	cmd.Env = m.env()
	// Setpgid: group riêng cho cả cây process — kill cả nhóm khi tắt
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = lf
	cmd.Stderr = lf
	// Stdin = pipe giữ mở (không đóng). Nếu để nil, Go dùng /dev/null → con đọc
	// stdin thấy EOF ngay và tự thoát. Đó là chuyện thật với umi max: plugin
	// tailwind của umi spawn `tailwindcss --watch`, mà tailwind v3 abort khi
	// stdin EOF ("Abort the watcher if stdin is closed") → file css không sinh →
	// umi báo "tailwindcss generate failed" rồi process.exit(1) → FE không lên
	// cổng. Giữ đầu đọc mở tới khi process kết thúc là đủ.
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		lf.Close()
		return fmt.Errorf("tạo stdin pipe: %w", err)
	}
	cmd.Stdin = stdinR
	// ExtraFiles: đưa luôn đầu ghi (fd 3) cho cây process con. Nhờ vậy stdin không
	// bao giờ EOF dù app (hay CLI -start) đã thoát — chỉ cần cây process còn sống
	// là đầu ghi còn mở.
	cmd.ExtraFiles = []*os.File{stdinW}
	if err := cmd.Start(); err != nil {
		stdinR.Close()
		stdinW.Close()
		lf.Close()
		return fmt.Errorf("spawn: %w", err)
	}
	// Đóng bản sao ở parent — child giữ fd riêng, giữ đầu ghi mở để không có EOF.
	stdinR.Close()
	mg := &Managed{ID: s.ID, PGID: cmd.Process.Pid, LogFile: lf, Started: time.Now(), stdinW: stdinW, done: make(chan struct{})}
	m.mu.Lock()
	m.procs[s.ID] = mg
	m.mu.Unlock()
	// Ghi state file — để app mở lại / CLI vẫn tìm được nhóm process này
	// (trường hợp service kẹt lúc khởi động, chưa lên cổng). Lưu kèm thời
	// điểm process thật bắt đầu (ps lstart) để sau này biết chắc pid đó vẫn
	// là process ta spawn, chứ không phải pid bị tái sử dụng.
	procStart := int64(0)
	if info, ok := scan.ProcessInfos()[mg.PGID]; ok && !info.StartedAt.IsZero() {
		procStart = info.StartedAt.UnixMilli()
	}
	m.writeState(s.ID, mg.PGID, mg.Started, procStart)
	go func() {
		mg.exitErr = cmd.Wait()
		stdinW.Close() // process đã chết → đóng stdin cho cây process con (nếu còn)
		lf.Close()     // không ai ghi vào log nữa → đóng file (tránh rò fd)
		// Process tự chết → state file thành rác, xoá đi (nhưng chỉ xoá bản
		// của mình: Restart có thể đã ghi state mới cho cùng id).
		if st, ok := m.readState(s.ID); ok && st.PGID == mg.PGID {
			m.clearState(s.ID)
		}
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
	stopped, err := m.stopIfRunning(s, listeners, workDir)
	if err != nil {
		return err
	}
	if !stopped {
		return fmt.Errorf("không tìm thấy process nào đang lắng nghe")
	}
	return nil
}

// Restart tắt service rồi bật lại. Dùng cho service kẹt trong lúc khởi động
// (process còn sống nhưng chưa lên cổng — lúc đó chưa có listener để kill) hoặc
// khi cần chạy lại code mới.
func (m *Manager) Restart(s config.Service, listeners []scan.Listener, workDir string) error {
	if _, err := m.stopIfRunning(s, listeners, workDir); err != nil {
		return fmt.Errorf("dừng service trước khi restart: %w", err)
	}
	return m.Start(s, workDir)
}

// stopIfRunning tắt service nếu đang chạy; trả về false nếu không có gì để tắt.
// Tìm process theo thứ tự: (1) process do app quản lý trong RAM, (2) process
// group ghi trong state file (app mở lại / CLI), (3) pid listener (service
// chạy ngoài app).
func (m *Manager) stopIfRunning(s config.Service, listeners []scan.Listener, workDir string) (bool, error) {
	if s.IsCompose() {
		cmdText := fmt.Sprintf("docker compose -f %s stop %s", shellQuote(s.ComposeFile), shellQuote(s.ComposeService))
		out, err := m.runSync(cmdText, workDir)
		if err != nil {
			return false, fmt.Errorf("docker compose stop: %v\n%s", err, out)
		}
		return true, nil
	}

	// (1) Process do app quản lý (map trong RAM)
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
		m.clearState(s.ID)
		return true, nil
	}

	// (2) App vừa mở lại (hoặc CLI): process group nằm trong state file.
	// Đây là đường duy nhất tắt được service kẹt lúc khởi động — lúc đó
	// service chưa lên cổng nên không có listener để mà kill.
	if _, ok := m.readState(s.ID); ok {
		if mi, alive := m.managedAlive(s.ID); alive {
			_ = syscall.Kill(-mi.PID, syscall.SIGTERM)
			if !waitGroupGone(mi.PID, 3*time.Second) {
				_ = syscall.Kill(-mi.PID, syscall.SIGKILL)
			}
			m.clearState(s.ID)
			return true, nil
		}
		m.clearState(s.ID) // state cũ, process đã chết → dọn
	}

	// (3) Service chạy ngoài app → chỉ kill đúng pid listener
	pids := map[int]bool{}
	for _, l := range listeners {
		pids[l.PID] = true
	}
	if len(pids) == 0 {
		return false, nil
	}
	for pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	if !waitPIDsGone(pids, 2*time.Second) {
		for pid := range pids {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	return true, nil
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

// StopContainer dừng container docker theo tên. Dùng cho cổng lạ do container
// giữ: pid mà lsof thấy khi đó là docker backend — kill nó = tắt cả Docker
// Desktop, nên thay bằng `docker stop` đúng container.
func (m *Manager) StopContainer(name string) error {
	if name == "" {
		return fmt.Errorf("thiếu tên container")
	}
	out, err := m.runSync("docker stop "+shellQuote(name), "")
	if err != nil {
		return fmt.Errorf("docker stop %s: %v\n%s", name, err, out)
	}
	return nil
}

// HasLog báo service đã từng có log (do app bật) — kể cả khi process đã chết
// và app không còn quản lý (sau khi mở lại app). UI dùng để bật nút "Log".
func (m *Manager) HasLog(id string) bool {
	st, err := os.Stat(m.logPath(id))
	return err == nil && !st.IsDir() && st.Size() > 0
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
