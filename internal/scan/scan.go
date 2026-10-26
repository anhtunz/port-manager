package scan

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"smatec/port-manager/internal/config"
	"smatec/port-manager/internal/git"
)

// Listener là một socket TCP đang LISTEN trên máy (một port có thể có nhiều
// listener: IPv4 + IPv6, hoặc process khác nhau).
type Listener struct {
	PID     int
	Command string // tên process theo lsof (không phải full command line)
	Port    int
}

type PortView struct {
	Port int  `json:"port"`
	Open bool `json:"open"`
}

type ServiceView struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Ports        []PortView    `json:"ports"`
	Status       string        `json:"status"` // running | partial | starting | stuck | stopped
	PID          int           `json:"pid"`
	Command      string        `json:"command"` // full command line (từ ps) của listener
	ManagedByApp bool          `json:"managedByApp"`
	StartCmd     string        `json:"startCmd"`
	IsCompose    bool          `json:"isCompose"`
	StartedAt    int64         `json:"startedAt,omitempty"`    // unix milli — lúc process/container bắt đầu chạy (0 = chưa biết)
	HasLog       bool          `json:"hasLog"`                 // có file log để xem (service từng do app bật)
	ManagedPID   int           `json:"managedPid,omitempty"`   // pid của process group do app quản lý (0 = app không quản lý / process đã chết)
	ManagedSince int64         `json:"managedSince,omitempty"` // unix milli — lúc app bật service
	Repo         *git.RepoView `json:"repo,omitempty"`         // nil nếu dir không thuộc git repo

	// LastError: dòng lỗi cuối cùng trong logs/<id>.log của lần chạy hiện tại
	// (sau marker "=== bật lúc" cuối). App điền sau khi quét qua internal/lastlog
	// (đọc 256KB cuối + cache 10s) — user thấy ngay service vì sao không lên
	// cổng mà không phải mở log.
	LastError string `json:"lastError,omitempty"`

	// AutoRestart: cờ auto_restart trong ports.yaml của service.
	AutoRestart bool `json:"autoRestart,omitempty"`

	// RepoDir: giá trị repo_dir trong ports.yaml (thư mục gốc git repo, tương
	// đối workspace). App điền sau khi quét để form Sửa service điền sẵn đúng
	// giá trị cũ — form gửi giá trị rỗng thì backend xoá key đó khỏi
	// ports.yaml (setString rỗng = removeKey), nên không được để user đoán mò.
	RepoDir string `json:"repoDir,omitempty"`

	// HealthPath / HealthSkipPorts: health_path + health_skip_ports trong
	// ports.yaml — form Sửa điền sẵn, cùng lý do với RepoDir.
	HealthPath      string `json:"healthPath,omitempty"`
	HealthSkipPorts []int  `json:"healthSkipPorts,omitempty"`
}

// ManagedInfo: trạng thái process do app quản lý cho một service.
type ManagedInfo struct {
	PID     int   // pid của process group leader
	Started int64 // unix milli — lúc app bật service
	Alive   bool  // process còn sống
}

// ManagedProvider: nơi lấy thông tin process do app quản lý.
// proc.Manager hiện thực — scan khai báo interface để không import proc
// (proc đã import scan, import ngược lại là vòng phụ thuộc).
type ManagedProvider interface {
	ManagedInfos() map[string]ManagedInfo
}

// UnknownListener: một listener không nằm trong cấu hình. Ngoài port/pid/command
// còn tra nguồn gốc để UI nói rõ nó thuộc dự án nào — nếu không thì chỉ hiện
// command (vd docker backend) thì không hiểu được là của ai.
type UnknownListener struct {
	Port      int    `json:"port"`
	PID       int    `json:"pid"`
	Command   string `json:"command"`
	Cwd       string `json:"cwd,omitempty"`       // thư mục làm việc (tương đối workspace nếu nằm trong workspace)
	Container string `json:"container,omitempty"` // container đang publish cổng này ra host
	Project   string `json:"project,omitempty"`   // thư mục project của container (tương đối workspace)
	StartedAt int64  `json:"startedAt,omitempty"` // unix milli — lúc process/container bắt đầu chạy
}

type GroupView struct {
	Name     string        `json:"name"`
	Services []ServiceView `json:"services"`
}

type Result struct {
	WorkspaceRoot string            `json:"workspaceRoot"`
	ScannedAt     int64             `json:"scannedAt"` // unix milli
	Groups        []GroupView       `json:"groups"`
	Unknown       []UnknownListener `json:"unknown"`
	Error         string            `json:"error,omitempty"`
}

// Listeners chạy `lsof` lấy toàn bộ socket TCP đang LISTEN.
// Dùng field output (-F0) vì tên process chứa dấu cách (vd "Code Helper")
// làm vỡ việc parse theo cột.
func Listeners() ([]Listener, error) {
	out, err := exec.Command("lsof", "+c0", "-F0pcn", "-nP", "-iTCP", "-sTCP:LISTEN").Output()
	if err != nil {
		// lsof trả exit 1 khi không có socket nào khớp — không phải lỗi
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("lsof: %w", err)
	}
	return ParseListeners(out), nil
}

