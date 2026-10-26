// Package daemon gom các cơ chế để Port Manager "chạy ngầm" bền bỉ:
//
//   - lock.go:  khoá một instance duy nhất (flock) + đánh thức instance đang
//     chạy bằng SIGUSR1 khi người dùng mở app lần hai.
//   - agent.go: cài/gỡ/xem trạng thái LaunchAgent của launchd để app tự khởi
//     động khi đăng nhập và tự mở lại khi bị tắt đột ngột.
//
// Vì sao cần cả hai: LaunchAgent với KeepAlive sẽ mở lại app khi app chết; nhưng
// nếu người dùng cũng tự mở app (make run, bấm Dock) thì sẽ có hai instance cùng
// quản lý một bộ service và cùng ghi một bộ state → phải có khoá để instance
// thứ hai nhường chỗ.
package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const (
	// LockFile là tên file khoá trong thư mục state (~/.port-manager/state).
	LockFile = "app.lock"

	// EnvAgent được launchd đặt (qua plist) cho instance mà nó khởi động.
	// Instance có biến này là "agent"; instance do người dùng mở thì không.
	EnvAgent = "PORT_MANAGER_AGENT"

	// ExitCodeConflict là mã thoát khi instance do launchd khởi động gặp khoá
	// đang bị giữ. Khác 0 để KeepAlive{SuccessfulExit:false} cho launchd thử
	// lại sau ThrottleInterval (~10s) — đây là cách "bàn giao": GUI thoát xong
	// thì lần thử kế tiếp của launchd sẽ lấy được khoá.
	//
	// Instance do NGƯỜI DÙNG mở mà gặp khoá thì thoát mã 0 (xem main.go) để
	// không bao giờ tạo vòng restart.
	ExitCodeConflict = 3
)

// ErrAlreadyRunning báo đã có instance khác giữ khoá.
type ErrAlreadyRunning struct {
	PID  int    // pid ghi trong file khoá (0 nếu không đọc được)
	Path string // đường dẫn file khoá
}

func (e *ErrAlreadyRunning) Error() string {
	if e.PID > 0 {
		return fmt.Sprintf("đã có Port Manager khác đang chạy (pid %d, khoá %s)", e.PID, e.Path)
	}
	return fmt.Sprintf("đã có Port Manager khác đang chạy (khoá %s)", e.Path)
}

// Lock là khoá một-instance đang được giữ. Release khi thoát; nếu app chết
// đột ngột, hệ điều hành tự nhả flock nên instance kế tiếp vẫn lấy được.
type Lock struct {
	f    *os.File
	path string
}

// LockPath trả đường dẫn file khoá trong stateDir.
func LockPath(stateDir string) string {
	return filepath.Join(stateDir, LockFile)
}

// AcquireLock lấy khoá một-instance trong stateDir và ghi pid hiện tại vào
// file khoá.
//
// Trả *ErrAlreadyRunning (kèm pid đọc được) khi instance khác đang giữ khoá.
// Lỗi khác (không tạo được thư mục, không mở được file) trả nguyên — caller
// quyết định có chạy tiếp không; mặc định main.go vẫn chạy tiếp để lỗi quyền
// file không làm app vô dụng.
func AcquireLock(stateDir string) (*Lock, error) {
	if strings.TrimSpace(stateDir) == "" {
		return nil, errors.New("thiếu thư mục state để đặt khoá")
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, fmt.Errorf("tạo thư mục state: %w", err)
	}
	path := LockPath(stateDir)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("mở file khoá: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		pid := readPID(f)
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, &ErrAlreadyRunning{PID: pid, Path: path}
		}
		return nil, fmt.Errorf("khoá file %s: %w", path, err)
	}
	// Đã giữ khoá → ghi pid để instance sau biết gửi SIGUSR1 cho ai.
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
		_ = f.Sync()
	}
	return &Lock{f: f, path: path}, nil
}

// Path trả đường dẫn file khoá đang giữ.
func (l *Lock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Release nhả khoá. Không xoá file: instance khác có thể vừa mở file này để
// thử lấy khoá; xoá đi sẽ làm họ khoá lên một inode đã mất.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	err := l.f.Close()
	l.f = nil
	return err
}

// readPID đọc pid ghi trong file khoá (0 nếu rỗng/hỏng).
func readPID(f *os.File) int {
	buf := make([]byte, 32)
	n, err := f.ReadAt(buf, 0)
	if n == 0 && err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// NotifyRunning gửi SIGUSR1 cho instance đang giữ khoá để nó hiện cửa sổ lên.
// Dùng khi người dùng mở app lần hai: thay vì chỉ thoát im lặng, instance cũ
// được đánh thức — đúng kỳ vọng "bấm mở app thì thấy app".
func NotifyRunning(pid int) error {
	if pid <= 0 {
		return errors.New("không biết pid instance đang chạy")
	}
	return syscall.Kill(pid, syscall.SIGUSR1)
}

// IsAgentProcess báo process hiện tại có phải do launchd (LaunchAgent) khởi
// động không — dựa vào biến môi trường mà plist đặt.
func IsAgentProcess() bool {
	return os.Getenv(EnvAgent) == "1"
}
