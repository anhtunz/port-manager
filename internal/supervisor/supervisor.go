// Package supervisor giám sát các service DO APP QUẢN LÝ và tự bật lại khi
// process chết bất thường (crash).
//
// # Nguyên tắc bất di bất dịch
//
//   - Chỉ can thiệp với process app đã bật. Nguồn sự thật: state file
//     ~/.port-manager/state/<id>.proc.json (ghi pgid của process group) + kiểm
//     tra process group còn sống hay không. Service user tự chạy tay KHÔNG có
//     state file → supervisor không bao giờ đụng tới.
//   - Service user vừa TẮT có chủ ý không được bật lại. App phải báo trước
//     khi tắt (ExpectStop) hoặc báo qua event "stop"/"restart" của proc —
//     giống cờ expectedStop mà internal/proc đặt trước khi kill. Lần tắt đến
//     từ process khác (CLI) thì không có event nào cả — tín hiệu là file
//     marker ~/.port-manager/state/<id>.stopping mà internal/proc ghi trước
//     khi kill; tick thấy marker còn mới thì coi như user tắt có chủ ý.
//   - Không thử lại vô hạn: Policy.MaxRestarts giới hạn số lần thử bật lại
//     trong một chu kỳ. Hết lượt → event "fail" và supervisor ngừng thử cho
//     service đó tới khi service chạy ổn định trở lại.
//   - Số lần thử nằm trong RAM (không ghi vào state file) → app đóng là mất,
//     đúng nguyên tắc "supervisor là goroutine trong tiến trình app, tự chết
//     khi app đóng" (không có tiến trình nền / launchd).
//
// # Luồng
//
// Supervisor chạy vòng lặp theo Policy.PollInterval:
//
//  1. Đọc thư mục state → biết service nào đang do app quản lý.
//  2. Kiểm tra process group của từng service: còn sống / đã chết.
//  3. Process đã chết mà không có dấu hiệu user tắt có chủ ý → coi là
//     crash → gọi hàm quyết định thuần tuý Decide() → nếu được phép thì lên
//     lịch bật lại sau thời gian chờ (backoff).
//  4. Bật lại: gọi Starter do App cấp (mặc định là proc.Manager.Start) → giữ
//     nguyên mọi nguyên tắc đã có: process group riêng, state file
//     ~/.port-manager/state/<id>.proc.json, log append ở logs/<id>.log, stdin
//     của cây process con được giữ mở.
//
// # Tích hợp (app.go — người tích hợp)
//
//	sv := supervisor.New(supervisor.Options{
//	    Policy:   supervisor.DefaultPolicy(),
//	    StateDir: stateDir,                    // ~/.port-manager/state
//	    Lookup:   func(id string) (supervisor.ServiceInfo, bool) { ... },
//	    Starter:  supervisor.NewProcStarter(mgr, resolveService),
//	    OnEvent:  func(id, kind, detail string) { ... },  // lịch sử / thông báo
//	})
//	sv.Start(ctx)      // trong App.startup
//	sv.Stop()          // trong OnShutdown
//
// # Hai việc App BẮT BUỘC phải làm
//
//  1. Trước khi tắt service (App.StopService) phải gọi sv.ExpectStop(id).
//     internal/proc xoá state file ở CẢ hai trường hợp "user tắt" lẫn
//     "process crash" → không có tín hiệu này thì supervisor không phân biệt
//     được và sẽ bật lại service user vừa tắt.
//  2. Binding SetAutoRestart(id, on) nên gọi thêm sv.SetAutoRestart(id, on)
//     sau khi đã ghi ports.yaml → áp dụng ngay, không phải chờ ReloadConfig.
//
// Observer của internal/proc (nếu đã có) nối thẳng vào HandleEvent.
package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"smatec/port-manager/internal/config"
	"smatec/port-manager/internal/proc"
	"smatec/port-manager/internal/scan"
)

// ---------------------------------------------------------------------------
// Hợp đồng kind sự kiện (giống hợp đồng lịch sử chạy của app)
// ---------------------------------------------------------------------------

const (
	KindStart   = "start"   // bật thành công
	KindStop    = "stop"    // dừng có chủ ý (user bấm Tắt)
	KindRestart = "restart" // tắt rồi bật lại
	KindCrash   = "crash"   // process chết NGOÀI Ý MUỐN
	KindStuck   = "stuck"   // process còn sống nhưng không lên cổng
	KindFail    = "fail"    // hết lượt bật lại
	KindEdit    = "edit"    // người dùng sửa cấu hình
)

