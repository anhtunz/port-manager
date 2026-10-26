package supervisor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Hàm quyết định thuần tuý
// ---------------------------------------------------------------------------

func TestDecideKhongBatlai(t *testing.T) {
	pol := DefaultPolicy()

	cases := []struct {
		name string
		obs  Observation
		want Action
	}{
		{
			name: "service user tự chạy tay → không đụng",
			obs:  Observation{ID: "x", Managed: false, Alive: false, AutoRestart: true},
			want: ActionNone,
		},
		{
			name: "user vừa tắt có chủ ý → không bật lại",
			obs:  Observation{ID: "x", Managed: true, Alive: false, ExpectedStop: true, AutoRestart: true},
			want: ActionNone,
		},
		{
			name: "auto_restart tắt → không bật lại",
			obs:  Observation{ID: "x", Managed: true, Alive: false, AutoRestart: false},
			want: ActionNone,
		},
		{
			name: "process còn sống → không làm gì",
			obs:  Observation{ID: "x", Managed: true, Alive: true, AutoRestart: true},
			want: ActionNone,
		},
		{
			name: "supervisor tắt → không làm gì",
			obs:  Observation{ID: "x", Managed: true, Alive: false, AutoRestart: true},
			want: ActionNone,
		},
		{
			name: "crash + còn lượt → bật lại",
			obs:  Observation{ID: "x", Managed: true, Alive: false, AutoRestart: true},
			want: ActionRestart,
		},
		{
			name: "crash + hết lượt → fail",
			obs:  Observation{ID: "x", Managed: true, Alive: false, AutoRestart: true},
			want: ActionFail,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			attempt := 0
			p := pol
			if c.name == "supervisor tắt → không làm gì" {
				p.Enabled = false
			}
			if c.name == "crash + hết lượt → fail" {
				attempt = p.MaxRestarts
			}
			got := Decide(c.obs, p, attempt)
			if got.Action != c.want {
				t.Fatalf("Action = %v, want %v (reason: %s)", got.Action, c.want, got.Reason)
			}
		})
	}
}

func TestDecideBatlaiCoThoiGianCho(t *testing.T) {
	pol := DefaultPolicy()
	pol.Backoff = 5 * time.Second
	pol.MaxBackoff = 60 * time.Second

	d := Decide(Observation{ID: "x", Managed: true, AutoRestart: true}, pol, 0)
	if d.Action != ActionRestart {
		t.Fatalf("Action = %v, want restart", d.Action)
	}
	if d.Delay != 5*time.Second {
		t.Fatalf("Delay = %s, want 5s", d.Delay)
	}
}

func TestDecideHetLuotKhongThuVinh(t *testing.T) {
	pol := DefaultPolicy()
	pol.MaxRestarts = 3
	obs := Observation{ID: "x", Managed: true, AutoRestart: true}

	for attempt := 0; attempt < 3; attempt++ {
		if d := Decide(obs, pol, attempt); d.Action != ActionRestart {
			t.Fatalf("attempt %d: Action = %v, want restart", attempt, d.Action)
		}
	}
	d := Decide(obs, pol, 3)
	if d.Action != ActionFail {
		t.Fatalf("attempt 3: Action = %v, want fail", d.Action)
	}
}

func TestBackoffForTanggDanVaCoTran(t *testing.T) {
	pol := DefaultPolicy()
	pol.Backoff = 5 * time.Second
	pol.MaxBackoff = 20 * time.Second

	want := []time.Duration{
		5 * time.Second,  // lần thử 1
		10 * time.Second, // lần thử 2
		20 * time.Second, // lần thử 3 (chạm trần)
		20 * time.Second, // lần thử 4 (vẫn trần, không nhân đôi nữa)
	}
	for attempt, w := range want {
		if got := BackoffFor(pol, attempt); got != w {
			t.Fatalf("BackoffFor(%d) = %s, want %s", attempt, got, w)
		}
	}
}

// ---------------------------------------------------------------------------
// Supervisor (vòng lặp giám sát)
// ---------------------------------------------------------------------------

