package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"smatec/port-manager/internal/api"
	"smatec/port-manager/internal/config"
	"smatec/port-manager/internal/daemon"
	"smatec/port-manager/internal/exitlog"
	"smatec/port-manager/internal/git"
	"smatec/port-manager/internal/health"
	"smatec/port-manager/internal/history"
	"smatec/port-manager/internal/lastlog"
	"smatec/port-manager/internal/mirror"
	"smatec/port-manager/internal/notify"
	"smatec/port-manager/internal/proc"
	"smatec/port-manager/internal/scan"
	"smatec/port-manager/internal/supervisor"
	"smatec/port-manager/internal/tray"
)

// App là struct expose cho frontend qua Wails bindings (window.go.main.App.*).
// Hợp đồng API chi tiết ở docs/plans/FEAT_*.md mục 4.
type App struct {
	ctx context.Context

	// mu bảo vệ cfg + root: bindings và supervisor/stuck-watcher chạy ở
	// goroutine riêng, ReloadConfig thay cả hai nên phải có khoá.
	mu   sync.RWMutex
	cfg  *config.Config
	root string

	// configPath: file ports.yaml để GHI (SaveService/SetAutoRestart/
	// HideUnknownPort). Rỗng → chỉ có bản nhúng, không có file để ghi.
	configPath string
	logDir     string // logs/<id>.log — dùng bởi lastlog + proc.Manager
	stateDir   string // state file + lịch sử chạy (JSONL)

	mgr     *proc.Manager
	hist    *history.Store
	sup     *supervisor.Supervisor
	version string

	// crashAt chống ghi/thông báo crash TRÙNG: proc observer phát "crash" ngay
	// khi process chết, supervisor tick cũng phát "crash" sau DeadGrace — cùng
	// một lần chết chỉ được báo một lần.
	crashMu sync.Mutex
	crashAt map[string]time.Time

	// stuckNotified: mỗi lần service kẹt (status chuyển sang "stuck") chỉ báo
	// một lần; thoát khỏi trạng thái kẹt thì reset.
	stuckMu       sync.Mutex
	stuckNotified map[string]bool

	// Chạy ngầm (plan FEAT 2026-10-25):
	//   - lock: khoá một-instance, nhả ở shutdown/signal (nil nếu không lấy được
	//     mà vẫn cho chạy — lỗi quyền file không được làm app vô dụng).
	//   - xlog: ghi phiên chạy + lý do thoát.
	//   - lastExit: kết luận về lần thoát trước, tính MỘT lần lúc khởi động
	//     (BeginSession ghi đè dấu phiên cũ nên không tính lại được).
	lock     *daemon.Lock
	xlog     *exitlog.Store
	lastExit api.LastExitInfo

	// endOnce: EndSession + nhả khoá chỉ chạy một lần dù shutdown và signal
	// handler cùng tới (⌘Q rồi bị SIGTERM trong lúc dọn).
	endOnce sync.Once
}

func NewApp(version string) (*App, error) {
	c, err := loadCore()
	if err != nil {
		return nil, err
	}
	return &App{
		cfg:           c.cfg,
		root:          c.root,
		configPath:    c.configPath,
		logDir:        c.logDir,
		stateDir:      c.stateDir,
		mgr:           c.mgr,
		hist:          history.New(c.stateDir),
		xlog:          exitlog.New(c.stateDir),
		lastExit:      api.LastExitInfo{Kind: exitlog.KindNone},
		version:       version,
		crashAt:       map[string]time.Time{},
		stuckNotified: map[string]bool{},
	}, nil
}

// current trả (cfg, root) hiện tại — đọc qua khoá, an toàn với ReloadConfig.
func (a *App) current() (*config.Config, string) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg, a.root
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	// Status item menu bar (icon radar): bấm nhanh Mở app / Quét lại /
	// Bật-Tắt tất cả / Thoát, dùng được cả khi cửa sổ đã minimise. Callback
	// về trayAction ở goroutine riêng nên không chạm UI thread.
	tray.Start(a.trayAction)

	// Supervisor: giám sát service do app quản lý, tự bật lại khi crash.
	// Lookup/Starter gọi NGOÀI khoá supervisor nên được phép lấy khoá config.
	a.sup = supervisor.New(supervisor.Options{
		Policy:   supervisor.DefaultPolicy(),
		StateDir: a.stateDir,
		Lookup: func(id string) (supervisor.ServiceInfo, bool) {
			cfg, _ := a.current()
			s, ok := cfg.Service(id)
			if !ok {
				return supervisor.ServiceInfo{}, false
			}
			return supervisor.ServiceInfo{ID: s.ID, AutoRestart: s.AutoRestart}, true
		},
		Starter: supervisor.NewProcStarter(a.mgr, func(id string) (config.Service, string, bool) {
			cfg, root := a.current()
			s, ok := cfg.Service(id)
			if !ok {
				return config.Service{}, "", false
			}
			return s, cfg.ResolveDir(root, serviceDir(s)), true
		}),
		OnEvent: a.onSupervisorEvent,
	})
	a.sup.Start(ctx)

	// Observer của proc: nguồn sự kiện start/stop/restart/crash tại chỗ phát
	// sinh (App, CLI trong cùng process, supervisor bật lại đều đi qua đây).
	// Forward cho supervisor (đúng thứ tự khuyến nghị của doc internal/supervisor)
	// rồi mới ghi lịch sử / thông báo.
	a.mgr.AddObserver(a.onProcEvent)

	// Watcher "kẹt": supervisor không có poller riêng cho trạng thái sống mà
	// không lên cổng → app tự quét định kỳ và báo khi status chuyển "stuck".
	go a.watchStuck(ctx)
}

