package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"smatec/port-manager/internal/config"
	"smatec/port-manager/internal/git"
	"smatec/port-manager/internal/proc"
	"smatec/port-manager/internal/scan"
)

// runCLI xử lý các flag headless: -scan / -start / -stop / -version.
// Dùng để test không cần mở cửa sổ + tiện script hoá.
func runCLI(args []string) {
	switch args[0] {
	case "-version":
		fmt.Println(strings.TrimSpace(string(embeddedVersion)))
	case "-scan":
		cfg, mgr, root, err := loadCore()
		if err != nil {
			fmt.Fprintln(os.Stderr, "lỗi load config:", err)
			os.Exit(1)
		}
		res := scan.ScanAll(cfg, root, mgr.IsManaged)
		if res.Error != "" {
			fmt.Fprintln(os.Stderr, "lỗi quét:", res.Error)
			os.Exit(1)
		}
		printScanResult(res)
	case "-start", "-stop":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "thiếu id service: %s <id>\n", args[0])
			os.Exit(2)
		}
		cfg, mgr, root, err := loadCore()
		if err != nil {
			fmt.Fprintln(os.Stderr, "lỗi load config:", err)
			os.Exit(1)
		}
		s, ok := cfg.Service(args[1])
		if !ok {
			fmt.Fprintf(os.Stderr, "không tìm thấy service %q trong ports.yaml\n", args[1])
			os.Exit(2)
		}
		dir := cfg.ResolveDir(root, serviceDir(s))
		if args[0] == "-start" {
			fmt.Printf("Đang bật %s (%s)...\n", s.Name, s.ID)
			if err := mgr.Start(s, dir); err != nil {
				fmt.Fprintln(os.Stderr, "LỖI:", err)
				os.Exit(1)
			}
			fmt.Println("Đã bật.")
		} else {
			fmt.Printf("Đang tắt %s (%s)...\n", s.Name, s.ID)
			if err := mgr.Stop(s, listenersOfPorts(s.Ports), dir); err != nil {
				fmt.Fprintln(os.Stderr, "LỖI:", err)
				os.Exit(1)
			}
			fmt.Println("Đã tắt.")
		}
	case "-branches":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "thiếu id service: -branches <id>")
			os.Exit(2)
		}
		cfg, _, root, err := loadCore()
		if err != nil {
			fmt.Fprintln(os.Stderr, "lỗi load config:", err)
			os.Exit(1)
		}
		printRepoDetails(cfg, root, args[1])
	case "-switch":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "thiếu tham số: -switch <id> <branch>")
			os.Exit(2)
		}
		cfg, _, root, err := loadCore()
		if err != nil {
			fmt.Fprintln(os.Stderr, "lỗi load config:", err)
			os.Exit(1)
		}
		s, ok := cfg.Service(args[1])
		if !ok {
			fmt.Fprintf(os.Stderr, "không tìm thấy service %q\n", args[1])
			os.Exit(2)
		}
		rd := scan.RepoDirPath(cfg, root, s)
		if rd == "" {
			fmt.Fprintf(os.Stderr, "service %s không khai báo git repo\n", s.ID)
			os.Exit(1)
		}
		if err := git.Switch(rd, args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "LỖI:", err)
			os.Exit(1)
		}
		fmt.Printf("Đã chuyển %s sang %s\n", s.ID, args[2])
	default:
		fmt.Fprintf(os.Stderr, "flag không rõ: %s (dùng -scan | -start <id> | -stop <id> | -branches <id> | -switch <id> <branch> | -version)\n", args[0])
		os.Exit(2)
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
			}
			if s.Repo != nil {
				b := s.Repo.Branch
				if s.Repo.Dirty > 0 {
					b += fmt.Sprintf(" *%d", s.Repo.Dirty)
				}
				fmt.Printf("  ⎇ %s%s", b, worktreeSuffix(s.Repo))
			}
			fmt.Println()
		}
	}
	if len(res.Unknown) > 0 {
		fmt.Println("\n== Cổng lạ (không nằm trong cấu hình) ==")
		for _, u := range res.Unknown {
			fmt.Printf("  %-6d pid=%-7d %s\n", u.Port, u.PID, truncateStr(u.Command, 72))
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

// findConfigPath: env PORT_MANAGER_CONFIG → ./ports.yaml → cạnh executable → embedded.
func findConfigPath() (string, bool) {
	if p := os.Getenv("PORT_MANAGER_CONFIG"); p != "" && fileExists(p) {
		return p, true
	}
	if fileExists("ports.yaml") {
		return "ports.yaml", true
	}
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "ports.yaml")
		if fileExists(cand) {
			return cand, true
		}
	}
	return "", false
}

func loadCore() (*config.Config, *proc.Manager, string, error) {
	var (
		cfg    *config.Config
		cfgDir string
		err    error
	)
	if path, ok := findConfigPath(); ok {
		cfg, cfgDir, err = config.Load(path)
	} else {
		// .app bundle không kèm ports.yaml ngoài → dùng bản embed
		cfg, err = config.Parse(embeddedPortsYAML)
		if err == nil {
			cfgDir, _ = os.Getwd()
		}
	}
	if err != nil {
		return nil, nil, "", err
	}
	root := cfg.ResolveRoot(cfgDir)
	mgr, err := proc.NewManager(resolveLogDir())
	if err != nil {
		return nil, nil, "", err
	}
	return cfg, mgr, root, nil
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