// ---------------------------------------------------------------------------
// Chính sách
// ---------------------------------------------------------------------------

// Mặc định của chính sách.
const (
	// DefaultMaxRestarts: số lần thử bật lại tối đa trong một chu kỳ.
	// Không bao giờ vô hạn.
	DefaultMaxRestarts = 3
	// DefaultBackoff: thời gian chờ trước lần thử bật lại đầu tiên.
	DefaultBackoff = 5 * time.Second
	// DefaultMaxBackoff: trần của thời gian chờ giữa hai lần thử.
	DefaultMaxBackoff = 60 * time.Second
	// DefaultPollInterval: chu kỳ kiểm tra service do app quản lý.
	DefaultPollInterval = 5 * time.Second
	// DefaultStableAfter: service sống liên tục bấy lâu → coi như hết chu kỳ
	// lỗi, bộ đếm thử lại được reset về 0.
	DefaultStableAfter = 5 * time.Minute
	// DefaultDeadGrace: thời gian chờ xác nhận process đã chết hẳn trước khi
	// coi là crash (tránh bắt nhầm khoảnh khắc user vừa bấm Tắt).
	DefaultDeadGrace = 10 * time.Second
)

// Policy là chính sách giám sát — cấu hình được.
type Policy struct {
	// Enabled: bật/tắt supervisor nói chung.
	Enabled bool `json:"enabled"`

	// MaxRestarts: số lần thử bật lại tối đa trong một chu kỳ. Luôn hữu hạn →
	// không bao giờ spam process. 0 → dùng DefaultMaxRestarts.
	MaxRestarts int `json:"maxRestarts"`

	// Backoff: thời gian chờ trước lần thử đầu tiên; lần sau nhân đôi.
	Backoff time.Duration `json:"backoff"`

	// MaxBackoff: trần thời gian chờ giữa hai lần thử.
	MaxBackoff time.Duration `json:"maxBackoff"`

	// PollInterval: chu kỳ kiểm tra service do app quản lý.
	PollInterval time.Duration `json:"pollInterval"`

	// StableAfter: service sống liên tục bấy lâu → reset bộ đếm thử lại
	// (một chu kỳ lỗi mới).
	StableAfter time.Duration `json:"stableAfter"`

	// DeadGrace: chờ xác nhận process đã chết hẳn trước khi coi là crash.
	DeadGrace time.Duration `json:"deadGrace"`
}

// DefaultPolicy trả chính sách mặc định.
func DefaultPolicy() Policy {
	return Policy{
		Enabled:      true,
		MaxRestarts:  DefaultMaxRestarts,
		Backoff:      DefaultBackoff,
		MaxBackoff:   DefaultMaxBackoff,
		PollInterval: DefaultPollInterval,
		StableAfter:  DefaultStableAfter,
		DeadGrace:    DefaultDeadGrace,
	}
}

// withDefaults điền các trường 0 bằng giá trị mặc định và chặn giá trị âm.
func (p Policy) withDefaults() Policy {
	d := DefaultPolicy()
	if p.MaxRestarts == 0 {
		p.MaxRestarts = d.MaxRestarts
	}
	if p.MaxRestarts < 0 {
		p.MaxRestarts = 0
	}
	if p.Backoff <= 0 {
		p.Backoff = d.Backoff
	}
	if p.MaxBackoff <= 0 {
		p.MaxBackoff = d.MaxBackoff
	}
	if p.PollInterval <= 0 {
		p.PollInterval = d.PollInterval
	}
	if p.StableAfter < 0 {
		p.StableAfter = 0
	}
	if p.DeadGrace < 0 {
		p.DeadGrace = 0
	}
	return p
}

// ---------------------------------------------------------------------------
// Hàm quyết định thuần tuý
// ---------------------------------------------------------------------------

// Action là hành động supervisor sẽ làm với 1 service.
type Action int

const (
	// ActionNone: không làm gì (không can thiệp).
	ActionNone Action = iota
	// ActionRestart: bật lại service sau thời gian chờ Decision.Delay.
	ActionRestart
	// ActionFail: hết lượt bật lại → ghi event "fail" và ngừng thử.
	ActionFail
)

func (a Action) String() string {
	switch a {
	case ActionRestart:
		return "restart"
	case ActionFail:
		return "fail"
	default:
		return "none"
	}
}