func (a *App) shutdown(ctx context.Context) {
	if a.sup != nil {
		a.sup.Stop()
	}
	a.endSession(exitlog.KindQuit, "", "")
}

// endSession ghi lý do thoát + nhả khoá một-instance, đúng một lần.
func (a *App) endSession(kind, sig, detail string) {
	a.endOnce.Do(func() {
		if a.xlog != nil {
			if err := a.xlog.EndSession(kind, sig, detail); err != nil {
				fmt.Fprintln(os.Stderr, "ghi lý do thoát lỗi:", err)
			}
		}
		if a.lock != nil {
			_ = a.lock.Release()
		}
	})
}

// watchSignals: SIGTERM/SIGINT/SIGHUP → ghi lý do thoát rồi thoát mã 0 (mã 0
// để LaunchAgent không coi là crash — người/launchd chủ ý dừng thì không mở
// lại). SIGUSR1 là tín hiệu "hiện cửa sổ" từ instance thứ hai (daemon.NotifyRunning).
//
// Vì sao không để Go xử lý mặc định: mặc định SIGTERM giết process ngay, không
// chạy OnShutdown → không có dấu vết → lần sau bị kết luận nhầm là "thoát đột
// ngột" (kill -9).
func (a *App) watchSignals() {
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGUSR1)
	go func() {
		for sig := range ch {
			if sig == syscall.SIGUSR1 {
				a.showWindow()
				continue
			}
			if a.sup != nil {
				a.sup.Stop()
			}
			a.xlogSignal(sig)
			os.Exit(0)
		}
	}()
}

func (a *App) xlogSignal(sig os.Signal) {
	a.endOnce.Do(func() {
		if a.xlog != nil {
			_ = a.xlog.RecordSignal(sig)
		}
		if a.lock != nil {
			_ = a.lock.Release()
		}
	})
}

// showWindow hiện lại cửa sổ sau khi app bị ẩn (nút đỏ với HideWindowOnClose,
// tray "Mở", SIGUSR1). runtime.Show bỏ ẩn cả app ([NSApp unhide]), rồi bỏ
// minimise + đưa cửa sổ lên trước.
func (a *App) showWindow() {
	if a.ctx == nil {
		return
	}
	runtime.Show(a.ctx)
	runtime.WindowUnminimise(a.ctx)
	runtime.WindowShow(a.ctx)
}

// onProcEvent xử lý sự kiện từ internal/proc: forward cho supervisor
// (HandleEvent quyết định bật lại) + ghi lịch sử + thông báo crash.
// Crash đi qua recordCrashOnce — proc observer và supervisor tick có thể cùng
// báo một lần chết, lịch sử chỉ được ghi một lần.
func (a *App) onProcEvent(id, kind, detail string) {
	if a.sup != nil {
		a.sup.HandleEvent(id, kind, detail)
	}
	if kind == proc.KindCrash {
		a.recordCrashOnce(id, detail)
		return
	}
	a.hist.RecordOK(id, kind, detail)
}

// onSupervisorEvent xử lý sự kiện do supervisor phát (crash phát hiện qua
// state file — service của lần chạy app trước, hoặc "fail" hết lượt bật lại).
func (a *App) onSupervisorEvent(id, kind, detail string) {
	switch kind {
	case supervisor.KindCrash:
		a.recordCrashOnce(id, detail)
	case supervisor.KindFail:
		a.hist.RecordOK(id, kind, detail)
		notify.Notify(a.ctx, "Không tự bật lại được: "+a.serviceName(id), detail)
	default:
		a.hist.RecordOK(id, kind, detail)
	}
}

// recordCrashOnce: chống ghi/thông báo crash TRÙNG — cùng một lần chết (proc
// observer phát ngay khi process chết + supervisor tick phát sau DeadGrace)
// chỉ được ghi lịch sử và thông báo MỘT lần trong vòng 1 phút.
func (a *App) recordCrashOnce(id, detail string) {
	a.crashMu.Lock()
	if t, ok := a.crashAt[id]; ok && time.Since(t) < time.Minute {
		a.crashMu.Unlock()
		return
	}
	a.crashAt[id] = time.Now()
	a.crashMu.Unlock()
	a.hist.RecordOK(id, proc.KindCrash, detail)
	notify.NotifyCrash(a.ctx, id, a.serviceName(id), detail)
}

