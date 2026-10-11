package git

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// RepoView là thông tin git rút gọn gắn trên mỗi dòng service (poll thường):
// 2 lần gọi git/repo/lần quét (rev-parse + status), đủ nhanh cho auto-refresh.
type RepoView struct {
	TopLevel   string `json:"topLevel"`
	Branch     string `json:"branch"` // tên branch hoặc "(detached @ <sha>)"
	Detached   bool   `json:"detached"`
	IsWorktree bool   `json:"isWorktree"`
	Dirty      int    `json:"dirty"` // số entry chưa commit (kể cả untracked)
	Ahead      int    `json:"ahead"`
	Behind     int    `json:"behind"`
	Upstream   string `json:"upstream,omitempty"`
}

type Worktree struct {
	Path     string `json:"path"`
	Branch   string `json:"branch"`
	Detached bool   `json:"detached"`
	Current  bool   `json:"current"`
}

type RepoDetails struct {
	View      *RepoView  `json:"view"`
	Branches  []string   `json:"branches"`
	Worktrees []Worktree `json:"worktrees"`
	Error     string     `json:"error,omitempty"`
}

func run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Resolve trả thông tin git của thư mục dir; (nil, nil) nếu dir không thuộc
// git repo nào — caller hiển thị "không có repo", không phải lỗi scan.
func Resolve(dir string) (*RepoView, error) {
	out, err := run(dir, "rev-parse", "--show-toplevel", "--git-common-dir", "--git-dir")
	if err != nil {
		return nil, nil
	}
	lines := nonEmptyLines(out)
	if len(lines) < 3 {
		return nil, fmt.Errorf("git rev-parse output bất ngờ: %q", out)
	}
	v := &RepoView{TopLevel: strings.TrimSpace(lines[0])}
	// --git-dir trả đường dẫn tương đối theo dir; trong linked worktree nó trỏ
	// vào .git/worktrees/<tên> của repo chính → khác --git-common-dir.
	common := absPath(dir, lines[1])
	gitDir := absPath(dir, lines[2])
	v.IsWorktree = common != gitDir

	st, err := run(dir, "status", "--porcelain=v1", "--branch")
	if err != nil {
		return v, nil
	}
	for _, line := range strings.Split(st, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "## ") {
			v.Dirty++ // mỗi dòng porcelain = 1 file thay đổi/untracked
			continue
		}
		parseBranchLine(v, strings.TrimPrefix(line, "## "))
	}
	if v.Detached {
		if sha, err := run(dir, "rev-parse", "--short", "HEAD"); err == nil {
			v.Branch = "(detached @ " + strings.TrimSpace(sha) + ")"
		} else {
			v.Branch = "(detached)"
		}
	}
	return v, nil
}

// parseBranchLine parse dòng "## main...origin/main [ahead 1, behind 2]"
// hoặc "## HEAD (no branch)" (detached).
func parseBranchLine(v *RepoView, body string) {
	if strings.HasPrefix(body, "HEAD (no branch)") || strings.HasPrefix(body, "No commits yet on ") {
		v.Detached = strings.HasPrefix(body, "HEAD (no branch)")
		if !v.Detached {
			v.Branch = "(chưa có commit)"
		}
		return
	}
	if i := strings.Index(body, " ["); i >= 0 {
		parseAheadBehind(v, strings.TrimSuffix(body[i+2:], "]"))
		body = body[:i]
	}
	if i := strings.Index(body, "..."); i >= 0 {
		v.Upstream = body[i+3:]
		body = body[:i]
	}
	v.Branch = strings.TrimSpace(body)
}

func parseAheadBehind(v *RepoView, s string) {
	for _, part := range strings.Split(s, ", ") {
		part = strings.TrimSpace(part)
		n := 0
		fmt.Sscanf(part, "%*s %d", &n)
		switch {
		case strings.HasPrefix(part, "ahead"):
			v.Ahead = n
		case strings.HasPrefix(part, "behind"):
			v.Behind = n
		}
	}
}

// Branches trả branch local sort theo committerdate (mới nhất trước).
func Branches(dir string) ([]string, error) {
	out, err := run(dir, "for-each-ref", "refs/heads", "--sort=-committerdate", "--format=%(refname:short)")
	if err != nil {
		return nil, fmt.Errorf("liệt kê branch: %v\n%s", err, out)
	}
	ls := nonEmptyLines(out)
	if ls == nil {
		ls = []string{}
	}
	return ls, nil
}

// Worktrees liệt kê các worktree của repo; đánh dấu Current theo toplevel.
func Worktrees(dir string, currentTop string) ([]Worktree, error) {
	out, err := run(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("worktree list: %v\n%s", err, out)
	}
	wts := []Worktree{}
	var cur *Worktree
	flush := func() {
		if cur != nil {
			wts = append(wts, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			flush()
			continue
		}
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			p := strings.TrimPrefix(line, "worktree ")
			cur = &Worktree{Path: p, Branch: "(detached)"}
			if samePath(p, currentTop) {
				cur.Current = true
			}
		case strings.HasPrefix(line, "branch "):
			if cur != nil {
				cur.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
				cur.Detached = false
			}
		case line == "detached":
			if cur != nil {
				cur.Detached = true
			}
		case strings.HasPrefix(line, "HEAD "):
			// bỏ qua
		}
	}
	flush()
	return wts, nil
}

// Switch chuyển branch trong repo/worktree chứa dir. CỐ Ý không force,
// không stash, không discard — git tự từ chối khi có thay đổi đụng chạm,
// lỗi trả nguyên văn để UI hiện. Caller phải xác nhận trong UI trước khi gọi.
func Switch(dir, branch string) error {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return fmt.Errorf("tên branch rỗng")
	}
	// exec truyền arg trực tiếp (không qua shell) nhưng branch bắt đầu bằng "-"
	// sẽ bị git hiểu là flag — chặn rõ ràng thay vì để git báo lỗi khó hiểu.
	if strings.HasPrefix(branch, "-") {
		return fmt.Errorf("tên branch không hợp lệ: %q", branch)
	}
	out, err := run(dir, "switch", branch)
	if err != nil {
		return fmt.Errorf("git switch %s: %v\n%s", branch, err, out)
	}
	return nil
}

// Details gộp Resolve + Branches + Worktrees cho modal (gọi theo yêu cầu, không poll).
func Details(dir string) (*RepoDetails, error) {
	v, err := Resolve(dir)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, fmt.Errorf("%s không thuộc git repo", dir)
	}
	d := &RepoDetails{View: v, Branches: []string{}, Worktrees: []Worktree{}}
	if bs, err := Branches(dir); err == nil {
		d.Branches = bs
	}
	if ws, err := Worktrees(dir, v.TopLevel); err == nil {
		d.Worktrees = ws
	}
	return d, nil
}

func nonEmptyLines(out string) []string {
	ls := []string{}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			ls = append(ls, l)
		}
	}
	return ls
}

// absPath giải đường dẫn git trả về (có thể tương đối theo dir) thành tuyệt đối
// đã EvalSymlinks để so sánh — macOS /tmp là symlink của /private/tmp.
func absPath(base, p string) string {
	p = strings.TrimSpace(p)
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

func samePath(a, b string) bool {
	return absPath(".", a) == absPath(".", b)
}