// Observation là ảnh chụp trạng thái của 1 service tại thời điểm kiểm tra.
// Đây là đầu vào duy nhất của hàm quyết định → hàm quyết định thuần tuý,
// không đụng I/O, không đụng đồng hồ → test được bằng unit test.
type Observation struct {
	// ID: id service.
	ID string

	// Managed: process này do app bật (có state file ~/.port-manager/state +
	// process group tương ứng). False = service user tự chạy tay → không
	// được đụng tới.
	Managed bool

	// Alive: process group còn sống.
	Alive bool

	// ExpectedStop: user vừa tắt service có chủ ý → không được bật lại.
	ExpectedStop bool

	// AutoRestart: cấu hình auto_restart của service (từ ports.yaml).
	AutoRestart bool
}

// Decision là kết quả của hàm quyết định.
type Decision struct {
	// Action: hành động cần làm.
	Action Action
	// Reason: lý do (tiếng Việt) — để log và ghi vào lịch sử.
	Reason string
	// Attempt: số lần thử bật lại đã thực hiện trong chu kỳ hiện tại.
	Attempt int
	// Delay: thời gian chờ trước khi bật lại (chỉ có ý nghĩa với
	// ActionRestart).
	Delay time.Duration
}

// Decide quyết định có nên bật lại service hay không.
//
// Hàm thuần tuý: cùng đầu vào → cùng kết quả. Không đọc đồng hồ, không đọc
// đĩa, không gọi hàm ngoài.
//
// attempt = số lần thử bật lại đã thực hiện trong chu kỳ hiện tại.
func Decide(obs Observation, pol Policy, attempt int) Decision {
	pol = pol.withDefaults()
	if attempt < 0 {
		attempt = 0
	}
	switch {
	case !pol.Enabled:
		return Decision{Action: ActionNone, Reason: "supervisor đang tắt", Attempt: attempt}
	case !obs.Managed:
		return Decision{Action: ActionNone, Reason: "service không do app quản lý (user tự chạy) — không đụng", Attempt: attempt}
	case obs.ExpectedStop:
		return Decision{Action: ActionNone, Reason: "user vừa tắt có chủ ý — không bật lại", Attempt: attempt}
	case !obs.AutoRestart:
		return Decision{Action: ActionNone, Reason: "service không bật auto_restart", Attempt: attempt}
	case obs.Alive:
		return Decision{Action: ActionNone, Reason: "process còn sống", Attempt: attempt}
	case attempt >= pol.MaxRestarts:
		return Decision{
			Action:  ActionFail,
			Reason:  fmt.Sprintf("đã thử bật lại %d lần — dừng, không thử vô hạn", attempt),
			Attempt: attempt,
		}
	default:
		return Decision{
			Action:  ActionRestart,
			Reason:  "process chết bất thường",
			Attempt: attempt,
			Delay:   BackoffFor(pol, attempt),
		}
	}
}

// BackoffFor trả thời gian chờ trước lần thử thứ (attempt+1).
//
// attempt=0 → Backoff; attempt=1 → 2×Backoff; ... và luôn ≤ MaxBackoff.
// Hàm thuần tuý.
func BackoffFor(pol Policy, attempt int) time.Duration {
	pol = pol.withDefaults()
	if attempt < 0 {
		attempt = 0
	}
	d := pol.Backoff
	for i := 0; i < attempt; i++ {
		if pol.MaxBackoff > 0 && d >= pol.MaxBackoff {
			return pol.MaxBackoff
		}
		d *= 2
	}
	if pol.MaxBackoff > 0 && d > pol.MaxBackoff {
		return pol.MaxBackoff
	}
	return d
}

// ---------------------------------------------------------------------------
// Kiểu phụ thuộc (App cấp vào)
// ---------------------------------------------------------------------------

// ServiceInfo là phần thông tin supervisor cần về 1 service.
type ServiceInfo struct {
	// ID: id service.
	ID string
	// AutoRestart: service có bật tự khởi động lại không (auto_restart trong
	// ports.yaml).
	AutoRestart bool
}

// Lookup trả service hiện tại theo id. App cấp (đọc config qua getter có
// khoá) — supervisor KHÔNG tự đọc internal/config và KHÔNG tự đọc file.
//
// Supervisor gọi lookup mỗi lần xử lý sự kiện → tự thấy giá trị mới sau khi
// app ReloadConfig.
type Lookup func(id string) (ServiceInfo, bool)

// Starter bật lại 1 service. App cấp — thường là proc.Manager.Start, nhờ đó
// giữ nguyên mọi nguyên tắc đã có (process group riêng, state file, log
// append, stdin giữ mở).
type Starter func(id string) error