func (a *App) serviceName(id string) string {
	cfg, _ := a.current()
	if s, ok := cfg.Service(id); ok {
		return s.Name
	}
	return id
}

// watchStuck quét định kỳ; service do app bật mà status chuyển sang "stuck"
// (process còn sống nhưng quá scan.StartingWindow vẫn chưa lên cổng) → ghi
// lịch sử + thông báo, đúng một lần cho mỗi lần kẹt.
func (a *App) watchStuck(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.checkStuck(ctx)
		}
	}
}

func (a *App) checkStuck(ctx context.Context) {
	cfg, root := a.current()
	res := scan.ScanAll(cfg, root, a.mgr)
	for _, g := range res.Groups {
		for _, sv := range g.Services {
			if sv.Status != "stuck" {
				a.setStuckNotified(sv.ID, false)
				continue
			}
			if a.getStuckNotified(sv.ID) {
				continue
			}
			a.setStuckNotified(sv.ID, true)
			a.hist.RecordOK(sv.ID, history.KindStuck,
				"process còn sống nhưng chưa lên cổng sau ~90s")
			notify.NotifyStuck(ctx, sv.ID, sv.Name, "")
		}
	}
}

func (a *App) setStuckNotified(id string, v bool) {
	a.stuckMu.Lock()
	if v {
		a.stuckNotified[id] = true
	} else {
		delete(a.stuckNotified, id)
	}
	a.stuckMu.Unlock()
}

func (a *App) getStuckNotified(id string) bool {
	a.stuckMu.Lock()
	defer a.stuckMu.Unlock()
	return a.stuckNotified[id]
}

// ---------------------------------------------------------------------------
// Quét + log
// ---------------------------------------------------------------------------

func (a *App) Scan() *scan.Result {
	cfg, root := a.current()
	res := scan.ScanAll(cfg, root, a.mgr)
	// Service từng do app bật vẫn còn file log để xem, kể cả khi app đã
	// restart (lúc đó không còn process nào trong map quản lý). lastlog.Reader
	// tự cache 10s/file nên gọi mỗi vòng quét không làm nóng đĩa.
	for gi := range res.Groups {
		for si := range res.Groups[gi].Services {
			sv := &res.Groups[gi].Services[si]
			sv.HasLog = a.mgr.HasLog(sv.ID)
			sv.LastError = lastlog.LastError(a.mgr.LogPath(sv.ID))
			// Giá trị cấu hình của service (repo_dir + health_*) — form Sửa
			// service cần để điền sẵn; không điền thì user lưu form là mất key.
			if s, ok := cfg.Service(sv.ID); ok {
				sv.RepoDir = s.RepoDir
				sv.HealthPath = s.HealthPath
				sv.HealthSkipPorts = s.HealthSkipPorts
			}
		}
	}
	return res
}

func (a *App) GetServiceLog(id string) string {
	return a.mgr.TailLog(id, 200)
}

// CopyText đưa chuỗi vào clipboard hệ thống. Frontend dùng cho nút Copy ở
// modal log: navigator.clipboard trong WKWebView của wails không ổn định
// (scheme wails:// không chắc là secure context), clipboard native luôn được.
func (a *App) CopyText(text string) error {
	return runtime.ClipboardSetText(a.ctx, text)
}

// ---------------------------------------------------------------------------
// Bật / tắt / restart từng service (hành vi cũ — giữ nguyên)
// ---------------------------------------------------------------------------

func (a *App) StartService(id string) error {
	cfg, root := a.current()
	s, ok := cfg.Service(id)
	if !ok {
		return fmt.Errorf("không tìm thấy service %q", id)
	}
	return a.mgr.Start(s, cfg.ResolveDir(root, serviceDir(s)))
}

func (a *App) StopService(id string) error {
	cfg, root := a.current()
	s, ok := cfg.Service(id)
	if !ok {
		return fmt.Errorf("không tìm thấy service %q", id)
	}
	return a.mgr.Stop(s, listenersOfPorts(s.Ports), cfg.ResolveDir(root, serviceDir(s)))
}

// RestartService tắt service rồi bật lại. Dùng cho service kẹt trong lúc
// khởi động (process còn sống nhưng chưa lên cổng — lúc đó chưa có listener
// để kill, chỉ tắt được nhờ process group mà app giữ) hoặc khi cần chạy lại
// code mới.
func (a *App) RestartService(id string) error {
	cfg, root := a.current()
	s, ok := cfg.Service(id)
	if !ok {
		return fmt.Errorf("không tìm thấy service %q", id)
	}
	return a.mgr.Restart(s, listenersOfPorts(s.Ports), cfg.ResolveDir(root, serviceDir(s)))
}

// ---------------------------------------------------------------------------
// Bật / tắt theo group + toàn workspace
// ---------------------------------------------------------------------------

// portWaitTimeout: sau khi bật một service, chờ cổng lên tối đa chừng này.
// Hết thời gian chờ KHÔNG tính là lỗi (service có thể lên sau) — chỉ không
// chờ tiếp để chuyển sang service kế.
const portWaitTimeout = 20 * time.Second