// testEnv là môi trường giả cho supervisor: thư mục state thật (tạm), đồng hồ
// giả, process group giả, hàm bật lại giả.
type testEnv struct {
	t   *testing.T
	dir string

	mu         sync.Mutex
	clock      time.Time
	alive      map[int]bool
	aliveCalls int
	starts     []string
	events     []evt

	policy Policy
	auto   map[string]bool
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	return &testEnv{
		t:     t,
		dir:   t.TempDir(),
		clock: time.Date(2026, 10, 22, 9, 0, 0, 0, time.Local),
		alive: map[int]bool{},
		auto:  map[string]bool{},
		policy: Policy{
			Enabled:      true,
			MaxRestarts:  3,
			Backoff:      10 * time.Millisecond,
			MaxBackoff:   time.Second,
			PollInterval: time.Hour, // ticker thật không bắn trong test
			StableAfter:  5 * time.Minute,
			DeadGrace:    0,
		},
	}
}

// writeState ghi state file như internal/proc làm (chỉ đọc ở supervisor).
func (e *testEnv) writeState(id string, pgid int) {
	e.t.Helper()
	data := fmt.Sprintf(`{"pgid":%d,"started":%d,"procStart":0}`, pgid, e.clock.UnixMilli())
	p := filepath.Join(e.dir, id+".proc.json")
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *testEnv) removeState(id string) {
	e.t.Helper()
	if err := os.Remove(filepath.Join(e.dir, id+".proc.json")); err != nil {
		e.t.Fatal(err)
	}
}

func (e *testEnv) setAlive(pgid int, alive bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.alive[pgid] = alive
}

func (e *testEnv) startCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.starts...)
}

func (e *testEnv) eventKinds() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.events))
	for _, ev := range e.events {
		out = append(out, ev.kind)
	}
	return out
}

func (e *testEnv) hasEvent(kind string) bool {
	for _, k := range e.eventKinds() {
		if k == kind {
			return true
		}
	}
	return false
}

// supervisor dựng từ env (nhưng Start chưa gọi — test gọi tick() trực tiếp).
func (e *testEnv) supervisor() *Supervisor {
	s := New(Options{
		Policy:   e.policy,
		StateDir: e.dir,
		Lookup: func(id string) (ServiceInfo, bool) {
			e.mu.Lock()
			defer e.mu.Unlock()
			v, ok := e.auto[id]
			return ServiceInfo{ID: id, AutoRestart: v}, ok
		},
		Starter: func(id string) error {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.starts = append(e.starts, id)
			return nil
		},
		OnEvent: func(id, kind, detail string) {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.events = append(e.events, evt{id: id, kind: kind, detail: detail})
		},
		Now: func() time.Time {
			e.mu.Lock()
			defer e.mu.Unlock()
			return e.clock
		},
		Alive: func(pgid int, procStart int64) bool {
			e.mu.Lock()
			defer e.mu.Unlock()
			return e.alive[pgid]
		},
	})
	// Bật "running" để performRestart được phép chạy (giống app đã start).
	s.running = true
	return s
}

// tick chạy một vòng kiểm tra (đồng hồ giả).
func (e *testEnv) tick(s *Supervisor) {
	s.tick()
}

// advanceTick dời đồng hồ rồi chạy một vòng.
func (e *testEnv) advanceTick(s *Supervisor, d time.Duration) {
	e.mu.Lock()
	e.clock = e.clock.Add(d)
	e.mu.Unlock()
	s.tick()
}

func TestSupervisorBatlaiServiceCrash(t *testing.T) {
	env := newTestEnv(t)
	env.auto["svc"] = true
	s := env.supervisor()

	// App bật service → có state file, process group còn sống.
	env.writeState("svc", 4242)
	env.setAlive(4242, true)
	env.tick(s)

	// Process chết bất thường → internal/proc xoá state file.
	env.setAlive(4242, false)
	env.removeState("svc")
	env.tick(s)

	// Chưa tới hạn backoff → chưa bật lại.
	if got := env.startCalls(); len(got) != 0 {
		t.Fatalf("bật lại quá sớm: %v", got)
	}

	// Tới hạn → bật lại đúng 1 lần.
	env.advanceTick(s, time.Second)
	got := env.startCalls()
	if len(got) != 1 || got[0] != "svc" {
		t.Fatalf("start calls = %v, want [svc]", got)
	}
	if !env.hasEvent(KindCrash) {
		t.Fatalf("thiếu event crash: %v", env.eventKinds())
	}
}