// EventFunc nhận sự kiện (id, kind, detail) để App ghi lịch sử / thông báo.
type EventFunc func(id, kind, detail string)

// ---------------------------------------------------------------------------
// Supervisor
// ---------------------------------------------------------------------------

// Options là cấu hình supervisor.
type Options struct {
	// Policy: chính sách giám sát.
	Policy Policy

	// StateDir: thư mục chứa state file của process do app quản lý.
	// Rỗng → mặc định ~/.port-manager/state (cùng chỗ với internal/proc).
	StateDir string

	// Lookup: App cấp để biết service nào bật auto_restart. Nil → không
	// service nào được bật lại (an toàn).
	Lookup Lookup

	// Starter: App cấp để bật lại service. Nil → supervisor chỉ quan sát,
	// không bật lại.
	Starter Starter

	// OnEvent: App cấp để ghi lịch sử / thông báo.
	OnEvent EventFunc

	// Logf: ghi log (mặc định bỏ qua).
	Logf func(format string, args ...any)

	// Now: đồng hồ (mặc định time.Now) — test thay bằng đồng hồ giả.
	Now func() time.Time

	// Alive: kiểm tra process group còn sống (mặc định: kiểm tra thật qua
	// `ps` + `kill -0`). Test thay bằng hàm giả.
	Alive func(pgid int, procStart int64) bool
}

// Supervisor giám sát service do app quản lý và bật lại khi crash.
type Supervisor struct {
	policy   Policy
	stateDir string
	lookup   Lookup
	starter  Starter
	onEvent  EventFunc
	logf     func(format string, args ...any)
	now      func() time.Time
	alive    func(pgid int, procStart int64) bool

	mu        sync.Mutex
	svcs      map[string]*svcRec
	overrides map[string]bool
	ovMu      sync.RWMutex
	running   bool
	stopCh    chan struct{}
	doneCh    chan struct{}
}

// svcRec là bộ nhớ trong RAM của supervisor cho 1 service.
type svcRec struct {
	known      bool // từng thấy service trong state file
	seenAlive  bool // từng thấy process group còn sống (bằng chứng service do app bật)
	alive      bool
	aliveSince time.Time
	deadSince  time.Time // lần đầu thấy process chết (state file còn)
	goneSince  time.Time // lần đầu thấy state file biến mất
	pgid       int

	attempt       int       // số lần đã thử bật lại trong chu kỳ hiện tại
	pending       bool      // đang chờ tới lúc bật lại
	nextAttempt   time.Time // thời điểm được phép bật lại
	exhausted     bool      // đã hết lượt → ngừng thử tới khi ổn định
	userStopped   bool      // user tắt có chủ ý → không bật lại
	stoppedPGID   int       // process group user muốn dừng (biết khi nào service được bật lại)
	crashReported bool      // đã phát event "crash" cho lần chết này
}

// New tạo supervisor. Chưa chạy gì cho tới khi gọi Start.
func New(opts Options) *Supervisor {
	s := &Supervisor{
		policy:    opts.Policy.withDefaults(),
		stateDir:  opts.StateDir,
		lookup:    opts.Lookup,
		starter:   opts.Starter,
		onEvent:   opts.OnEvent,
		logf:      opts.Logf,
		now:       opts.Now,
		alive:     opts.Alive,
		svcs:      map[string]*svcRec{},
		overrides: map[string]bool{},
	}
	if s.stateDir == "" {
		s.stateDir = defaultStateDir()
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.alive == nil {
		s.alive = groupAlive
	}
	if s.logf == nil {
		s.logf = func(string, ...any) {}
	}
	return s
}

// defaultStateDir: ~/.port-manager/state — cùng chỗ với internal/proc
// (xem cli.go resolveStateDir).
func defaultStateDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".port-manager", "state")
}

// Start chạy vòng lặp giám sát trong goroutine riêng. Vòng lặp dừng khi ctx bị
// huỷ hoặc khi gọi Stop (app đóng).
func (s *Supervisor) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.stopCh = make(chan struct{})
	s.doneCh = make(chan struct{})
	stopCh, doneCh := s.stopCh, s.doneCh
	interval := s.policy.PollInterval
	s.mu.Unlock()

	go func() {
		defer close(doneCh)
		// ctx bị huỷ (app đóng) → supervisor ngừng hẳn, không bật lại gì nữa.
		defer func() {
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
		}()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stopCh:
				return
			case <-ticker.C:
				s.tick()
			}
		}
	}()
}

