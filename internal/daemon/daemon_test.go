package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderPlist(t *testing.T) {
	out := RenderPlist(PlistParams{
		Label:            "vn.test",
		Program:          "/Users/a b/.port-manager/bin/port-manager",
		OutLog:           "/tmp/out.log",
		ErrLog:           "/tmp/err.log",
		Env:              map[string]string{"PATH": "/a:/b&c", EnvAgent: "1"},
		RunAtLoad:        true,
		KeepAliveOnCrash: true,
	})
	for _, want := range []string{
		"<key>Label</key>\n  <string>vn.test</string>",
		"<string>/Users/a b/.port-manager/bin/port-manager</string>",
		"<key>RunAtLoad</key>\n  <true/>",
		"<key>SuccessfulExit</key>\n    <false/>",
		"<key>PATH</key>\n  <string>/a:/b&amp;c</string>", // & phải được escape
		"<key>" + EnvAgent + "</key>\n  <string>1</string>",
		"<key>StandardOutPath</key>\n  <string>/tmp/out.log</string>",
		"<key>StandardErrorPath</key>\n  <string>/tmp/err.log</string>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plist thiếu %q\n%s", want, out)
		}
	}
	// Env phải theo thứ tự khoá ổn định (PATH trước PORT_MANAGER_AGENT) để
	// plist không đổi nội dung mỗi lần cài lại.
	if strings.Index(out, "<key>PATH</key>") > strings.Index(out, "<key>"+EnvAgent+"</key>") {
		t.Errorf("env không sắp theo khoá:\n%s", out)
	}
}

func TestRenderPlistNoKeepAlive(t *testing.T) {
	out := RenderPlist(PlistParams{Label: "x", Program: "/bin/true"})
	if strings.Contains(out, "KeepAlive") || strings.Contains(out, "RunAtLoad") || strings.Contains(out, "EnvironmentVariables") {
		t.Errorf("plist tối giản không được có KeepAlive/RunAtLoad/Env:\n%s", out)
	}
}

func TestParseLaunchctlPID(t *testing.T) {
	cases := map[string]int{
		"gui/501/vn.smatec.port-manager = {\n\tactive count = 1\n\tpath = /x.plist\n\tstate = running\n\n\tpid = 4242\n\tprogram = /x\n}": 4242,
		"state = not running\n": 0,
		"":                      0,
		"pid = abc":             0,
	}
	for in, want := range cases {
		if got := parseLaunchctlPID(in); got != want {
			t.Errorf("parseLaunchctlPID(%q) = %d, muốn %d", in, got, want)
		}
	}
}

func TestRefuseGoRun(t *testing.T) {
	if err := RefuseGoRun("/var/folders/ab/T/go-build123/b001/exe/port-manager"); err == nil {
		t.Error("binary go run phải bị từ chối")
	}
	if err := RefuseGoRun("/Users/x/Documents/smatec/port-manager/build/port-manager"); err != nil {
		t.Errorf("binary build thật bị từ chối nhầm: %v", err)
	}
}

func TestAcquireLockTwice(t *testing.T) {
	dir := t.TempDir()
	l1, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("lấy khoá lần 1: %v", err)
	}
	defer l1.Release()

	// flock là khoá theo open-file-description: cùng một process mở file lần
	// hai vẫn bị chặn → mô phỏng được instance thứ hai trong test.
	_, err = AcquireLock(dir)
	var already *ErrAlreadyRunning
	if !asAlreadyRunning(err, &already) {
		t.Fatalf("lần 2 phải trả ErrAlreadyRunning, được: %v", err)
	}
	if already.PID != os.Getpid() {
		t.Errorf("pid trong khoá = %d, muốn %d", already.PID, os.Getpid())
	}
	if already.Path != filepath.Join(dir, LockFile) {
		t.Errorf("đường dẫn khoá sai: %s", already.Path)
	}

	// Nhả rồi lấy lại được.
	if err := l1.Release(); err != nil {
		t.Fatalf("nhả khoá: %v", err)
	}
	l3, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("lấy khoá sau khi nhả: %v", err)
	}
	l3.Release()
}

func TestAcquireLockEmptyDir(t *testing.T) {
	if _, err := AcquireLock(""); err == nil {
		t.Error("stateDir rỗng phải lỗi")
	}
}

func TestInstallRefusesGoRun(t *testing.T) {
	err := Install(InstallOptions{Exe: "/tmp/go-build1/b001/exe/pm"})
	if err == nil || !strings.Contains(err.Error(), "go run") {
		t.Errorf("Install với binary go run phải báo lỗi go run, được: %v", err)
	}
}

func asAlreadyRunning(err error, target **ErrAlreadyRunning) bool {
	if err == nil {
		return false
	}
	e, ok := err.(*ErrAlreadyRunning)
	if !ok {
		return false
	}
	*target = e
	return true
}
