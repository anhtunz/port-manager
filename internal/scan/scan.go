package scan

import (
	"fmt"
	"os/exec"
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
	Status       string        `json:"status"` // running | partial | stopped
	PID          int           `json:"pid"`
	Command      string        `json:"command"` // full command line (từ ps) của listener
	ManagedByApp bool          `json:"managedByApp"`
	StartCmd     string        `json:"startCmd"`
	IsCompose    bool          `json:"isCompose"`
	Repo         *git.RepoView `json:"repo,omitempty"` // nil nếu dir không thuộc git repo
}

type UnknownListener struct {
	Port    int    `json:"port"`
	PID     int    `json:"pid"`
	Command string `json:"command"`
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

// CommandLines trả map pid → full command line (một lần gọi ps cho mọi pid).
func CommandLines(pids []int) map[int]string {
	m := map[int]string{}
	if len(pids) == 0 {
		return m
	}
	out, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return m
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		sp := strings.IndexByte(line, ' ')
		if sp < 0 {
			continue
		}
		pid, err := strconv.Atoi(line[:sp])
		if err != nil {
			continue
		}
		m[pid] = strings.TrimSpace(line[sp+1:])
	}
	return m
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

// ScanAll quét trạng thái toàn bộ service trong config + các cổng lạ.
// managed cho biết service nào đang do app quản lý (từ proc.Manager).
func ScanAll(cfg *config.Config, root string, managed func(id string) bool) *Result {
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

	byPort := map[int]Listener{}
	pidSet := map[int]bool{}
	for _, l := range ls {
		if _, ok := byPort[l.Port]; !ok {
			byPort[l.Port] = l
		}
		pidSet[l.PID] = true
	}
	pids := make([]int, 0, len(pidSet))
	for p := range pidSet {
		pids = append(pids, p)
	}
	cmds := CommandLines(pids)

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

	for _, g := range cfg.Groups {
		gv := GroupView{Name: g.Name, Services: []ServiceView{}}
		for _, s := range g.Services {
			sv := ServiceView{
				ID:           s.ID,
				Name:         s.Name,
				Ports:        []PortView{},
				StartCmd:     s.Start,
				IsCompose:    s.IsCompose(),
				ManagedByApp: managed != nil && managed(s.ID),
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
			switch {
			case open == len(s.Ports):
				sv.Status = "running"
			case open > 0:
				sv.Status = "partial"
			default:
				sv.Status = "stopped"
			}
			if first.PID != 0 {
				sv.PID = first.PID
				sv.Command = cmds[first.PID]
				if sv.Command == "" {
					sv.Command = first.Command
				}
			}
			sv.Repo = repoOf(s)
			gv.Services = append(gv.Services, sv)
		}
		res.Groups = append(res.Groups, gv)
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
		cmd := cmds[l.PID]
		if cmd == "" {
			cmd = l.Command
		}
		res.Unknown = append(res.Unknown, UnknownListener{Port: l.Port, PID: l.PID, Command: cmd})
	}
	return res
}
