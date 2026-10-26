package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"smatec/port-manager/internal/api"
	"smatec/port-manager/internal/config"
	"smatec/port-manager/internal/daemon"
	"smatec/port-manager/internal/exitlog"
	"smatec/port-manager/internal/git"
	"smatec/port-manager/internal/history"
	"smatec/port-manager/internal/lastlog"
	"smatec/port-manager/internal/proc"
	"smatec/port-manager/internal/scan"
)

// core là tập hợp các thành phần lõi mà cả GUI (app.go) và CLI đều cần:
// config đã nạp, thư mục workspace, manager process, và các đường dẫn phụ.
type core struct {
	cfg        *config.Config
	root       string // workspace root đã resolve
	mgr        *proc.Manager
	configPath string // file ports.yaml để GHI (rỗng → chỉ có bản nhúng)
	logDir     string
	stateDir   string
}

// runCLI xử lý các flag headless:
//
//	-scan                      in kết quả quét (dạng bảng)
//	-json                      in kết quả quét dạng JSON (cho script)
//	-start <id>                bật 1 service
//	-stop <id>                 tắt 1 service
//	-restart <id>              restart 1 service
//	-start-group <tên>         bật cả group (hạ tầng → Backend → Frontend trong group)
//	-stop-all                  tắt toàn bộ workspace (ngược thứ tự bật)
//	-history <id>              in lịch sử chạy của service
//	-branches <id>             xem branch/worktree của repo service
//	-switch <id> <branch>      chuyển nhánh
//	-install-agent             cài LaunchAgent chạy ngầm (tự mở khi đăng nhập, tự mở lại khi bị tắt)
//	-uninstall-agent           gỡ LaunchAgent
//	-agent-status              in trạng thái LaunchAgent (exit 1 nếu chưa cài / không chạy)
//	-last-exit                 in lý do app GUI thoát lần trước
//	-version                   in phiên bản
//
// Exit code: 0 thành công · 1 lỗi thao tác · 2 sai cú pháp / không thấy service.
func runCLI(args []string) {
	switch args[0] {
	case "-version":
		fmt.Println(strings.TrimSpace(string(embeddedVersion)))

	case "-scan", "-json":
		c, err := loadCore()
		if err != nil {
			fmt.Fprintln(os.Stderr, "lỗi load config:", err)
			os.Exit(1)
		}
		res := scan.ScanAll(c.cfg, c.root, c.mgr)
		// Điền HasLog/LastError/repo_dir/health_* như App.Scan làm ở GUI — để
		// -scan/-json cho ra cùng mức thông tin, không phải chỉ GUI mới thấy.
		for gi := range res.Groups {
			for si := range res.Groups[gi].Services {
				sv := &res.Groups[gi].Services[si]
				sv.HasLog = c.mgr.HasLog(sv.ID)
				sv.LastError = lastlog.LastError(c.mgr.LogPath(sv.ID))
				if s, ok := c.cfg.Service(sv.ID); ok {
					sv.RepoDir = s.RepoDir
					sv.HealthPath = s.HealthPath
					sv.HealthSkipPorts = s.HealthSkipPorts
				}
			}
		}
		if res.Error != "" {
			fmt.Fprintln(os.Stderr, "lỗi quét:", res.Error)
			os.Exit(1)
		}
		if args[0] == "-json" {
			out, err := json.MarshalIndent(res, "", "  ")
			if err != nil {
				fmt.Fprintln(os.Stderr, "lỗi mã hoá JSON:", err)
				os.Exit(1)
			}
			fmt.Println(string(out))
			return
		}
		printScanResult(res)

	case "-start", "-stop", "-restart":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "thiếu id service: %s <id>\n", args[0])
			os.Exit(2)
		}
		c, err := loadCore()
		if err != nil {
			fmt.Fprintln(os.Stderr, "lỗi load config:", err)
			os.Exit(1)
		}
		s, ok := c.cfg.Service(args[1])
		if !ok {
			fmt.Fprintf(os.Stderr, "không tìm thấy service %q trong ports.yaml\n", args[1])
			os.Exit(2)
		}
		dir := c.cfg.ResolveDir(c.root, serviceDir(s))
		switch args[0] {
		case "-start":
			fmt.Printf("Đang bật %s (%s)...\n", s.Name, s.ID)
			if err := c.mgr.Start(s, dir); err != nil {
				fmt.Fprintln(os.Stderr, "LỖI:", err)
				os.Exit(1)
			}
			fmt.Println("Đã bật.")
		case "-stop":
			fmt.Printf("Đang tắt %s (%s)...\n", s.Name, s.ID)
			if err := c.mgr.Stop(s, listenersOfPorts(s.Ports), dir); err != nil {
				fmt.Fprintln(os.Stderr, "LỖI:", err)
				os.Exit(1)
			}
			fmt.Println("Đã tắt.")
		case "-restart":
			fmt.Printf("Đang restart %s (%s)...\n", s.Name, s.ID)
			if err := c.mgr.Restart(s, listenersOfPorts(s.Ports), dir); err != nil {
				fmt.Fprintln(os.Stderr, "LỖI:", err)
				os.Exit(1)
			}
			fmt.Println("Đã restart.")
		}

	case "-start-group":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "thiếu tên group: -start-group <tên group>")
			os.Exit(2)
		}
		c, err := loadCore()
		if err != nil {
			fmt.Fprintln(os.Stderr, "lỗi load config:", err)
			os.Exit(1)
		}
		ops := svcOps{cfg: c.cfg, root: c.root, mgr: c.mgr}
		g, ok := ops.group(args[1])
		if !ok {
			fmt.Fprintf(os.Stderr, "không tìm thấy group %q\n", args[1])
			os.Exit(2)
		}
		fmt.Printf("Đang bật group %s (%d service)...\n", g.Name, len(g.Services))
		if err := ops.startGroup(g); err != nil {
			fmt.Fprintln(os.Stderr, "LỖI:", err)
			os.Exit(1)
		}
		fmt.Println("Đã bật group.")

	case "-stop-all":
		c, err := loadCore()
		if err != nil {
			fmt.Fprintln(os.Stderr, "lỗi load config:", err)
			os.Exit(1)
		}
		ops := svcOps{cfg: c.cfg, root: c.root, mgr: c.mgr}
		fmt.Println("Đang tắt toàn bộ service...")
		if err := ops.stopAll(); err != nil {
			fmt.Fprintln(os.Stderr, "LỖI:", err)
			os.Exit(1)
		}
		fmt.Println("Đã tắt tất cả.")

	case "-history":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "thiếu id service: -history <id>")
			os.Exit(2)
		}
		c, err := loadCore()
		if err != nil {
			fmt.Fprintln(os.Stderr, "lỗi load config:", err)
			os.Exit(1)
		}
		printHistory(c, args[1])
	case "-branches":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "thiếu id service: -branches <id>")
			os.Exit(2)
		}
		c, err := loadCore()
		if err != nil {
			fmt.Fprintln(os.Stderr, "lỗi load config:", err)
			os.Exit(1)
		}
		printRepoDetails(c.cfg, c.root, args[1])

	case "-switch":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "thiếu tham số: -switch <id> <branch>")
			os.Exit(2)
		}
		c, err := loadCore()
		if err != nil {
			fmt.Fprintln(os.Stderr, "lỗi load config:", err)
			os.Exit(1)
		}
		s, ok := c.cfg.Service(args[1])
		if !ok {
			fmt.Fprintf(os.Stderr, "không tìm thấy service %q\n", args[1])
			os.Exit(2)
		}
		rd := scan.RepoDirPath(c.cfg, c.root, s)
		if rd == "" {
			fmt.Fprintf(os.Stderr, "service %s không khai báo git repo\n", s.ID)
			os.Exit(1)
		}
		if err := git.Switch(rd, args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "LỖI:", err)
			os.Exit(1)
		}
		fmt.Printf("Đã chuyển %s sang %s\n", s.ID, args[2])

	case "-install-agent":
		// CLI không giữ khoá một-instance nên cài thẳng (bootout → bootstrap →
		// kickstart). Nếu GUI đang mở, instance agent sẽ thoát mã 3 và launchd
		// thử lại tới khi GUI thoát — báo trước cho người dùng.
		if err := daemon.Install(daemon.InstallOptions{}); err != nil {
			fmt.Fprintln(os.Stderr, "LỖI:", err)
			os.Exit(1)
		}
		st := daemon.Status()
		fmt.Printf("Đã cài LaunchAgent %s\n  plist : %s\n  binary: %s\n  log   : %s\n", st.Label, st.PlistPath, st.BinPath, st.LogOutPath)
		if st.PID > 0 {
			fmt.Printf("Agent đang chạy (pid %d).\n", st.PID)
		} else {
			fmt.Println("Agent đã nạp; nếu GUI đang mở thì agent sẽ thay chỗ sau khi GUI thoát (launchd thử lại mỗi ~10s).")
		}

	case "-uninstall-agent":
		if err := daemon.Uninstall(); err != nil {
			fmt.Fprintln(os.Stderr, "LỖI:", err)
			os.Exit(1)
		}
		fmt.Println("Đã gỡ LaunchAgent", daemon.Label)

	case "-agent-status":
		printAgentStatus(daemon.Status())

	case "-last-exit":
		c, err := loadCore()
		if err != nil {
			fmt.Fprintln(os.Stderr, "lỗi load config:", err)
			os.Exit(1)
		}
		printLastExit(exitlog.New(c.stateDir).Last())

	default:
		fmt.Fprintf(os.Stderr, "flag không rõ: %s (dùng -scan | -json | -start <id> | -stop <id> | -restart <id> | -start-group <tên> | -stop-all | -history <id> | -branches <id> | -switch <id> <branch> | -install-agent | -uninstall-agent | -agent-status | -last-exit | -version)\n", args[0])
		os.Exit(2)
	}
}