// Stop dừng vòng lặp giám sát. Sau khi Stop, supervisor không bật lại service
// nào nữa (app đóng → không còn ai giám sát).
func (s *Supervisor) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	close(s.stopCh)
	done := s.doneCh
	s.mu.Unlock()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

// Running báo supervisor còn đang chạy hay không.
func (s *Supervisor) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// ---------------------------------------------------------------------------
// Kênh báo hiệu từ App / internal/proc
// ---------------------------------------------------------------------------

// ExpectStop báo: user chủ động tắt service → supervisor KHÔNG bật lại.
//
// App PHẢI gọi hàm này TRƯỚC khi tắt process (giống cờ expectedStop của
// internal/proc). Nếu không, supervisor chỉ biết service biến mất khỏi state
// file mà không phân biệt được "user tắt" với "process crash".
func (s *Supervisor) ExpectStop(id string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.recLocked(id)
	rec.userStopped = true
	// Nhớ process group user muốn dừng: chỉ khi thấy MỘT process group
	// MỚI (người dùng đã bật lại) mới bỏ chặn "user đã tắt".
	rec.stoppedPGID = rec.pgid
	rec.pending = false
	rec.attempt = 0
	rec.exhausted = false
}

// HandleEvent nhận sự kiện từ internal/proc (observer) hoặc từ App.
//
//   - "start": app vừa bật service → ghi nhận, bỏ chặn "user đã tắt".
//   - "stop": user tắt có chủ ý → không bật lại.
//   - "restart": user restart → không coi là crash.
//   - "crash": process chết ngoài ý muốn → xét bật lại.
func (s *Supervisor) HandleEvent(id, kind, detail string) {
	if id == "" {
		return
	}
	switch kind {
	case KindStart:
		s.mu.Lock()
		rec := s.recLocked(id)
		rec.known = true
		rec.userStopped = false
		rec.crashReported = false
		s.mu.Unlock()
	case KindStop, KindRestart:
		// Dừng có chủ ý → tuyệt đối không bật lại.
		s.ExpectStop(id)
	case KindCrash:
		s.handleCrashEvent(id, detail)
	}
}

// handleCrashEvent xử lý event "crash" do internal/proc phát ra (process chết
// ngoài ý muốn — internal/proc chỉ phát crash khi cờ expectedStop chưa bật).
func (s *Supervisor) handleCrashEvent(id, detail string) {
	now := s.now()
	auto := s.autoRestartMap([]string{id})

	s.mu.Lock()
	rec := s.recLocked(id)
	rec.known = true
	// Process do app bật → proc.Manager chỉ báo về process của chính nó.
	obs := Observation{
		ID:           id,
		Managed:      true,
		Alive:        false,
		ExpectedStop: rec.userStopped,
		AutoRestart:  auto[id],
	}
	evts := s.decideLocked(id, rec, obs, now, detail, false)
	s.mu.Unlock()

	s.emitAll(evts)
}

// SetAutoRestart bật/tắt tự khởi động lại cho 1 service trong RAM.
//
// App dùng hàm này sau khi đã ghi ports.yaml, để supervisor áp dụng ngay mà
// không phải chờ ReloadConfig. Nếu không có override nào, giá trị
// auto_restart trong config (qua Lookup) là nguồn sự thật.
func (s *Supervisor) SetAutoRestart(id string, on bool) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("thiếu id service")
	}
	s.ovMu.Lock()
	defer s.ovMu.Unlock()
	s.overrides[id] = on
	return nil
}

// SyncAutoRestart thay toàn bộ override bằng dữ liệu mới — App gọi sau khi
// nạp lại config để giá trị trong file là nguồn sự thật.
func (s *Supervisor) SyncAutoRestart(values map[string]bool) {
	s.ovMu.Lock()
	defer s.ovMu.Unlock()
	s.overrides = map[string]bool{}
	for k, v := range values {
		s.overrides[k] = v
	}
}

// ---------------------------------------------------------------------------
// Vòng lặp giám sát
// ---------------------------------------------------------------------------