func TestSupervisorKhongBatlaiKhiAutoRestartTat(t *testing.T) {
	env := newTestEnv(t)
	env.auto["svc"] = false // auto_restart tắt
	s := env.supervisor()

	env.writeState("svc", 4242)
	env.setAlive(4242, true)
	env.tick(s)

	env.setAlive(4242, false)
	env.removeState("svc")
	env.advanceTick(s, time.Second)

	if got := env.startCalls(); len(got) != 0 {
		t.Fatalf("đã bật lại dù auto_restart tắt: %v", got)
	}
}

func TestSupervisorDungSauKhiHetLuot(t *testing.T) {
	env := newTestEnv(t)
	env.policy.MaxRestarts = 2
	env.auto["svc"] = true
	s := env.supervisor()

	env.writeState("svc", 4242)
	env.setAlive(4242, true)
	env.tick(s)

	env.setAlive(4242, false)
	env.removeState("svc")

	// Mỗi vòng: phát hiện crash → lên lịch → bật lại. Lặp tới khi hết lượt.
	for i := 0; i < 6; i++ {
		env.advanceTick(s, time.Second)
	}
	if got := env.startCalls(); len(got) != 2 {
		t.Fatalf("start calls = %v, want đúng 2 lần (MaxRestarts=2)", got)
	}
	if !env.hasEvent(KindFail) {
		t.Fatalf("thiếu event fail: %v", env.eventKinds())
	}
}

func TestSupervisorKhongDungServiceUserTuChayTay(t *testing.T) {
	env := newTestEnv(t)
	env.auto["svc"] = true
	s := env.supervisor()

	// User tự chạy `npm run dev` → KHÔNG có state file, KHÔNG có process group
	// nào của app → supervisor không có gì để can thiệp.
	for i := 0; i < 3; i++ {
		env.advanceTick(s, time.Second)
	}
	if got := env.startCalls(); len(got) != 0 {
		t.Fatalf("đã đụng service không do app quản lý: %v", got)
	}
}

func TestSupervisorKhongBatlaiServiceUserVuaTatCoY(t *testing.T) {
	env := newTestEnv(t)
	env.auto["svc"] = true
	s := env.supervisor()

	env.writeState("svc", 4242)
	env.setAlive(4242, true)
	env.tick(s)

	// App báo user tắt có chủ ý TRƯỚC khi tắt process.
	s.ExpectStop("svc")

	env.setAlive(4242, false)
	env.removeState("svc")
	for i := 0; i < 3; i++ {
		env.advanceTick(s, time.Second)
	}
	if got := env.startCalls(); len(got) != 0 {
		t.Fatalf("đã bật lại service user vừa tắt: %v", got)
	}
}

func TestSupervisorKhongBatlaiKhiUserBamRestart(t *testing.T) {
	env := newTestEnv(t)
	env.auto["svc"] = true
	s := env.supervisor()

	env.writeState("svc", 4242)
	env.setAlive(4242, true)
	env.tick(s)

	// internal/proc phát event "restart" (user bấm Restart) → không coi là crash.
	s.HandleEvent("svc", KindRestart, "restart theo yêu cầu")

	env.setAlive(4242, false)
	env.removeState("svc")
	for i := 0; i < 3; i++ {
		env.advanceTick(s, time.Second)
	}
	if got := env.startCalls(); len(got) != 0 {
		t.Fatalf("đã bật lại sau khi user restart: %v", got)
	}
}

func TestSupervisorBatlaiKhiNhanEventCrash(t *testing.T) {
	env := newTestEnv(t)
	env.auto["svc"] = true
	s := env.supervisor()

	// internal/proc phát event crash (process chết ngoài ý muốn).
	s.HandleEvent("svc", KindCrash, "thoát với code 1")

	env.advanceTick(s, time.Second)
	got := env.startCalls()
	if len(got) != 1 || got[0] != "svc" {
		t.Fatalf("start calls = %v, want [svc]", got)
	}
	// Event crash do internal/proc phát → supervisor không phát lại (tránh
	// trùng trong lịch sử).
	if n := countKind(env.eventKinds(), KindCrash); n != 0 {
		t.Fatalf("supervisor phát lại event crash (%d lần)", n)
	}
}