// svcOps gom thao tác theo group/all — dùng chung cho App (GUI) và CLI.
type svcOps struct {
	cfg  *config.Config
	root string
	mgr  *proc.Manager
}

func (a *App) ops() svcOps {
	cfg, root := a.current()
	return svcOps{cfg: cfg, root: root, mgr: a.mgr}
}

func (o svcOps) group(name string) (config.Group, bool) {
	for _, g := range o.cfg.Groups {
		if strings.EqualFold(strings.TrimSpace(g.Name), strings.TrimSpace(name)) {
			return g, true
		}
	}
	return config.Group{}, false
}

// startService bật 1 service rồi chờ cổng lên (tối đa portWaitTimeout).
// Service đã chạy (proc.ErrAlreadyRunning) → bỏ qua, KHÔNG tính lỗi.
func (o svcOps) startService(s config.Service) error {
	dir := o.cfg.ResolveDir(o.root, serviceDir(s))
	err := o.mgr.Start(s, dir)
	if errors.Is(err, proc.ErrAlreadyRunning) {
		return nil
	}
	if err != nil {
		return err
	}
	waitPortsUp(s.Ports, portWaitTimeout)
	return nil
}

// stopService tắt 1 service. Service đã tắt trước đó → bỏ qua (không tính
// lỗi) — StopAll/StopGroup không được báo lỗi chỉ vì service chưa chạy.
func (o svcOps) stopService(s config.Service) error {
	infos := o.mgr.ManagedInfos()
	if mi, ok := infos[s.ID]; !ok || !mi.Alive {
		if len(listenersOfPorts(s.Ports)) == 0 {
			return nil
		}
	}
	dir := o.cfg.ResolveDir(o.root, serviceDir(s))
	return o.mgr.Stop(s, listenersOfPorts(s.Ports), dir)
}

// startGroup bật lần lượt các service của group. Service lỗi KHÔNG chặn các
// service sau — lỗi gom lại trả một lần.
func (o svcOps) startGroup(g config.Group) error {
	var errs []string
	for _, s := range g.Services {
		if err := o.startService(s); err != nil {
			errs = append(errs, s.ID+": "+firstLine(err.Error()))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("group %s: %d/%d service lỗi — %s",
			g.Name, len(errs), len(g.Services), strings.Join(errs, "; "))
	}
	return nil
}

// stopGroup tắt các service của group theo THỨ TỰ NGƯỢC.
func (o svcOps) stopGroup(g config.Group) error {
	var errs []string
	for i := len(g.Services) - 1; i >= 0; i-- {
		s := g.Services[i]
		if err := o.stopService(s); err != nil {
			errs = append(errs, s.ID+": "+firstLine(err.Error()))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("group %s: %d/%d service lỗi — %s",
			g.Name, len(errs), len(g.Services), strings.Join(errs, "; "))
	}
	return nil
}

// startParallel bật song song các service của MỘT tier (plan 3.11: "song song
// trong tier" — 1 goroutine/service, mỗi service tự chờ cổng riêng trong
// timeout 20s; tổng thời gian một lượt ≈ chậm nhất của một service.
// Lỗi bật 1 service KHÔNG chặn service khác — gom lại trả một lần.
func (o svcOps) startParallel(svcs []config.Service) error {
	if len(svcs) == 0 {
		return nil
	}
	var wg sync.WaitGroup
	errCh := make(chan string, len(svcs))
	for _, s := range svcs {
		wg.Add(1)
		go func(s config.Service) {
			defer wg.Done()
			if err := o.startService(s); err != nil {
				errCh <- s.ID + ": " + firstLine(err.Error())
			}
		}(s)
	}
	wg.Wait()
	close(errCh)
	var errs []string
	for msg := range errCh {
		errs = append(errs, msg)
	}
	if len(errs) > 0 {
		return fmt.Errorf("batch %d service: %d lỗi — %s",
			len(svcs), len(errs), strings.Join(errs, " · "))
	}
	return nil
}

// startAll bật toàn bộ workspace theo thứ tự hạ tầng → Backend Go → Frontend
// (GroupsInStartupOrder xếp theo tier; group thiếu tier xếp sau, giữ thứ tự
// file). Các group LIÊN TIẾP cùng tier (tier >= 0) được gộp thành một lượt để
// bật song song trong tier (plan 3.11); group không khai báo tier (-1) vẫn
// chạy tuần tự từng group để không phá cấu trúc file cũ.
func (o svcOps) startAll() error {
	groups := o.cfg.GroupsInStartupOrder()
	var errs []string
	for i := 0; i < len(groups); i++ {
		next := i + 1
		for next < len(groups) && groups[i].Tier >= 0 && groups[next].Tier == groups[i].Tier {
			next++
		}
		var svcs []config.Service
		for _, g := range groups[i:next] {
			svcs = append(svcs, g.Services...)
		}
		if err := o.startParallel(svcs); err != nil {
			errs = append(errs, err.Error())
		}
		i = next - 1
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, " · "))
	}
	return nil
}