// ParseListeners parse field output của lsof -F0pcn. Mỗi record kết thúc bằng
// `\0\n` — sau khi split theo NUL, dấu newline dính ở ĐẦU field kế tiếp
// (`"\np779"`) nên phải trim `\n` ở cả hai đầu, không chỉ phải.
func ParseListeners(data []byte) []Listener {
	var ls []Listener
	seen := map[string]bool{}
	var pid int
	var cmd string
	for _, field := range strings.Split(string(data), "\x00") {
		field = strings.Trim(field, "\n")
		if field == "" {
			continue
		}
		switch field[0] {
		case 'p':
			pid, _ = strconv.Atoi(field[1:])
		case 'c':
			cmd = field[1:]
		case 'n':
			name := field[1:]
			idx := strings.LastIndexByte(name, ':')
			if pid == 0 || idx < 0 {
				continue
			}
			port, err := strconv.Atoi(name[idx+1:])
			if err != nil {
				continue
			}
			key := strconv.Itoa(pid) + ":" + strconv.Itoa(port)
			if seen[key] {
				continue
			}
			seen[key] = true
			ls = append(ls, Listener{PID: pid, Command: cmd, Port: port})
		}
	}
	sort.Slice(ls, func(i, j int) bool {
		if ls[i].Port != ls[j].Port {
			return ls[i].Port < ls[j].Port
		}
		return ls[i].PID < ls[j].PID
	})
	return ls
}

// RepoDirPath trả thư mục git repo của service (đã gắn workspace root).
// Ưu tiên repo_dir khai trong config; mặc định Dir với service thường,
// bỏ qua với compose (compose dir không mặc định là repo).
func RepoDirPath(cfg *config.Config, root string, s config.Service) string {
	if s.RepoDir != "" {
		return cfg.ResolveDir(root, s.RepoDir)
	}
	if s.IsCompose() {
		return ""
	}
	return cfg.ResolveDir(root, s.Dir)
}

// StartingWindow: service do app bật mà sau thời gian này vẫn chưa lên cổng
// thì coi là "kẹt" chứ không còn là "đang khởi động" nữa.
const StartingWindow = 90 * time.Second

// ServiceStatus quyết định trạng thái hiển thị của 1 service.
// open = số cổng đang mở, total = số cổng khai trong config,
// mi = thông tin process do app quản lý (0 = app không quản lý).
func ServiceStatus(open, total int, mi ManagedInfo, now time.Time) string {
	switch {
	case open >= total && total > 0:
		return "running"
	case open > 0:
		return "partial"
	case mi.Alive:
		// App đã bật nhưng chưa lên cổng → đang khởi động, hoặc kẹt nếu lâu
		// quá StartingWindow (đang build, chờ DB, treo).
		if mi.Started > 0 && now.Sub(time.UnixMilli(mi.Started)) >= StartingWindow {
			return "stuck"
		}
		return "starting"
	default:
		return "stopped"
	}
}