// printAgentStatus in trạng thái LaunchAgent. Exit 1 khi chưa cài hoặc không
// chạy để script (`make agent-status && …`) dùng được làm điều kiện.
func printAgentStatus(st api.AgentStatus) {
	fmt.Printf("LaunchAgent : %s\n", st.Label)
	fmt.Printf("Đã cài      : %s\n", yesNo(st.Installed))
	fmt.Printf("Đã nạp      : %s\n", yesNo(st.Loaded))
	if st.PID > 0 {
		fmt.Printf("Đang chạy   : có (pid %d)\n", st.PID)
	} else {
		fmt.Println("Đang chạy   : không")
	}
	fmt.Printf("plist       : %s\n", st.PlistPath)
	fmt.Printf("binary      : %s\n", st.BinPath)
	fmt.Printf("log         : %s · %s\n", st.LogOutPath, st.LogErrPath)
	if !st.CanInstall {
		fmt.Printf("Không cài được từ process này: %s\n", st.CannotInstallReason)
	}
	if !st.Installed || st.PID == 0 {
		os.Exit(1)
	}
}

// printLastExit in kết luận về lần thoát trước của app GUI.
func printLastExit(info api.LastExitInfo) {
	switch info.Kind {
	case exitlog.KindNone:
		fmt.Println("Chưa có dữ liệu lần thoát trước.")
	case exitlog.KindQuit:
		fmt.Printf("Lần trước: thoát bình thường lúc %s (pid %d).\n", fmtAt(info.At), info.PID)
	case exitlog.KindSignal:
		fmt.Printf("Lần trước: bị %s lúc %s (pid %d). %s\n", info.Signal, fmtAt(info.At), info.PID, info.Detail)
	case exitlog.KindAbrupt:
		fmt.Printf("Lần trước: THOÁT ĐỘT NGỘT (pid %d). %s\n", info.PID, info.Detail)
	default:
		fmt.Printf("Lần trước: %s %s\n", info.Kind, info.Detail)
	}
}