func TestSupervisorPhatEventFailKhiHetLuotQuaEventCrash(t *testing.T) {
	env := newTestEnv(t)
	env.policy.MaxRestarts = 1
	env.auto["svc"] = true
	s := env.supervisor()

	s.HandleEvent("svc", KindCrash, "thoát với code 1")
	env.advanceTick(s, time.Second) // lần thử 1
	if got := env.startCalls(); len(got) != 1 {
		t.Fatalf("start calls = %v, want 1", got)
	}
	s.HandleEvent("svc", KindCrash, "thoát với code 1")
	env.advanceTick(s, time.Second) // hết lượt → fail
	if !env.hasEvent(KindFail) {
		t.Fatalf("thiếu event fail: %v", env.eventKinds())
	}
	if got := env.startCalls(); len(got) != 1 {
		t.Fatalf("start calls = %v, want vẫn 1 (đã hết lượt)", got)
	}
}

func TestSupervisorResetBoDemKhiServiceOnDinh(t *testing.T) {
	env := newTestEnv(t)
	env.policy.MaxRestarts = 1
	env.policy.StableAfter = time.Minute
	env.auto["svc"] = true
	s := env.supervisor()

	env.writeState("svc", 4242)
	env.setAlive(4242, true)
	env.tick(s)

	// Crash 1 → hết lượt (MaxRestarts=1) → exhausted.
	env.setAlive(4242, false)
	env.removeState("svc")
	for i := 0; i < 4; i++ {
		env.advanceTick(s, time.Second) // phát hiện crash → bật lại → hết lượt
	}
	if !env.hasEvent(KindFail) {
		t.Fatalf("thiếu event fail: %v", env.eventKinds())
	}

	// Service chạy lại và ổn định đủ lâu → bộ đếm reset → crash mới được thử lại.
	before := len(env.startCalls())
	env.writeState("svc", 4343)
	env.setAlive(4343, true)
	env.advanceTick(s, time.Second)   // ghi nhận process sống lại (aliveSince)
	env.advanceTick(s, 2*time.Minute) // sống ổn định ≥ StableAfter → reset bộ đếm
	env.setAlive(4343, false)
	env.removeState("svc")
	env.advanceTick(s, time.Second) // phát hiện crash + lên lịch
	env.advanceTick(s, time.Second) // bật lại (lượt mới)

	if after := len(env.startCalls()); after <= before {
		t.Fatalf("sau khi reset chu kỳ vẫn không thử bật lại: %d → %d", before, after)
	}
}

func TestSupervisorGiuChanLenhTatKhiProcessCuConSong(t *testing.T) {
	env := newTestEnv(t)
	env.auto["svc"] = true
	s := env.supervisor()

	env.writeState("svc", 4242)
	env.setAlive(4242, true)
	env.tick(s)

	// App báo "user tắt" TRƯỚC khi kill → chặn phải có hiệu lực ngay, kể cả khi
	// supervisor vẫn thấy process cũ còn sống (app chưa kịp kill).
	s.ExpectStop("svc")
	env.tick(s)
	if !s.recForTest("svc").userStopped {
		t.Fatal("mất chặn 'user đã tắt' khi process cũ còn sống")
	}

	// Process chết + state file bị xoá → vẫn không được bật lại.
	env.setAlive(4242, false)
	env.removeState("svc")
	for i := 0; i < 3; i++ {
		env.advanceTick(s, time.Second)
	}
	if got := env.startCalls(); len(got) != 0 {
		t.Fatalf("đã bật lại service user vừa tắt: %v", got)
	}

	// User bật lại service (process group MỚI) → chặn được gỡ, service trở lại
	// bình thường: crash tiếp theo vẫn được bật lại.
	env.writeState("svc", 4343)
	env.setAlive(4343, true)
	env.tick(s)
	if s.recForTest("svc").userStopped {
		t.Fatal("chưa gỡ chặn 'user đã tắt' sau khi service được bật lại")
	}
	env.setAlive(4343, false)
	env.removeState("svc")
	env.advanceTick(s, time.Second)
	env.advanceTick(s, time.Second)
	if got := env.startCalls(); len(got) != 1 {
		t.Fatalf("start calls = %v, want 1 (service đã bật lại bởi user)", got)
	}
}

