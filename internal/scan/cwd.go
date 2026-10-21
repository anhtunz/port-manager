package scan

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// lsofTimeout: giới hạn thời gian chờ lsof — không được chặn cả lần quét.
const lsofTimeout = 2 * time.Second

// ProcessCwds trả map pid → thư mục làm việc (cwd) của các process.
// Dùng `lsof -a -p <pids> -d cwd -Fn` — 1 lệnh cho nhiều pid.
// Cwd giúp biết process "lạ" thuộc thư mục/dự án nào (vd vite của 1 FE).
// lsof không chạy / lỗi → trả map rỗng.
func ProcessCwds(pids []int) map[int]string {
	out := map[int]string{}
	if len(pids) == 0 {
		return out
	}
	parts := make([]string, 0, len(pids))
	for _, p := range pids {
		if p > 0 {
			parts = append(parts, strconv.Itoa(p))
		}
	}
	if len(parts) == 0 {
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), lsofTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "lsof", "-a", "-p", strings.Join(parts, ","), "-d", "cwd", "-Fn")
	raw, err := cmd.Output()
	if err != nil && len(raw) == 0 {
		return out
	}
	return ParseLsofCwd(string(raw))
}

// ParseLsofCwd parse output `lsof -Fn` cho fd cwd. Mỗi process một khối:
//
//	p<pid>\n  (bắt đầu record)
//	fcwd\n     (fd đang xét)
//	n<path>\n   (đường dẫn cwd)
func ParseLsofCwd(data string) map[int]string {
	out := map[int]string{}
	cur := 0
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "p"):
			pid, err := strconv.Atoi(strings.TrimPrefix(line, "p"))
			if err != nil {
				cur = 0
				continue
			}
			cur = pid
		case strings.HasPrefix(line, "n") && cur > 0:
			out[cur] = strings.TrimPrefix(line, "n")
		}
	}
	return out
}