func fmtAt(rfc string) string {
	t, err := time.Parse(time.RFC3339, rfc)
	if err != nil {
		return rfc
	}
	return t.Format("2006-01-02 15:04:05")
}

func yesNo(b bool) string {
	if b {
		return "có"
	}
	return "không"
}

// printHistory in lịch sử chạy của service (mới nhất trước) — dùng cho debug
// và script (supervisor đã tự bật lại bao nhiêu lần, crash lúc nào...).
func printHistory(c core, id string) {
	if _, ok := c.cfg.Service(id); !ok {
		fmt.Fprintf(os.Stderr, "không tìm thấy service %q\n", id)
		os.Exit(2)
	}
	h := history.New(c.stateDir)
	evs := h.List(id, history.DefaultLimit)
	if len(evs) == 0 {
		fmt.Printf("Chưa có lịch sử nào cho %s.\n", id)
		return
	}
	fmt.Printf("Lịch sử %s (%d sự kiện, mới nhất trước):\n", id, len(evs))
	for _, ev := range evs {
		t, err := time.Parse(time.RFC3339, ev.At)
		at := ev.At
		if err == nil {
			at = t.Format("2006-01-02 15:04:05")
		}
		if ev.Detail != "" {
			fmt.Printf("  %s  %-8s %s\n", at, ev.Kind, ev.Detail)
		} else {
			fmt.Printf("  %s  %s\n", at, ev.Kind)
		}
	}
}