// stopAll tắt toàn bộ workspace NGƯỢC thứ tự bật.
func (o svcOps) stopAll() error {
	groups := o.cfg.GroupsInStartupOrder()
	var errs []string
	for i := len(groups) - 1; i >= 0; i-- {
		if err := o.stopGroup(groups[i]); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, " · "))
	}
	return nil
}

func (a *App) StartGroup(group string) error {
	o := a.ops()
	g, ok := o.group(group)
	if !ok {
		return fmt.Errorf("không tìm thấy group %q", group)
	}
	return o.startGroup(g)
}

func (a *App) StopGroup(group string) error {
	o := a.ops()
	g, ok := o.group(group)
	if !ok {
		return fmt.Errorf("không tìm thấy group %q", group)
	}
	return o.stopGroup(g)
}

func (a *App) StartAll() error {
	o := a.ops()
	return o.startAll()
}

func (a *App) StopAll() error {
	o := a.ops()
	return o.stopAll()
}

// waitPortsUp chờ ít nhất MỘT cổng trong danh sách có listener. Hết hạn →
// trả false, KHÔNG lỗi.
func waitPortsUp(ports []int, timeout time.Duration) bool {
	want := map[int]bool{}
	for _, p := range ports {
		want[p] = true
	}
	if len(want) == 0 {
		return true
	}
	deadline := time.Now().Add(timeout)
	for {
		if ls, err := scan.Listeners(); err == nil {
			for _, l := range ls {
				if want[l.Port] {
					return true
				}
			}
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

func intIn(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Cấu hình: lưu service / auto_restart / ẩn cổng lạ / nạp lại
// ---------------------------------------------------------------------------

// errNoConfigFile: app chạy mà không tìm thấy file ports.yaml nào để ghi
// (chỉ có bản nhúng trong binary) → các thao tác ghi cấu hình bị chặn.
var errNoConfigFile = errors.New("không tìm thấy ports.yaml để ghi")

// SaveService thêm mới hoặc sửa một service trong ports.yaml (giữ nguyên định
// dạng + comment — edit.go sửa theo toạ độ dòng), rồi nạp lại config.
func (a *App) SaveService(spec api.ServiceSpec) error {
	if a.configPath == "" {
		return errNoConfigFile
	}
	if err := spec.Validate(); err != nil {
		return err
	}
	if err := config.UpsertService(a.configPath, spec); err != nil {
		return err
	}
	if err := a.ReloadConfig(); err != nil {
		return err
	}
	a.hist.RecordOK(spec.ID, history.KindEdit, "lưu cấu hình service từ app")
	return nil
}

// SetAutoRestart bật/tắt tự khởi động lại cho một service: ghi auto_restart
// vào ports.yaml, nạp lại config và áp dụng ngay cho supervisor.
func (a *App) SetAutoRestart(id string, on bool) error {
	cfg, _ := a.current()
	if _, ok := cfg.Service(id); !ok {
		return fmt.Errorf("không tìm thấy service %q", id)
	}
	if a.configPath == "" {
		return errNoConfigFile
	}
	if err := config.SetAutoRestart(a.configPath, id, on); err != nil {
		return err
	}
	if err := a.ReloadConfig(); err != nil {
		return err
	}
	// Áp dụng ngay cho supervisor, không phải chờ vòng tick sau.
	if a.sup != nil {
		_ = a.sup.SetAutoRestart(id, on)
	}
	a.hist.RecordOK(id, history.KindEdit, fmt.Sprintf("auto_restart = %v", on))
	return nil
}

// HideUnknownPort thêm cổng vào unknown_ignore_ports trong ports.yaml (giữ
// nguyên định dạng file) rồi nạp lại config — mục "Cổng lạ" ẩn ngay.
func (a *App) HideUnknownPort(port int) error {
	if a.configPath == "" {
		return errNoConfigFile
	}
	if err := config.AddIgnorePort(a.configPath, port); err != nil {
		return err
	}
	return a.ReloadConfig()
}

// ReloadConfig nạp lại ports.yaml (sau khi sửa) và thay config đang chạy.
func (a *App) ReloadConfig() error {
	var (
		cfg    *config.Config
		cfgDir string
		err    error
	)
	if a.configPath != "" {
		cfg, cfgDir, err = config.Load(a.configPath)
	} else {
		// Không có file → nạp lại bản nhúng (vẫn phải hoạt động, chỉ không ghi được).
		cfg, err = config.Parse(embeddedPortsYAML)
		cfgDir, _ = os.Getwd()
	}
	if err != nil {
		return err
	}
	root := cfg.ResolveRoot(cfgDir)
	a.mu.Lock()
	a.cfg = cfg
	a.root = root
	a.mu.Unlock()
	// Config trong file là nguồn sự thật cho auto_restart.
	if a.sup != nil {
		a.sup.SyncAutoRestart(autoRestartMap(cfg))
	}
	return nil
}

func autoRestartMap(cfg *config.Config) map[string]bool {
	m := map[string]bool{}
	for _, g := range cfg.Groups {
		for _, s := range g.Services {
			m[s.ID] = s.AutoRestart
		}
	}
	return m
}

// ---------------------------------------------------------------------------
// Mirror / health / lịch sử / gợi ý cổng / mở Terminal
// ---------------------------------------------------------------------------

// MirrorReport đối chiếu bảng quy hoạch port ở AGENTS.md mục 10.1 (nguồn sự
// thật) với ports.yaml (bản mirror) — trả danh sách cổng lệch.
func (a *App) MirrorReport() api.MirrorReport {
	cfg, root := a.current()
	tbl, err := mirror.ParseFile(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		// Không đọc được AGENTS.md → báo rỗng nhưng nói rõ lý do (note) để UI
		// hiện, không phải bảng "0 lệch" gây hiểu nhầm là mọi thứ đều ổn.
		return api.MirrorReport{
			Mismatches: []api.Mismatch{},
			CheckedAt:  time.Now().Format(time.RFC3339),
			Note:       "Không đọc được AGENTS.md: " + firstLine(err.Error()),
		}
	}
	rep := mirror.CompareWithOwners(tbl, cfg.ConfiguredPorts(), ownersOf(cfg))
	if rep.Mismatches == nil {
		rep.Mismatches = []api.Mismatch{}
	}
	return rep
}

// ownersOf: port → service/group sở hữu (để ghi chú lệch cho dễ đọc).
func ownersOf(cfg *config.Config) mirror.Owners {
	owners := mirror.Owners{}
	for _, g := range cfg.Groups {
		for _, s := range g.Services {
			for _, p := range s.Ports {
				if _, ok := owners[p]; !ok {
					owners[p] = mirror.Owner{Service: s.ID, Group: g.Name}
				}
			}
		}
	}
	return owners
}

// HealthCheckAll probe HTTP từng cổng ĐANG MỞ của mọi service (127.0.0.1).
// Cổng gRPC nằm trong health_skip_ports và cổng của service compose → state
// "skipped", không probe giả tạo http_error.
func (a *App) HealthCheckAll() []api.HealthEntry {
	cfg, root := a.current()
	res := scan.ScanAll(cfg, root, a.mgr)
	var targets []health.Target
	for _, g := range res.Groups {
		for _, sv := range g.Services {
			svc, ok := cfg.Service(sv.ID)
			if !ok {
				continue
			}
			for _, p := range sv.Ports {
				if !p.Open {
					continue // chỉ probe cổng đang mở
				}
				skip := svc.IsCompose()
				reason := ""
				if skip {
					reason = "service compose (docker)"
				}
				if intIn(svc.HealthSkipPorts, p.Port) {
					skip = true
					reason = "cổng nằm trong health_skip_ports"
				}
				targets = append(targets, health.Target{
					ID:     sv.ID,
					Port:   p.Port,
					Path:   svc.HealthPath,
					Skip:   skip,
					Reason: reason,
				})
			}
		}
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	entries := health.ProbeAll(ctx, targets, 2*time.Second)
	if entries == nil {
		entries = []health.HealthEntry{}
	}
	return entries
}

// History trả lịch sử chạy của một service (mới nhất trước).
func (a *App) History(id string) []api.HistoryEvent {
	return a.hist.List(id, history.DefaultLimit)
}

// SuggestPort gợi ý một cổng ĐANG TRỐNG, lấy từ bảng quy hoạch AGENTS.md
// (không bày cổng ngoài quy hoạch). Hết ứng viên → 0.
func (a *App) SuggestPort() int {
	cfg, root := a.current()
	tbl, err := mirror.ParseFile(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		return 0
	}
	used := cfg.ConfiguredPorts()
	if ls, err := scan.Listeners(); err == nil {
		for _, l := range ls {
			used[l.Port] = true
		}
	}
	return mirror.SuggestPort(tbl, used)
}

// OpenTerminal mở Terminal.app tại thư mục được truyền vào. Chấp nhận: đường
// dẫn tuyệt đối/tương đối, hoặc id service (resolve về thư mục làm việc của
// service đó) — frontend thuận tiện gọi theo id.
func (a *App) OpenTerminal(dir string) error {
	target := strings.TrimSpace(dir)
	if target == "" {
		return errors.New("thiếu thư mục cần mở")
	}
	cfg, root := a.current()
	if s, ok := cfg.Service(target); ok {
		// Là id service → thư mục làm việc của service.
		target = cfg.ResolveDir(root, serviceDir(s))
	} else if !filepath.IsAbs(target) {
		if p, err := filepath.Abs(target); err == nil {
			target = p
		}
	}
	st, err := os.Stat(target)
	if err != nil || !st.IsDir() {
		return fmt.Errorf("thư mục không tồn tại: %s", target)
	}
	// AppleScriptString escape an toàn (dấu nháy, backslash, newline).
	script := fmt.Sprintf("tell application \"Terminal\"\ndo script %s\nactivate\nend tell",
		notify.AppleScriptString("cd "+target))
	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("mở Terminal thất bại: %s", msg)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Git + cổng lạ + tiện ích (hành vi cũ — giữ nguyên)
// ---------------------------------------------------------------------------

// OpenURL mở URL bằng trình duyệt mặc định của hệ điều hành. Frontend gọi khi
// user bấm vào chip cổng (http://localhost:<port>).
//
// Chỉ cho phép http/https — không mở scheme khác (file:, javascript:) để không
// mở nhầm nội dung ngoài ý muốn.
func (a *App) OpenURL(rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	u, err := parseHTTPURL(rawURL)
	if err != nil {
		return err
	}
	if a.ctx == nil {
		return fmt.Errorf("app chưa khởi động xong")
	}
	runtime.BrowserOpenURL(a.ctx, u)
	return nil
}

// parseHTTPURL kiểm tra URL chỉ nhận scheme http/https (tránh mở scheme lạ
// kiểu file:, javascript:) và trả URL đã chuẩn hoá.
func parseHTTPURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("thiếu URL")
	}
	lower := strings.ToLower(raw)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		// Chấp nhận "localhost:3000" / "127.0.0.1:3000" không có scheme → mặc định http.
		if strings.Contains(lower, "://") {
			return "", fmt.Errorf("URL phải là http/https: %s", raw)
		}
		raw = "http://" + raw
	}
	return raw, nil
}

// KillUnknown: user kill process lạ chiếm cổng (UI phải confirm trước).
// Kiểm tra lại pid vẫn còn lắng nghe ĐÚNG cổng này trước khi kill: UI có thể
// đang giữ dữ liệu của vòng quét trước — pid/cổng đã đổi thì kill nhầm process
// của người khác.
func (a *App) KillUnknown(port int, pid int) error {
	ls, err := scan.Listeners()
	if err != nil {
		return fmt.Errorf("kiểm tra listener: %w", err)
	}
	for _, l := range ls {
		if l.PID == pid && l.Port == port {
			return a.mgr.KillPID(pid)
		}
	}
	return fmt.Errorf("process %d không còn lắng nghe cổng %d — không kill để tránh nhầm process khác", pid, port)
}

// StopUnknownContainer: dừng container đang giữ một cổng lạ. Cổng do container
// publish ra host thì pid lsof thấy là docker backend — kill pid đó là tắt cả
// Docker Desktop, nên phải `docker stop` đúng container.
func (a *App) StopUnknownContainer(name string) error {
	return a.mgr.StopContainer(name)
}

// RepoDetails: chi tiết git repo của service (branch/worktree/dirty + branches + worktrees).
func (a *App) RepoDetails(id string) *git.RepoDetails {
	cfg, root := a.current()
	s, ok := cfg.Service(id)
	if !ok {
		return &git.RepoDetails{Error: "không tìm thấy service " + id}
	}
	d, err := git.Details(scan.RepoDirPath(cfg, root, s))
	if err != nil {
		return &git.RepoDetails{Error: err.Error()}
	}
	return d
}

// SwitchBranch chuyển nhánh repo chứa service. UI phải confirm trước khi gọi —
// hàm không force, không stash, git từ chối khi conflict và lỗi trả nguyên văn.
func (a *App) SwitchBranch(id string, branch string) error {
	cfg, root := a.current()
	s, ok := cfg.Service(id)
	if !ok {
		return fmt.Errorf("không tìm thấy service %q", id)
	}
	dir := scan.RepoDirPath(cfg, root, s)
	if dir == "" {
		return fmt.Errorf("service %s không khai báo git repo", id)
	}
	return git.Switch(dir, branch)
}

func (a *App) Version() string {
	return a.version
}

// ---------------------------------------------------------------------------
// Menu ứng dụng (wails v2 menu — main.go dựng menu, callback gọi vào đây).
// Callback của menu KHÔNG có ctx → dùng a.ctx. Hành động chạy nền để không
// khoá UI thread; sau khi xong phát event "menu:action" cho frontend refresh
// (frontend chưa nghe event thì cũng không sao — hành động vẫn đã chạy).
// ---------------------------------------------------------------------------

func (a *App) menuAction(name string) {
	switch name {
	case "refresh":
		a.emitMenu("refresh")
	case "start-all":
		go func() {
			if !a.confirmDialog("Bật tất cả",
				"Bật các service của workspace theo thứ tự hạ tầng → Backend → Frontend?") {
				return
			}
			_ = a.StartAll()
			a.emitMenu("start-all")
		}()
	case "stop-all":
		go func() {
			if !a.confirmDialog("Tắt tất cả",
				"Tắt toàn bộ service của workspace (thứ tự ngược thứ tự bật)?") {
				return
			}
			_ = a.StopAll()
			a.emitMenu("stop-all")
		}()
	case "about":
		a.showAbout()
	case "agent":
		// Mở modal Chạy ngầm trên frontend (frontend nghe menu:action "agent").
		a.showWindow()
		a.emitMenu("agent")
	case "agent-install":
		go func() {
			if !a.confirmDialog("Cài chạy ngầm",
				"Cài LaunchAgent để Port Manager tự mở khi đăng nhập và tự mở lại khi bị tắt đột ngột?\n\nApp sẽ thoát và tự mở lại ngầm trong vài giây.") {
				return
			}
			if err := a.AgentInstall(); err != nil {
				a.infoDialog("Cài chạy ngầm lỗi", err.Error())
			}
		}()
	case "agent-uninstall":
		go func() {
			if !a.confirmDialog("Gỡ chạy ngầm",
				"Gỡ LaunchAgent? App sẽ không tự mở khi đăng nhập / không tự mở lại khi bị tắt nữa.") {
				return
			}
			if err := a.AgentUninstall(); err != nil {
				a.infoDialog("Gỡ chạy ngầm lỗi", err.Error())
				return
			}
			a.emitMenu("agent")
		}()
	case "quit":
		if a.ctx != nil {
			runtime.Quit(a.ctx)
		}
	}
}

// ---------------------------------------------------------------------------
// Chạy ngầm — bindings (plan FEAT 2026-10-25 §3).
// ---------------------------------------------------------------------------

// AgentStatus trả trạng thái LaunchAgent. Không bao giờ trả lỗi: "chưa cài"
// hay "launchctl không trả lời" đều là trạng thái, không phải sự cố.
func (a *App) AgentStatus() api.AgentStatus {
	return daemon.Status()
}

// AgentInstall cài LaunchAgent rồi BÀN GIAO: ghi file + lên lịch bootstrap
// nền, sau đó app tự thoát (mã 0) để launchd mở instance mới. Trả về trước
// khi thoát để frontend kịp hiện thông điệp.
func (a *App) AgentInstall() error {
	if err := daemon.Install(daemon.InstallOptions{Handover: true, PATH: proc.SmartPath()}); err != nil {
		return err
	}
	a.quitLater(1500 * time.Millisecond)
	return nil
}

// AgentUninstall gỡ LaunchAgent. Nếu chính process này là agent, bootout sẽ
// SIGTERM nó — watchSignals ghi lý do rồi thoát; frontend đã cảnh báo trước.
func (a *App) AgentUninstall() error {
	return daemon.Uninstall()
}

// LastExit trả kết luận về lần thoát trước (tính một lần lúc khởi động).
func (a *App) LastExit() api.LastExitInfo {
	return a.lastExit
}

// quitLater thoát app sau một khoảng ngắn — để lời gọi binding kịp trả về
// và frontend kịp vẽ thông điệp trước khi cửa sổ biến mất.
func (a *App) quitLater(d time.Duration) {
	go func() {
		time.Sleep(d)
		if a.ctx != nil {
			runtime.Quit(a.ctx)
		}
	}()
}

// infoDialog: hộp thoại native thông báo (dùng khi hành động từ menu lỗi —
// menu không có frontend để hiện lỗi).
func (a *App) infoDialog(title, message string) {
	if a.ctx == nil {
		return
	}
	_, _ = runtime.MessageDialog(a.ctx, runtime.MessageDialogOptions{
		Type:    runtime.InfoDialog,
		Title:   title,
		Message: message,
	})
}

// confirmDialog: hộp thoại xác nhận native (dùng được cả khi webview treo —
// lý do menu tồn tại). Lỗi dialog (hiếm) → coi như xác nhận để không mất đường
// tắt-all rescue khi cửa sổ đã hỏng.
func (a *App) confirmDialog(title, message string) bool {
	if a.ctx == nil {
		return false
	}
	res, err := runtime.MessageDialog(a.ctx, runtime.MessageDialogOptions{
		Type:    runtime.QuestionDialog,
		Title:   title,
		Message: message,
	})
	if err != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(res), "Yes")
}

func (a *App) emitMenu(name string) {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "menu:action", name)
	}
}

// trayAction: callback từ status item menu bar (internal/tray). "open"/"quit"
// là hành động riêng của tray; các key còn lại ("refresh", "start-all",
// "stop-all", "about") tái dùng menuAction để hành vi + hộp thoại confirm
// giống hệt menu ứng dụng. Chạy ở goroutine riêng (tray không bao giờ gọi
// trực tiếp trên main thread).
func (a *App) trayAction(action string) {
	switch action {
	case "open":
		a.showWindow()
	case "quit":
		if a.ctx != nil {
			runtime.Quit(a.ctx)
		}
	default:
		a.menuAction(action)
	}
}

// showAbout: hộp thoại native "Giới thiệu" kèm phiên bản.
func (a *App) showAbout() {
	if a.ctx == nil {
		return
	}
	_, _ = runtime.MessageDialog(a.ctx, runtime.MessageDialogOptions{
		Type:    runtime.InfoDialog,
		Title:   "Giới thiệu",
		Message: fmt.Sprintf("SMATEC Port Manager\nPhiên bản %s", a.version),
	})
}
