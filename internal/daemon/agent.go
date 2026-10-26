// Quản lý LaunchAgent của launchd cho Port Manager.
//
// Mục đích: app tự khởi động khi người dùng đăng nhập (RunAtLoad) và tự mở
// lại khi bị tắt đột ngột (KeepAlive với SuccessfulExit:false — thoát mã 0
// thì KHÔNG mở lại, thoát mã khác/signal thì mở lại sau ~10s).
//
// Vì sao chạy binary trần (~/.port-manager/bin/port-manager) thay vì .app:
// plist ProgramArguments trỏ thẳng vào executable là cách chắc chắn nhất —
// không phụ thuộc thư mục build trong repo (make clean xoá mất), không cần
// `open -a` (không chạy được khi không có session Aqua đẹp). Đổi lại icon
// Dock là icon mặc định — chấp nhận ở v0.7.0 (xem plan §7).
package daemon

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Label định danh LaunchAgent (trùng bundle id của .app để dễ tra cứu).
const Label = "vn.smatec.port-manager"

// handoverDelay là thời gian bàn giao: sau khi cài từ GUI, một script nền
// (tách session) chờ hết delay rồi mới bootstrap — để GUI kịp thoát và nhả
// khoá một-instance trước khi launchd mở instance mới.
const handoverDelay = 2 * time.Second

// AgentStatus là trạng thái chạy ngầm trả cho GUI/CLI. Tên field JSON là
// hợp đồng với frontend — không đổi.
type AgentStatus struct {
	Label               string `json:"label"`
	PlistPath           string `json:"plistPath"`
	BinPath             string `json:"binPath"`
	LogOutPath          string `json:"logOutPath"`
	LogErrPath          string `json:"logErrPath"`
	Installed           bool   `json:"installed"`   // plist tồn tại
	Loaded              bool   `json:"loaded"`      // launchctl print thành công
	PID                 int    `json:"pid"`         // 0 = không chạy
	ThisIsAgent         bool   `json:"thisIsAgent"` // process hiện tại do launchd khởi động
	CanInstall          bool   `json:"canInstall"`  // false khi đang go run / sai OS
	CannotInstallReason string `json:"cannotInstallReason"`
}

// InstallOptions tuỳ chọn cài (test thay Exec/launchctl).
type InstallOptions struct {
	// Handover: true khi cài từ GUI — GUI đang giữ khoá một-instance, nên
	// không bootstrap ngay mà lên lịch bootstrap sau handoverDelay để GUI
	// thoát trước. false khi cài từ CLI (CLI không giữ khoá).
	Handover bool
	// Exe: đường dẫn binary cần agent chạy. Rỗng → os.Executable().
	Exe string
	// PATH: chuỗi PATH ghi vào plist để service con (`make`, `go`, `npm`…)
	// tìm được binary như khi app chạy từ terminal. Rỗng → PATH hiện tại.
	PATH string
	// runCmd: runner launchctl (test thay). nil → chạy thật.
	runCmd func(name string, args ...string) (string, error)
}

// PlistPath trả đường dẫn plist LaunchAgent: ~/Library/LaunchAgents/<Label>.plist.
func PlistPath() string { return filepath.Join(launchAgentsDir(), Label+".plist") }

// BinPath trả đường dẫn binary mà agent chạy: ~/.port-manager/bin/port-manager.
func BinPath() string { return filepath.Join(portManagerDir(), "bin", "port-manager") }

// LogPaths trả cặp file log chuẩn của agent (launchd StandardOut/Err).
func LogPaths() (out, errPath string) {
	dir := filepath.Join(portManagerDir(), "logs")
	return filepath.Join(dir, "agent.out.log"), filepath.Join(dir, "agent.err.log")
}

func launchAgentsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "LaunchAgents")
}

func portManagerDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".port-manager")
}

// RefuseGoRun trả lỗi khi exe là binary tạm của `go run` (nằm trong thư mục
// go-build của tmp): binary đó bị xoá ngay khi `go run` thoát — cài vào plist
// thì launchd sẽ không tìm thấy lần chạy sau.
func RefuseGoRun(exe string) error {
	if strings.Contains(exe, string(filepath.Separator)+"go-build") {
		return errors.New("app đang chạy bằng `go run` (binary tạm) — hãy `make build` rồi mở build/PortManager.app để cài chạy ngầm")
	}
	return nil
}