// tick làm một vòng kiểm tra. Tách riêng khỏi vòng lặp để test được.
func (s *Supervisor) tick() {
	now := s.now()
	states := s.readStates()

	// Danh sách id cần xét: service trong ledger + service có state file.
	ids := map[string]bool{}
	s.mu.Lock()
	for id := range s.svcs {
		ids[id] = true
	}
	s.mu.Unlock()
	for id := range states {
		ids[id] = true
	}

	// Gọi lookup NGOÀI khoá supervisor (lookup của App có thể cần khoá
	// config → tránh khoá chéo).
	auto := s.autoRestartMap(mapKeys(ids))

	// alive của từng service cũng phải gọi NGOÀI khoá: alive mặc định
	// (groupAlive) spawn `ps` (scan.ProcessGroups + scan.ProcessInfos) — giữ
	// khoá s.mu khi gọi là giữ khoá suốt thời gian chạy tiến trình con, chặn
	// luôn HandleEvent/ExpectStop của App trong khoảng đó.
	aliveByID := make(map[string]bool, len(states))
	for id, st := range states {
		aliveByID[id] = s.alive(st.PGID, st.ProcStart)
	}

	// Marker ".stopping" (do CLI ghi trước khi tắt service) cũng kiểm tra
	// NGOÀI khoá — os.Stat là I/O đĩa.
	stopping := map[string]bool{}
	for id := range ids {
		if proc.StoppingRecent(s.stateDir, id) {
			stopping[id] = true
		}
	}

	var evts []evt
	var due []string

	s.mu.Lock()
	for id := range ids {
		rec := s.recLocked(id)
		st, present := states[id]
		alive := aliveByID[id]
		if present {
			rec.pgid = st.PGID
		}
		if present && alive {
			// Service do app quản lý, process còn sống.
			rec.known = true
			rec.seenAlive = true
			if !rec.alive {
				rec.alive = true
				rec.aliveSince = now
			}
			rec.deadSince = time.Time{}
			rec.goneSince = time.Time{}
			// Chỉ bỏ chặn "user đã tắt" khi thấy process group MỚI (user đã bật
			// lại) — vẫn là nhóm cũ thì lệnh tắt chưa xong, phải giữ chặn.
			if rec.userStopped && (rec.stoppedPGID <= 0 || rec.pgid != rec.stoppedPGID) {
				rec.userStopped = false
			}
			rec.crashReported = false
			// Sống ổn định đủ lâu → hết chu kỳ lỗi, reset bộ đếm thử lại.
			if s.policy.StableAfter > 0 && now.Sub(rec.aliveSince) >= s.policy.StableAfter {
				if rec.attempt != 0 || rec.exhausted {
					s.logf("supervisor: %s ổn định %s → reset bộ đếm thử lại", id, s.policy.StableAfter)
				}
				rec.attempt = 0
				rec.exhausted = false
			}
			continue
		}

		// Process không còn sống.
		rec.alive = false
		if present {
			if rec.deadSince.IsZero() {
				rec.deadSince = now
			}
		} else {
			if rec.goneSince.IsZero() {
				rec.goneSince = now
			}
		}
		if rec.userStopped {
			continue // user tắt có chủ ý → không đụng
		}
		if stopping[id] {
			// CLI (process khác) vừa chủ động tắt service này — marker
			// ".stopping" còn mới → coi như user tắt có chủ ý: khoá lại,
			// không báo crash, không bật lại. Mở khoá khi thấy process group
			// MỚI (giống ExpectStop).
			rec.userStopped = true
			rec.stoppedPGID = rec.pgid
			rec.pending = false
			rec.attempt = 0
			rec.exhausted = false
			continue
		}
		if rec.exhausted {
			continue // đã hết lượt bật lại
		}
		if !rec.seenAlive {
			continue // chưa từng thấy process sống → chưa đủ bằng chứng
		}
		since := rec.deadSince
		if since.IsZero() {
			since = rec.goneSince
		}
		if since.IsZero() {
			since = now
		}
		if now.Sub(since) < s.policy.DeadGrace {
			continue // chờ xác nhận process đã chết hẳn
		}

		obs := Observation{
			ID:           id,
			Managed:      true, // có state file do app quản lý
			Alive:        false,
			ExpectedStop: false,
			AutoRestart:  auto[id],
		}
		detail := fmt.Sprintf("process chết bất thường (pgid %d)", rec.pgid)
		evts = append(evts, s.decideLocked(id, rec, obs, now, detail, true)...)
	}

	// Các lịch bật lại đã đến hạn.
	for id, rec := range s.svcs {
		if rec.pending && !now.Before(rec.nextAttempt) {
			due = append(due, id)
		}
	}
	s.mu.Unlock()

	s.emitAll(evts)

	for _, id := range due {
		s.performRestart(id, now)
	}
}