func printRepoDetails(cfg *config.Config, root, id string) {
	s, ok := cfg.Service(id)
	if !ok {
		fmt.Fprintf(os.Stderr, "không tìm thấy service %q\n", id)
		os.Exit(2)
	}
	d, err := git.Details(scan.RepoDirPath(cfg, root, s))
	if err != nil {
		fmt.Fprintln(os.Stderr, "LỖI:", err)
		os.Exit(1)
	}
	v := d.View
	fmt.Printf("Repo     : %s%s\n", v.TopLevel, worktreeSuffix(v))
	fmt.Printf("Branch   : %s", v.Branch)
	if v.Upstream != "" {
		fmt.Printf(" (theo %s", v.Upstream)
		if v.Ahead > 0 {
			fmt.Printf(", ahead %d", v.Ahead)
		}
		if v.Behind > 0 {
			fmt.Printf(", behind %d", v.Behind)
		}
		fmt.Print(")")
	}
	fmt.Println()
	fmt.Printf("Thay đổi : %d chưa commit\n", v.Dirty)
	fmt.Println("Branches :")
	for _, b := range d.Branches {
		cur := ""
		if b == v.Branch && !v.Detached {
			cur = "  ← hiện tại"
		}
		fmt.Printf("  - %s%s\n", b, cur)
	}
	fmt.Println("Worktrees:")
	for _, w := range d.Worktrees {
		cur := ""
		if w.Current {
			cur = "  ← hiện tại"
		}
		fmt.Printf("  - %s (%s)%s\n", w.Path, w.Branch, cur)
	}
}

func worktreeSuffix(v *git.RepoView) string {
	if v.IsWorktree {
		return "  [linked worktree]"
	}
	return ""
}

func printScanResult(res *scan.Result) {
	fmt.Printf("Workspace: %s\n", res.WorkspaceRoot)
	for _, g := range res.Groups {
		fmt.Printf("\n== %s ==\n", g.Name)
		for _, s := range g.Services {
			ports := make([]string, 0, len(s.Ports))
			for _, p := range s.Ports {
				if p.Open {
					ports = append(ports, fmt.Sprintf("%d✓", p.Port))
				} else {
					ports = append(ports, fmt.Sprintf("%d·", p.Port))
				}
			}
			mark := "  "
			if s.ManagedByApp {
				mark = "* "
			}
			fmt.Printf("%s%-40s %-12s %-10s", mark, s.Name, strings.Join(ports, " "), statusVN(s.Status))
			if s.PID != 0 {
				fmt.Printf(" pid=%-7d %s", s.PID, truncateStr(s.Command, 52))
			} else if s.ManagedPID != 0 {
				// App đang giữ process nhưng service chưa lên cổng (đang khởi
				// động / kẹt) → vẫn biết pid để tắt được.
				fmt.Printf(" pid=%-7d %s", s.ManagedPID, "(app đang giữ — chưa lên cổng)")
			}
			if s.StartedAt > 0 {
				st := time.UnixMilli(s.StartedAt)
				fmt.Printf("  ▶ %s · %s", st.Format("15:04:05"), humanDuration(time.Since(st)))
			}
			if s.Repo != nil {
				b := s.Repo.Branch
				if s.Repo.Dirty > 0 {
					b += fmt.Sprintf(" *%d", s.Repo.Dirty)
				}
				fmt.Printf("  ⎇ %s%s", b, worktreeSuffix(s.Repo))
			}
			fmt.Println()
			if s.LastError != "" {
				fmt.Printf("    ↳ lỗi cuối: %s\n", truncateStr(s.LastError, 100))
			}
		}
	}
	if len(res.Unknown) > 0 {
		fmt.Println("\n== Cổng lạ (không nằm trong cấu hình) ==")
		for _, u := range res.Unknown {
			fmt.Printf("  %-6d pid=%-7d %s", u.Port, u.PID, truncateStr(u.Command, 52))
			if u.Container != "" {
				fmt.Printf("  🐳 %s", u.Container)
				if u.Project != "" {
					fmt.Printf(" · %s", u.Project)
				}
			} else if u.Cwd != "" {
				fmt.Printf("  📁 %s", u.Cwd)
			}
			if u.StartedAt > 0 {
				st := time.UnixMilli(u.StartedAt)
				fmt.Printf("  ▶ %s · %s", st.Format("15:04:05"), humanDuration(time.Since(st)))
			}
			fmt.Println()
		}
	} else {
		fmt.Println("\nKhông có cổng lạ.")
	}
}

