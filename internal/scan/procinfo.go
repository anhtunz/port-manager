package scan

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ProcInfo là thông tin của một process lấy từ `ps`: command line + thời điểm
// process bắt đầu chạy (để tính "đã chạy bao lâu").
type ProcInfo struct {
	Command   string
	StartedAt time.Time
}

// psLstartLayout: định dạng cột `lstart` của ps trên macOS — "Wed Oct 21 10:54:03 2026".
// ps in theo locale nên lệnh ps luôn chạy với LC_ALL=C để không đổi tên tháng/ngày.
const psLstartLayout = "Mon Jan 2 15:04:05 2006"

// ProcessInfos trả map pid → ProcInfo cho toàn bộ process trên máy.
// Một lần gọi `ps` duy nhất cho cả command line lẫn thời điểm bắt đầu chạy.
func ProcessInfos() map[int]ProcInfo {
	cmd := exec.Command("ps", "-axo", "pid=,lstart=,command=")
	cmd.Env = cLocaleEnv()
	out, err := cmd.Output()
	if err != nil {
		return map[int]ProcInfo{}
	}
	return ParsePsOutput(string(out))
}

// ProcessGroups trả map pid → pgid cho toàn bộ process trên máy.
// Dùng để kiểm tra "pid này còn là process ta đã spawn" — pid bị hệ điều hành
// tái sử dụng thì pgid gần như chắc chắn khác, nên tránh kill nhầm.
func ProcessGroups() map[int]int {
	cmd := exec.Command("ps", "-axo", "pid=,pgid=")
	out, err := cmd.Output()
	if err != nil {
		return map[int]int{}
	}
	m := map[int]int{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		pgid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		m[pid] = pgid
	}
	return m
}

// cLocaleEnv ép locale C cho lệnh con — `ps` in cột lstart theo locale, không ép
// thì tên tháng/ngày có thể không parse được.
func cLocaleEnv() []string {
	drop := map[string]bool{"LC_ALL": true, "LC_CTYPE": true, "LANG": true}
	env := make([]string, 0, len(os.Environ())+1)
	for _, e := range os.Environ() {
		if k := strings.SplitN(e, "=", 2)[0]; drop[k] {
			continue
		}
		env = append(env, e)
	}
	return append(env, "LC_ALL=C")
}

// ParsePsOutput parse output của `ps -axo pid=,lstart=,command=`.
// Mỗi dòng: pid + 5 token của lstart (thứ, tháng, ngày, giờ, năm) + command.
func ParsePsOutput(data string) map[int]ProcInfo {
	m := map[int]ProcInfo{}
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		pid, started, cmd := ParsePsLine(line)
		if pid == 0 {
			continue
		}
		m[pid] = ProcInfo{Command: cmd, StartedAt: started}
	}
	return m
}

// ParsePsLine tách một dòng ps thành pid / thời điểm bắt đầu / command line.
// Command giữ nguyên khoảng trắng bên trong (nhiều process có tên có dấu cách).
func ParsePsLine(line string) (int, time.Time, string) {
	i := 0
	pidTok, i := nextField(line, i)
	pid, err := strconv.Atoi(pidTok)
	if err != nil {
		return 0, time.Time{}, ""
	}
	var lstart [5]string
	for k := 0; k < len(lstart); k++ {
		t, ni := nextField(line, i)
		if t == "" {
			return pid, time.Time{}, strings.TrimSpace(line[i:])
		}
		lstart[k] = t
		i = ni
	}
	started, err := time.ParseInLocation(psLstartLayout, strings.Join(lstart[:], " "), time.Local)
	if err != nil {
		started = time.Time{}
	}
	return pid, started, strings.TrimSpace(line[i:])
}

// nextField đọc token kế tiếp (không chứa space), trả về token và vị trí kế tiếp.
func nextField(s string, i int) (string, int) {
	for i < len(s) && s[i] == ' ' {
		i++
	}
	start := i
	for i < len(s) && s[i] != ' ' {
		i++
	}
	return s[start:i], i
}