// decideLocked là phần quyết định (dùng chung cho event "crash" và cho vòng
// lặp). Phải gọi khi đang giữ khoá s.mu.
//
// Trả về danh sách sự kiện cần phát ra ngoài khoá.
func (s *Supervisor) decideLocked(id string, rec *svcRec, obs Observation, now time.Time, detail string, emitCrash bool) []evt {
	var evts []evt
	if emitCrash && !rec.crashReported {
		rec.crashReported = true
		evts = append(evts, evt{id: id, kind: KindCrash, detail: detail})
	}
	d := Decide(obs, s.policy, rec.attempt)
	switch d.Action {
	case ActionNone:
		s.logf("supervisor: %s — không bật lại (%s)", id, d.Reason)
		return evts
	case ActionFail:
		rec.exhausted = true
		rec.pending = false
		s.logf("supervisor: %s — %s", id, d.Reason)
		evts = append(evts, evt{id: id, kind: KindFail, detail: d.Reason})
		return evts
	default:
		if rec.pending {
			// Đã có lịch bật lại → không lên lịch lần nữa.
			return evts
		}
		rec.pending = true
		rec.nextAttempt = now.Add(d.Delay)
		s.logf("supervisor: %s — %s, bật lại sau %s (lần thử %d/%d)", id, d.Reason, d.Delay, rec.attempt+1, s.policy.MaxRestarts)
		return evts
	}
}