func statusVN(st string) string {
	switch st {
	case "running":
		return "ĐANG CHẠY"
	case "partial":
		return "MỘT PHẦN"
	case "starting":
		return "ĐANG KHỞI ĐỘNG"
	case "stuck":
		return "KẸT"
	default:
		return "tắt"
	}
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// humanDuration định dạng khoảng thời gian đã trôi qua cho dễ đọc: 45s · 12m 30s
// · 2h 15m · 3d 4h.
func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

// listenersOfPorts quét và lọc listener thuộc đúng các port khai của service.
func listenersOfPorts(ports []int) []scan.Listener {
	want := map[int]bool{}
	for _, p := range ports {
		want[p] = true
	}
	ls, err := scan.Listeners()
	if err != nil {
		return nil
	}
	out := []scan.Listener{}
	for _, l := range ls {
		if want[l.Port] {
			out = append(out, l)
		}
	}
	return out
}

func serviceDir(s config.Service) string {
	if s.IsCompose() {
		return s.ComposeDir
	}
	return s.Dir
}

// findConfigPath: env PORT_MANAGER_CONFIG → ./ports.yaml →
// <workspace mặc định>/port-manager/ports.yaml → cạnh executable.
// Trả đường dẫn TUYỆT ĐỐI để các thao tác ghi cấu hình (edit.go) luôn ghi
// đúng chỗ bất kể cwd thay đổi. Không thấy file → rỗng, dùng bản nhúng.
func findConfigPath() (string, bool) {
	if p := os.Getenv("PORT_MANAGER_CONFIG"); p != "" && fileExists(p) {
		if abs, err := filepath.Abs(p); err == nil {
			return abs, true
		}
		return p, true
	}
	if fileExists("ports.yaml") {
		abs, err := filepath.Abs("ports.yaml")
		if err == nil {
			return abs, true
		}
		return "ports.yaml", true
	}
	// Workspace mặc định $HOME/Documents/smatec — cùng fallback với
	// config.ResolveRoot. GUI chạy ngoài repo (app .app) vẫn tìm thấy
	// ports.yaml của port-manager như khi mở từ workspace.
	if home, err := os.UserHomeDir(); err == nil {
		root := filepath.Join(home, "Documents", "smatec")
		if fileExists(filepath.Join(root, "AGENTS.md")) {
			if cand := filepath.Join(root, "port-manager", "ports.yaml"); fileExists(cand) {
				return cand, true
			}
		}
	}
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "ports.yaml")
		if fileExists(cand) {
			return cand, true
		}
	}
	return "", false
}

func loadCore() (core, error) {
	var (
		cfg    *config.Config
		cfgDir string
		err    error
	)
	configPath, found := findConfigPath()
	if found {
		cfg, cfgDir, err = config.Load(configPath)
	} else {
		// .app bundle không kèm ports.yaml ngoài → dùng bản embed
		cfg, err = config.Parse(embeddedPortsYAML)
		if err == nil {
			cfgDir, _ = os.Getwd()
		}
	}
	if err != nil {
		return core{}, err
	}
	root := cfg.ResolveRoot(cfgDir)
	mgr, err := proc.NewManager(resolveLogDir(), resolveStateDir())
	if err != nil {
		return core{}, err
	}
	return core{
		cfg:        cfg,
		root:       root,
		mgr:        mgr,
		configPath: configPath,
		logDir:     resolveLogDir(),
		stateDir:   resolveStateDir(),
	}, nil
}

// resolveLogDir: env → ./logs (khi chạy từ repo) → ~/.port-manager/logs (app .app).
func resolveLogDir() string {
	if p := os.Getenv("PORT_MANAGER_LOGS"); p != "" {
		return p
	}
	if wd, err := os.Getwd(); err == nil && dirWritable(wd) {
		return filepath.Join(wd, "logs")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".port-manager", "logs")
}

// resolveStateDir: thư mục lưu state file (pid process group) của service do
// app bật. Luôn ở HOME — không phụ thuộc cwd — để app GUI (cwd="/") và CLI
// (cwd=repo) dùng chung một chỗ, nên CLI cũng tắt được service do app bật.
func resolveStateDir() string {
	if p := os.Getenv("PORT_MANAGER_STATE"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".port-manager", "state")
}

func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".pm-write-test-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