// ScanAll quét trạng thái toàn bộ service trong config + các cổng lạ.
// mgr cho biết service nào đang do app quản lý (proc.Manager) — dùng để phân
// biệt "tắt thật" với "đang khởi động / kẹt" (process còn sống nhưng chưa lên cổng).
func ScanAll(cfg *config.Config, root string, mgr ManagedProvider) *Result {
	res := &Result{
		WorkspaceRoot: root,
		ScannedAt:     time.Now().UnixMilli(),
		Groups:        []GroupView{},
		Unknown:       []UnknownListener{},
	}
	ls, err := Listeners()
	if err != nil {
		res.Error = err.Error()
		return res
	}

	managed := map[string]ManagedInfo{}
	if mgr != nil {
		if mi := mgr.ManagedInfos(); mi != nil {
			managed = mi
		}
	}

	byPort := map[int]Listener{}
	for _, l := range ls {
		if _, ok := byPort[l.Port]; !ok {
			byPort[l.Port] = l
		}
	}
	// Một lần gọi ps duy nhất: lấy command line + thời điểm bắt đầu của process.
	infos := ProcessInfos()

	// Docker: 1 ảnh chụp duy nhất cho cả thời gian chạy của service compose lẫn
	// container đang publish cổng nào ra host. Lấy lazy — chỉ gọi khi thật cần.
	var snap *DockerSnapshot
	dockerSnapshot := func() *DockerSnapshot {
		if snap == nil {
			snap = ReadDockerSnapshot()
		}
		return snap
	}

	// Resolve git repo theo repo dir, dedupe trong 1 lần quét (BE/FE emulator
	// chung repo; resolve nil cũng cache để không probe lại mỗi dòng).
	repoCache := map[string]*git.RepoView{}
	repoOf := func(s config.Service) *git.RepoView {
		rd := RepoDirPath(cfg, root, s)
		if rd == "" {
			return nil
		}
		if rv, ok := repoCache[rd]; ok {
			return rv
		}
		r, _ := git.Resolve(rd)
		repoCache[rd] = r
		return r
	}

	// Service hạ tầng (compose): thời gian chạy lấy từ container, không phải
	// process docker backend (lúc Docker Desktop bật, không phải lúc container
	// bật). Gom danh sách rồi tra 1 lần cho tất cả để không gọi docker lặp lại.
	type composeRef struct {
		gi, si  int
		fileAbs string
		svcName string
	}
	composeRefs := []composeRef{}

	for gi, g := range cfg.Groups {
		gv := GroupView{Name: g.Name, Services: []ServiceView{}}
		for si, s := range g.Services {
			mi := managed[s.ID]
			sv := ServiceView{
				ID:           s.ID,
				Name:         s.Name,
				Ports:        []PortView{},
				StartCmd:     s.Start,
				IsCompose:    s.IsCompose(),
				AutoRestart:  s.AutoRestart,
				ManagedByApp: mi.Alive,
				ManagedPID:   mi.PID,
				ManagedSince: mi.Started,
			}
			open := 0
			var first Listener
			for _, p := range s.Ports {
				l, ok := byPort[p]
				sv.Ports = append(sv.Ports, PortView{Port: p, Open: ok})
				if ok {
					open++
					if first.PID == 0 || l.Port < first.Port {
						first = l
					}
				}
			}
			sv.Status = ServiceStatus(open, len(s.Ports), mi, time.Now())
			if first.PID != 0 {
				sv.PID = first.PID
				sv.Command = infos[first.PID].Command
				if sv.Command == "" {
					sv.Command = first.Command
				}
				if s.IsCompose() {
					composeRefs = append(composeRefs, composeRef{
						gi:      gi,
						si:      si,
						fileAbs: filepath.Join(root, s.ComposeDir, s.ComposeFile),
						svcName: s.ComposeService,
					})
				} else if started := infos[first.PID].StartedAt; !started.IsZero() {
					sv.StartedAt = started.UnixMilli()
				}
			}
			sv.Repo = repoOf(s)
			gv.Services = append(gv.Services, sv)
		}
		res.Groups = append(res.Groups, gv)
	}

	if len(composeRefs) > 0 {
		ci := dockerSnapshot()
		for _, ref := range composeRefs {
			if ref.si >= len(res.Groups[ref.gi].Services) {
				continue
			}
			if t, ok := ci.StartTime(ref.fileAbs, ref.svcName); ok {
				res.Groups[ref.gi].Services[ref.si].StartedAt = t.UnixMilli()
			}
		}
	}

	// Cổng lạ: socket đang LISTEN nhưng port không nằm trong cấu hình
	conf := cfg.ConfiguredPorts()
	unknownSeen := map[int]bool{}
	for _, l := range ls {
		if conf[l.Port] || unknownSeen[l.Port] {
			continue
		}
		unknownSeen[l.Port] = true
		if cfg.IsIgnoredUnknown(l.Command, l.Port) {
			continue
		}
		cmd := infos[l.PID].Command
		if cmd == "" {
			cmd = l.Command
		}
		res.Unknown = append(res.Unknown, UnknownListener{Port: l.Port, PID: l.PID, Command: cmd})
	}

	// Tra nguồn gốc cho cổng lạ: đây là process nào, chạy ở thư mục nào, hay
	// container nào đang publish cổng đó ra host. Không có thông tin này thì
	// UI chỉ hiện command (vd "com.docker.backend") — không hiểu thuộc ai.
	if len(res.Unknown) > 0 {
		pids := make([]int, 0, len(res.Unknown))
		for _, u := range res.Unknown {
			pids = append(pids, u.PID)
		}
		cwds := ProcessCwds(pids)
		snap := dockerSnapshot()
		for i := range res.Unknown {
			u := &res.Unknown[i]
			if cwd, ok := cwds[u.PID]; ok && cwd != "" {
				u.Cwd = relToRoot(root, cwd)
			}
			if st := infos[u.PID].StartedAt; !st.IsZero() {
				u.StartedAt = st.UnixMilli()
			}
			if owner, ok := snap.PortOwner(u.Port); ok {
				u.Container = owner.Container
				u.Project = relToRoot(root, owner.Project)
				if !owner.StartedAt.IsZero() {
					u.StartedAt = owner.StartedAt.UnixMilli()
				}
			}
		}
	}
	return res
}

// relToRoot rút gọn đường dẫn tuyệt đối về dạng tương đối so với gốc workspace
// (vd "assistant-service/frontend") cho dễ đọc trong UI. Đường dẫn nằm ngoài
// workspace thì giữ nguyên dạng tuyệt đối.
func relToRoot(root, path string) string {
	if root == "" || path == "" {
		return path
	}
	if path == root {
		return "."
	}
	prefix := root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	if strings.HasPrefix(path, prefix) {
		return path[len(prefix):]
	}
	return path
}