// Status trả trạng thái hiện tại. launchctl lỗi/không có → Loaded=false, KHÔNG
// coi là lỗi: "chưa loaded" là dữ liệu trạng thái, không phải sự cố.
func Status() AgentStatus {
	st := AgentStatus{
		Label:       Label,
		PlistPath:   PlistPath(),
		BinPath:     BinPath(),
		ThisIsAgent: IsAgentProcess(),
	}
	st.LogOutPath, st.LogErrPath = LogPaths()
	if st.PlistPath != "" {
		_, err := os.Stat(st.PlistPath)
		st.Installed = err == nil
	}
	if pid, err := launchctlPID(); err == nil {
		st.Loaded = true
		st.PID = pid
	}
	exe, exeErr := os.Executable()
	switch {
	case exeErr != nil:
		st.CannotInstallReason = "không xác định được đường dẫn app: " + exeErr.Error()
	default:
		if err := RefuseGoRun(exe); err != nil {
			st.CannotInstallReason = err.Error()
		} else {
			st.CanInstall = true
		}
	}
	return st
}

// runLaunchctl chạy launchctl thật (biến package để test thay).
var runLaunchctl = func(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

func launchctl(args ...string) (string, error) {
	return launchctlWith(runLaunchctl, args...)
}

// launchctlWith chạy launchctl qua runner cho trước (Install nhận runner từ
// InstallOptions để test không gọi launchctl thật).
func launchctlWith(run func(name string, args ...string) (string, error), args ...string) (string, error) {
	return run("launchctl", args...)
}

// serviceTarget là định danh "gui/<uid>/<label>" cho launchctl print/bootout/kickstart.
func serviceTarget() string {
	return fmt.Sprintf("gui/%d/%s", os.Getuid(), Label)
}

// domainTarget là "gui/<uid>" cho bootstrap.
func domainTarget() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

// launchctlPID hỏi launchd agent đang chạy với pid bao nhiêu.
// Lỗi → agent chưa loaded (dùng errors.Is(err, errNotLoaded) không cần — Status
// chỉ cần biết thành công/thất bại).
func launchctlPID() (int, error) {
	out, err := launchctl("print", serviceTarget())
	if err != nil {
		return 0, err
	}
	return parseLaunchctlPID(out), nil
}

// pidLine khớp dòng "pid = 1234" trong output của `launchctl print`.
var pidLine = regexp.MustCompile(`(?m)^\s*pid\s*=\s*(\d+)\s*$`)

// parseLaunchctlPID tách pid từ output `launchctl print`. Không thấy → 0.
func parseLaunchctlPID(out string) int {
	m := pidLine.FindStringSubmatch(out)
	if m == nil {
		return 0
	}
	pid, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return pid
}

// Install cài LaunchAgent: copy binary → ghi plist → (bàn giao nếu Handover)
// bootstrap + kickstart.
//
// An toàn khi cài lại: mọi bước ghi đều qua file tạm + rename; bootout trước
// bootstrap để plist mới được nạp trọn vẹn.
func Install(opts InstallOptions) error {
	exe := opts.Exe
	if strings.TrimSpace(exe) == "" {
		e, err := os.Executable()
		if err != nil {
			return fmt.Errorf("không xác định được binary: %w", err)
		}
		exe, _ = filepath.EvalSymlinks(e)
	}
	if err := RefuseGoRun(exe); err != nil {
		return err
	}
	runner := opts.runCmd
	if runner == nil {
		runner = runLaunchctl
	}

	if err := copyExecutable(exe, BinPath()); err != nil {
		return err
	}
	outLog, errLog := LogPaths()
	if err := os.MkdirAll(filepath.Dir(outLog), 0o755); err != nil {
		return fmt.Errorf("tạo thư mục log agent: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(PlistPath()), 0o755); err != nil {
		return fmt.Errorf("tạo thư mục LaunchAgents: %w", err)
	}
	path := opts.PATH
	if path == "" {
		path = os.Getenv("PATH")
	}
	plist := RenderPlist(PlistParams{
		Label:            Label,
		Program:          BinPath(),
		OutLog:           outLog,
		ErrLog:           errLog,
		Env:              map[string]string{EnvAgent: "1", "PATH": path},
		RunAtLoad:        true,
		KeepAliveOnCrash: true,
	})
	if err := writeAtomic(PlistPath(), []byte(plist)); err != nil {
		return err
	}

	if opts.Handover {
		// Bàn giao từ GUI: GUI đang giữ khoá một-instance, phải thoát trước
		// rồi launchd mới mở được. Script nền tách session (Setsid) sống sót
		// khi app thoát; lỗi launchctl bị nuốt — lần thử sau của KeepAlive
		// hoặc lần cài kế sẽ sửa. Không chờ script: cài xong là trả ngay.
		return spawnHandover(handoverDelay)
	}

	// Cài từ CLI: không ai giữ khoá → bootout (bỏ qua lỗi chưa loaded),
	// bootstrap nạp plist, kickstart -k chạy ngay dù RunAtLoad đã chạy rồi.
	_, _ = launchctlWith(runner, "bootout", serviceTarget())
	if _, err := launchctlWith(runner, "bootstrap", domainTarget(), PlistPath()); err != nil {
		return fmt.Errorf("launchctl bootstrap: %w", err)
	}
	if _, err := launchctlWith(runner, "kickstart", "-k", serviceTarget()); err != nil {
		return fmt.Errorf("launchctl kickstart: %w", err)
	}
	return nil
}

// spawnHandover lên lịch bootstrap sau delay bằng script nền tách session.
// GUI gọi hàm này rồi tự thoát — script tự chạy tiếp không cần app.
func spawnHandover(delay time.Duration) error {
	script := fmt.Sprintf(
		"sleep %d; launchctl bootout %s >/dev/null 2>&1; launchctl bootstrap %s %q >/dev/null 2>&1; launchctl kickstart -k %s >/dev/null 2>&1",
		int(delay.Seconds()), serviceTarget(), domainTarget(), PlistPath(), serviceTarget(),
	)
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("chạy script bàn giao: %w", err)
	}
	go func() { _ = cmd.Wait() }() // tránh zombie; không quan tâm kết quả
	return nil
}

// Uninstall gỡ agent: xoá plist + binary TRƯỚC rồi mới bootout sau cùng —
// bootout gửi SIGTERM cho instance agent đang chạy; nếu instance đó chính là
// process này thì nó sẽ thoát ngay trong lệnh bootout, các bước sau không
// chạy nữa. Xoá file trước để dù chết giữa chừng cũng không để lại rác.
func Uninstall() error {
	var firstErr error
	if err := os.Remove(PlistPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		firstErr = fmt.Errorf("xoá plist: %w", err)
	}
	if err := os.Remove(BinPath()); err != nil && !errors.Is(err, os.ErrNotExist) && firstErr == nil {
		firstErr = fmt.Errorf("xoá binary: %w", err)
	}
	_, _ = launchctl("bootout", serviceTarget()) // chưa loaded → lỗi, bỏ qua
	return firstErr
}

// PlistParams tham số dựng nội dung plist (tách riêng để test không đụng file).
type PlistParams struct {
	Label            string
	Program          string
	OutLog, ErrLog   string
	Env              map[string]string
	RunAtLoad        bool
	KeepAliveOnCrash bool // thoát mã khác 0 / chết vì signal → launchd mở lại
}

// RenderPlist dựng nội dung XML plist. Hàm thuần — test được không cần file.
func RenderPlist(p PlistParams) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n<dict>\n")
	writeKeyString(&b, "Label", p.Label)
	b.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	b.WriteString("    <string>" + xmlEscape(p.Program) + "</string>\n  </array>\n")
	if p.RunAtLoad {
		b.WriteString("  <key>RunAtLoad</key>\n  <true/>\n")
	}
	if p.KeepAliveOnCrash {
		// SuccessfulExit=false: giữ sống khi thoát KHÔNG sạch (mã != 0,
		// signal). Thoát mã 0 (người dùng chủ ý thoát) → launchd không mở lại.
		b.WriteString("  <key>KeepAlive</key>\n  <dict>\n")
		b.WriteString("    <key>SuccessfulExit</key>\n    <false/>\n  </dict>\n")
	}
	if len(p.Env) > 0 {
		b.WriteString("  <key>EnvironmentVariables</key>\n  <dict>\n")
		for _, k := range sortedKeys(p.Env) {
			writeKeyString(&b, k, p.Env[k])
		}
		b.WriteString("  </dict>\n")
	}
	if p.OutLog != "" {
		writeKeyString(&b, "StandardOutPath", p.OutLog)
	}
	if p.ErrLog != "" {
		writeKeyString(&b, "StandardErrorPath", p.ErrLog)
	}
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

func writeKeyString(b *strings.Builder, key, val string) {
	b.WriteString("  <key>" + xmlEscape(key) + "</key>\n")
	b.WriteString("  <string>" + xmlEscape(val) + "</string>\n")
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ { // insertion sort — danh sách gần như luôn ≤ 3 phần tử
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// xmlEscaper escape 5 ký tự đặc biệt của XML — đường dẫn home/PATH có thể
// chứa `&` hay dấu nháy.
var xmlEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
	"'", "&apos;",
)

func xmlEscape(s string) string { return xmlEscaper.Replace(s) }

// copyExecutable copy binary sang dst qua file tạm + rename, chmod 755.
// Rename trong cùng ổ đĩa là nguyên tử → agent không bao giờ chạy nửa file.
func copyExecutable(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("đọc binary %s: %w", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("tạo thư mục bin: %w", err)
	}
	if err := writeAtomic(dst, data); err != nil {
		return err
	}
	if err := os.Chmod(dst, 0o755); err != nil {
		return fmt.Errorf("chmod binary: %w", err)
	}
	return nil
}

// writeAtomic ghi data vào path qua file tạm cùng thư mục + rename.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("tạo file tạm: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name) // dọn nếu rename lỗi
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("ghi %s: %w", name, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("ghi %s ra đĩa: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
