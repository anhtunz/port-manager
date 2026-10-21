package main

import (
	"context"
	"fmt"

	"smatec/port-manager/internal/config"
	"smatec/port-manager/internal/git"
	"smatec/port-manager/internal/proc"
	"smatec/port-manager/internal/scan"
)

// App là struct expose cho frontend qua Wails bindings (window.go.main.App.*).
// Hợp đồng API chi tiết ở docs/plans/FEAT_*.md mục 4.
type App struct {
	ctx     context.Context
	cfg     *config.Config
	mgr     *proc.Manager
	root    string
	version string
}

func NewApp(version string) (*App, error) {
	cfg, mgr, root, err := loadCore()
	if err != nil {
		return nil, err
	}
	return &App{cfg: cfg, mgr: mgr, root: root, version: version}, nil
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

func (a *App) Scan() *scan.Result {
	res := scan.ScanAll(a.cfg, a.root, a.mgr)
	// Service từng do app bật vẫn còn file log để xem, kể cả khi app đã
	// restart (lúc đó không còn process nào trong map quản lý).
	for gi := range res.Groups {
		for si := range res.Groups[gi].Services {
			res.Groups[gi].Services[si].HasLog = a.mgr.HasLog(res.Groups[gi].Services[si].ID)
		}
	}
	return res
}

func (a *App) StartService(id string) error {
	s, ok := a.cfg.Service(id)
	if !ok {
		return fmt.Errorf("không tìm thấy service %q", id)
	}
	return a.mgr.Start(s, a.cfg.ResolveDir(a.root, serviceDir(s)))
}

func (a *App) StopService(id string) error {
	s, ok := a.cfg.Service(id)
	if !ok {
		return fmt.Errorf("không tìm thấy service %q", id)
	}
	return a.mgr.Stop(s, listenersOfPorts(s.Ports), a.cfg.ResolveDir(a.root, serviceDir(s)))
}

// RestartService tắt service rồi bật lại. Dùng cho service kẹt trong lúc
// khởi động (process còn sống nhưng chưa lên cổng — lúc đó chưa có listener
// để kill, chỉ tắt được nhờ process group mà app giữ) hoặc khi cần chạy lại
// code mới.
func (a *App) RestartService(id string) error {
	s, ok := a.cfg.Service(id)
	if !ok {
		return fmt.Errorf("không tìm thấy service %q", id)
	}
	return a.mgr.Restart(s, listenersOfPorts(s.Ports), a.cfg.ResolveDir(a.root, serviceDir(s)))
}

func (a *App) GetServiceLog(id string) string {
	return a.mgr.TailLog(id, 200)
}

// KillUnknown: user kill process lạ chiếm cổng (UI phải confirm trước).
func (a *App) KillUnknown(port int, pid int) error {
	return a.mgr.KillPID(pid)
}

// StopUnknownContainer: dừng container đang giữ một cổng lạ. Cổng do container
// publish ra host thì pid lsof thấy là docker backend — kill pid đó là tắt cả
// Docker Desktop, nên phải `docker stop` đúng container.
func (a *App) StopUnknownContainer(name string) error {
	return a.mgr.StopContainer(name)
}

// RepoDetails: chi tiết git repo của service (branch/worktree/dirty + branches + worktrees).
func (a *App) RepoDetails(id string) *git.RepoDetails {
	s, ok := a.cfg.Service(id)
	if !ok {
		return &git.RepoDetails{Error: "không tìm thấy service " + id}
	}
	d, err := git.Details(scan.RepoDirPath(a.cfg, a.root, s))
	if err != nil {
		return &git.RepoDetails{Error: err.Error()}
	}
	return d
}

// SwitchBranch chuyển nhánh repo chứa service. UI phải confirm trước khi gọi —
// hàm không force, không stash, git từ chối khi conflict và lỗi trả nguyên văn.
func (a *App) SwitchBranch(id string, branch string) error {
	s, ok := a.cfg.Service(id)
	if !ok {
		return fmt.Errorf("không tìm thấy service %q", id)
	}
	dir := scan.RepoDirPath(a.cfg, a.root, s)
	if dir == "" {
		return fmt.Errorf("service %s không khai báo git repo", id)
	}
	return git.Switch(dir, branch)
}

func (a *App) Version() string {
	return a.version
}
