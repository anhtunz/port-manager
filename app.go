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
	return scan.ScanAll(a.cfg, a.root, a.mgr.IsManaged)
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

func (a *App) GetServiceLog(id string) string {
	return a.mgr.TailLog(id, 200)
}

// KillUnknown: user kill process lạ chiếm cổng (UI phải confirm trước).
func (a *App) KillUnknown(port int, pid int) error {
	return a.mgr.KillPID(pid)
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
