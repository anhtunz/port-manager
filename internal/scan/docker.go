package scan

import (
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// dockerTimeout: giới hạn thời gian chờ docker — Docker Desktop đang trong lúc
// khởi động lại có thể treo lệnh `docker ps` vài giây, không được chặn cả quét.
const dockerTimeout = 3 * time.Second

// PortOwner: container đang publish cổng ra máy host.
type PortOwner struct {
	Container string    // tên container
	Project   string    // thư mục project (label working_dir của compose) — tuyệt đối
	StartedAt time.Time // lúc container bắt đầu chạy
}

// DockerSnapshot: ảnh chụp trạng thái docker lúc quét. Gộp 2 thứ cần thiết:
//   - thời điểm start của service compose (để tính "đã chạy bao lâu")
//   - container nào đang publish cổng nào ra host (để biết cổng lạ do ai giữ)
//
// Chỉ tốn 2 lệnh docker: 1 `docker ps` + 1 `docker inspect` cho toàn bộ container.
type DockerSnapshot struct {
	byPath map[string]time.Time // <đường dẫn tuyệt đối của file compose>|<service>
	byBase map[string]time.Time // <tên file compose>|<service> — fallback khi path lệch
	byPort map[int]PortOwner    // host port ← container publish
}

// StartTime trả thời điểm container bắt đầu chạy. composeFileAbs = đường dẫn
// tuyệt đối của file compose, service = tên service trong compose.
func (d *DockerSnapshot) StartTime(composeFileAbs, service string) (time.Time, bool) {
	if d == nil {
		return time.Time{}, false
	}
	if t, ok := d.byPath[composeFileAbs+"|"+service]; ok {
		return t, true
	}
	base := filepath.Base(composeFileAbs)
	if t, ok := d.byBase[base+"|"+service]; ok {
		return t, true
	}
	return time.Time{}, false
}

// PortOwner trả container đang giữ cổng host (nếu có).
func (d *DockerSnapshot) PortOwner(port int) (PortOwner, bool) {
	if d == nil {
		return PortOwner{}, false
	}
	o, ok := d.byPort[port]
	return o, ok
}

// ReadDockerSnapshot đọc trạng thái docker đang chạy.
// Dùng 2 lệnh: `docker ps` (1 lần, lấy id + tên + port publish + label compose)
// rồi `docker inspect` (1 lần, lấy State.StartedAt cho toàn bộ id).
// Docker không chạy / lỗi → trả về snapshot rỗng, không làm hỏng cả lần quét.
func ReadDockerSnapshot() *DockerSnapshot {
	snap := &DockerSnapshot{
		byPath: map[string]time.Time{},
		byBase: map[string]time.Time{},
		byPort: map[int]PortOwner{},
	}
	ctx, cancel := context.WithTimeout(context.Background(), dockerTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, "docker", "ps", "--no-trunc",
		"--format", "{{.ID}}\t{{.Names}}\t{{.Ports}}\t"+
			"{{.Label \"com.docker.compose.service\"}}\t"+
			"{{.Label \"com.docker.compose.project.config_files\"}}\t"+
			"{{.Label \"com.docker.compose.project.working_dir\"}}",
	).Output()
	if err != nil {
		return snap
	}

	type row struct {
		service, files, workingDir string
		names                      string
		ports                      []int
	}
	rows := map[string]row{} // container id → thông tin
	ids := []string{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 6 {
			continue
		}
		id := strings.TrimSpace(f[0])
		if id == "" {
			continue
		}
		r := row{
			names:      strings.TrimSpace(f[1]),
			ports:      ParseDockerPorts(f[2]),
			service:    strings.TrimSpace(f[3]),
			files:      strings.TrimSpace(f[4]),
			workingDir: strings.TrimSpace(f[5]),
		}
		rows[id] = r
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return snap
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), dockerTimeout)
	defer cancel2()
	args := append([]string{"inspect", "--format", "{{.Id}}\t{{.State.StartedAt}}"}, ids...)
	ins, err := exec.CommandContext(ctx2, "docker", args...).Output()
	if err != nil {
		return snap
	}
	started := map[string]time.Time{}
	for _, line := range strings.Split(string(ins), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 2 {
			continue
		}
		id, startedRaw := strings.TrimSpace(f[0]), strings.TrimSpace(f[1])
		t, err := time.Parse(time.RFC3339Nano, startedRaw)
		if err != nil {
			continue
		}
		started[id] = t
	}

	for id, r := range rows {
		t := started[id]
		if r.service != "" { // container thuần (không do compose quản lý) không có entry compose
			for _, cf := range strings.Split(r.files, ",") {
				cf = strings.TrimSpace(cf)
				if cf == "" {
					continue
				}
				abs, err := filepath.Abs(cf)
				if err != nil {
					abs = cf
				}
				snap.byPath[abs+"|"+r.service] = t
				snap.byBase[filepath.Base(abs)+"|"+r.service] = t
			}
		}
		for _, p := range r.ports {
			snap.byPort[p] = PortOwner{Container: r.names, Project: r.workingDir, StartedAt: t}
		}
	}
	return snap
}

// ParseDockerPorts rút các cổng host mà container publish ra từ trường .Ports
// của `docker ps --format`. Ví dụ: "0.0.0.0:8088->80/tcp, [::]:8088->80/tcp"
// → [8088]. Dải "9000-9001->9000-9001/tcp" → [9000 9001].
// Trường rỗng (container không publish port) → nil.
func ParseDockerPorts(field string) []int {
	var out []int
	seen := map[int]bool{}
	add := func(p int) {
		if p <= 0 || p > 65535 || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, chunk := range strings.Split(field, ",") {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		left, _, ok := strings.Cut(chunk, "->")
		if !ok {
			continue // không phải cổng publish ra host
		}
		idx := strings.LastIndex(left, ":")
		if idx < 0 {
			continue
		}
		hostRange := strings.TrimSpace(left[idx+1:])
		if lo, hi, ok := strings.Cut(hostRange, "-"); ok {
			l, err1 := strconv.Atoi(strings.TrimSpace(lo))
			h, err2 := strconv.Atoi(strings.TrimSpace(hi))
			if err1 == nil && err2 == nil && l <= h {
				for p := l; p <= h; p++ {
					add(p)
				}
			}
			continue
		}
		if p, err := strconv.Atoi(hostRange); err == nil {
			add(p)
		}
	}
	return out
}