// performRestart bật lại service đã lên lịch.
func (s *Supervisor) performRestart(id string, now time.Time) {
	if !s.Running() {
		return
	}
	s.mu.Lock()
	rec := s.recLocked(id)
	if !rec.pending || rec.userStopped {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	if s.starter == nil {
		s.mu.Lock()
		s.recLocked(id).pending = false
		s.mu.Unlock()
		return
	}

	// Trước khi bật lại: nếu service đã sống (user tự bật lại, hoặc app vừa
	// bật) → huỷ lịch, không bật thêm bản thứ hai (cổng sẽ đụng).
	if s.serviceAlive(id) {
		s.mu.Lock()
		rec := s.recLocked(id)
		rec.pending = false
		s.mu.Unlock()
		s.logf("supervisor: %s đã chạy → huỷ lịch bật lại", id)
		return
	}

	err := s.starter(id)

	// Gọi lookup NGOÀI khoá supervisor (lookup của App có thể cần khoá
	// config → tránh khoá chéo).
	auto := s.autoRestartMap([]string{id})

	s.mu.Lock()
	rec = s.recLocked(id)
	rec.pending = false
	rec.attempt++
	rec.deadSince = now
	rec.goneSince = now
	var evts []evt
	if err != nil {
		s.logf("supervisor: bật lại %s thất bại: %v", id, err)
		obs := Observation{
			ID:           id,
			Managed:      true,
			Alive:        false,
			ExpectedStop: rec.userStopped,
			AutoRestart:  auto[id],
		}
		evts = s.decideLocked(id, rec, obs, now, fmt.Sprintf("bật lại thất bại: %v", err), false)
	} else {
		rec.crashReported = false
		s.logf("supervisor: đã bật lại %s (lần thử %d/%d)", id, rec.attempt, s.policy.MaxRestarts)
	}
	s.mu.Unlock()

	s.emitAll(evts)
}

// serviceAlive báo service do app quản lý còn sống hay không (đọc state file
// rồi kiểm tra process group).
func (s *Supervisor) serviceAlive(id string) bool {
	states := s.readStates()
	st, ok := states[id]
	if !ok {
		return false
	}
	return s.alive(st.PGID, st.ProcStart)
}

// ---------------------------------------------------------------------------
// State file & process group
// ---------------------------------------------------------------------------

// procState là nội dung state file ~/.port-manager/state/<id>.proc.json.
// Giữ đúng schema của internal/proc — supervisor chỉ ĐỌC, không ghi.
type procState struct {
	PGID      int   `json:"pgid"`
	Started   int64 `json:"started"`
	ProcStart int64 `json:"procStart"`
}

// readStates đọc toàn bộ state file trong thư mục state → map id → state.
func (s *Supervisor) readStates() map[string]procState {
	out := map[string]procState{}
	if s.stateDir == "" {
		return out
	}
	entries, err := os.ReadDir(s.stateDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".proc.json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.stateDir, e.Name()))
		if err != nil {
			continue
		}
		var st procState
		if err := json.Unmarshal(data, &st); err != nil || st.PGID <= 0 {
			continue
		}
		out[strings.TrimSuffix(e.Name(), ".proc.json")] = st
	}
	return out
}

// groupAlive kiểm tra process group còn sống hay không.
//
// Dùng đúng cách internal/proc kiểm tra: pgid phải còn là process group
// leader, và (nếu state file có ghi) thời điểm process bắt đầu phải khớp →
// tránh nhầm pid đã bị hệ điều hành tái sử dụng.
func groupAlive(pgid int, procStart int64) bool {
	if pgid <= 0 {
		return false
	}
	groups := scan.ProcessGroups()
	if g, ok := groups[pgid]; !ok || g != pgid {
		return false
	}
	if procStart > 0 {
		if info, ok := scan.ProcessInfos()[pgid]; ok && !info.StartedAt.IsZero() {
			return info.StartedAt.UnixMilli() == procStart
		}
	}
	// Không lấy được thời điểm bắt đầu → hỏi trực tiếp process group.
	return syscall.Kill(-pgid, syscall.Signal(0)) == nil
}

// ---------------------------------------------------------------------------
// Tiện ích nhỏ
// ---------------------------------------------------------------------------

// evt là một sự kiện chờ phát ra.
type evt struct {
	id     string
	kind   string
	detail string
}

// recLocked lấy (hoặc tạo) bản ghi của 1 service. Phải gọi khi giữ khoá s.mu.
func (s *Supervisor) recLocked(id string) *svcRec {
	rec, ok := s.svcs[id]
	if !ok {
		rec = &svcRec{}
		s.svcs[id] = rec
	}
	return rec
}

// autoRestartMap lấy cờ auto_restart cho các id (gọi ngoài khoá supervisor).
func (s *Supervisor) autoRestartMap(ids []string) map[string]bool {
	out := map[string]bool{}
	if s.lookup == nil {
		return out
	}
	for _, id := range ids {
		out[id] = s.autoRestartFor(id)
	}
	return out
}

// autoRestartFor trả cờ auto_restart của 1 service: override trong RAM (do
// App đặt qua SetAutoRestart) được ưu tiên, không thì đọc từ config qua
// Lookup. Không giữ khoá s.mu → gọi được ở mọi nơi.
func (s *Supervisor) autoRestartFor(id string) bool {
	if v, ok := s.overrideFor(id); ok {
		return v
	}
	if s.lookup == nil {
		return false
	}
	info, ok := s.lookup(id)
	if !ok {
		return false
	}
	return info.AutoRestart
}

// overrideFor đọc override auto_restart (khóa riêng, không đụng khoá chính).
func (s *Supervisor) overrideFor(id string) (bool, bool) {
	s.ovMu.RLock()
	defer s.ovMu.RUnlock()
	v, ok := s.overrides[id]
	return v, ok
}

// emitAll phát sự kiện ra ngoài (không giữ khoá).
func (s *Supervisor) emitAll(evts []evt) {
	for _, e := range evts {
		s.emit(e.id, e.kind, e.detail)
	}
}

// emit gửi 1 sự kiện cho App (ghi lịch sử / thông báo).
func (s *Supervisor) emit(id, kind, detail string) {
	if s.onEvent == nil {
		return
	}
	s.onEvent(id, kind, detail)
}

// mapKeys trả key của map thành slice.
func mapKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ---------------------------------------------------------------------------
// Tiện ích tích hợp
// ---------------------------------------------------------------------------

// NewProcStarter tạo Starter gọi proc.Manager.Start.
//
// Nhờ đi bộ qua proc.Manager, mọi nguyên tắc đã có được giữ nguyên: process
// group riêng, state file ~/.port-manager/state/<id>.proc.json chống kill
// nhầm, log append ở logs/<id>.log (không bị xoá khi bật lại), stdin của cây
// process con được giữ mở.
//
// resolve trả service và thư mục làm việc của service đó (App cấp, vì App
// mới biết workspace_root hiện tại).
func NewProcStarter(mgr *proc.Manager, resolve func(id string) (config.Service, string, bool)) Starter {
	return func(id string) error {
		if mgr == nil {
			return fmt.Errorf("chưa có proc manager")
		}
		if resolve == nil {
			return fmt.Errorf("chưa cấu hình cách tìm service %q", id)
		}
		s, dir, ok := resolve(id)
		if !ok {
			return fmt.Errorf("không tìm thấy service %q", id)
		}
		return mgr.Start(s, dir)
	}
}