func TestSupervisorStopKhongBatlaiNua(t *testing.T) {
	env := newTestEnv(t)
	env.policy.PollInterval = 10 * time.Millisecond
	env.policy.MaxRestarts = 100
	env.auto["svc"] = true

	// Đồng hồ thật cho test này (vòng lặp thật chạy nền).
	var observed int32
	s := New(Options{
		Policy:   env.policy,
		StateDir: env.dir,
		Lookup: func(id string) (ServiceInfo, bool) {
			return ServiceInfo{ID: id, AutoRestart: true}, true
		},
		Starter: func(id string) error {
			env.mu.Lock()
			defer env.mu.Unlock()
			env.starts = append(env.starts, id)
			return nil
		},
		Now: time.Now,
		Alive: func(pgid int, procStart int64) bool {
			env.mu.Lock()
			defer env.mu.Unlock()
			env.aliveCalls++
			return env.alive[pgid]
		},
	})

	env.writeState("svc", 4242)
	env.setAlive(4242, true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	// Chờ supervisor quan sát thấy process còn sống (bằng chứng service do app
	// bật) rồi mới giả vờ process chết.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&observed) == 0 {
		env.mu.Lock()
		observed = int32(env.aliveCalls)
		env.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	if atomic.LoadInt32(&observed) == 0 {
		t.Fatal("supervisor không quan sát service")
	}

	// Chờ supervisor bật lại service đã chết.
	env.setAlive(4242, false)
	env.removeState("svc")
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(env.startCalls()) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if len(env.startCalls()) == 0 {
		t.Fatal("supervisor không bật lại service crash")
	}

	s.Stop()
	if s.Running() {
		t.Fatal("supervisor vẫn chạy sau Stop()")
	}
	before := len(env.startCalls())
	time.Sleep(200 * time.Millisecond)
	if len(env.startCalls()) != before {
		t.Fatalf("vẫn bật lại sau khi Stop(): %d → %d", before, len(env.startCalls()))
	}
}

func TestSetAutoRestartOverride(t *testing.T) {
	env := newTestEnv(t)
	env.auto["svc"] = false
	s := env.supervisor()

	if err := s.SetAutoRestart("svc", true); err != nil {
		t.Fatal(err)
	}
	if !s.autoRestartFor("svc") {
		t.Fatal("override auto_restart=true không có hiệu lực")
	}
	if err := s.SetAutoRestart("", true); err == nil {
		t.Fatal("SetAutoRestart(\"\") phải trả lỗi")
	}
	s.SyncAutoRestart(map[string]bool{"svc": false})
	if s.autoRestartFor("svc") {
		t.Fatal("SyncAutoRestart chưa áp dụng giá trị mới")
	}
}

func TestReadStates(t *testing.T) {
	env := newTestEnv(t)
	env.writeState("a", 111)
	env.writeState("b", 222)
	// File rác → bỏ qua.
	if err := os.WriteFile(filepath.Join(env.dir, "c.proc.json"), []byte("không phải json"), 0o644); err != nil {
		t.Fatal(err)
	}
	states := env.supervisor().readStates()
	if len(states) != 2 {
		t.Fatalf("đọc %d state file, want 2", len(states))
	}
	if states["a"].PGID != 111 || states["b"].PGID != 222 {
		t.Fatalf("state sai: %+v", states)
	}
}

// recForTest đọc bản ghi nội bộ của 1 service (chỉ dùng trong test).
func (s *Supervisor) recForTest(id string) *svcRec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recLocked(id)
}

func countKind(kinds []string, kind string) int {
	n := 0
	for _, k := range kinds {
		if k == kind {
			n++
		}
	}
	return n
}
