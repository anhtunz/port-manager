package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Repo tạm dùng cho test — KHÔNG bao giờ đụng repo thật của user.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-b", "main")
	git("config", "user.email", "test@local")
	git("config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "init")
	return dir
}

func TestResolveMainRepo(t *testing.T) {
	dir := initRepo(t)
	v, err := Resolve(dir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if v == nil {
		t.Fatal("Resolve trả nil với repo hợp lệ")
	}
	if v.Branch != "main" || v.Detached || v.IsWorktree || v.Dirty != 0 {
		t.Fatalf("sai: %+v", v)
	}
	if v.TopLevel == "" {
		t.Fatal("thiếu TopLevel")
	}
}

func TestResolveNotARepo(t *testing.T) {
	v, err := Resolve(t.TempDir())
	if err != nil {
		t.Fatalf("Resolve phải nil,nil với dir thường: %v", err)
	}
	if v != nil {
		t.Fatalf("kỳ vọng nil, nhận %+v", v)
	}
}

func TestDirtyCount(t *testing.T) {
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	v, _ := Resolve(dir)
	// 1 tracked sửa + 1 untracked
	if v.Dirty != 2 {
		t.Fatalf("kỳ vọng dirty=2, nhận %d", v.Dirty)
	}
}

func TestSwitchBranch(t *testing.T) {
	dir := initRepo(t)
	if out, err := exec.Command("git", "-C", dir, "branch", "feat/x").CombinedOutput(); err != nil {
		t.Fatalf("tạo branch: %v\n%s", err, out)
	}
	if err := Switch(dir, "feat/x"); err != nil {
		t.Fatalf("Switch: %v", err)
	}
	v, _ := Resolve(dir)
	if v.Branch != "feat/x" {
		t.Fatalf("kỳ vọng branch feat/x, nhận %s", v.Branch)
	}
	if err := Switch(dir, "main"); err != nil {
		t.Fatalf("Switch về main: %v", err)
	}
}

func TestSwitchInvalid(t *testing.T) {
	dir := initRepo(t)
	if err := Switch(dir, ""); err == nil {
		t.Fatal("switch branch rỗng phải lỗi")
	}
	if err := Switch(dir, "-weird"); err == nil {
		t.Fatal("switch branch bắt đầu bằng - phải bị chặn")
	}
	if err := Switch(dir, "không-tồn-tại"); err == nil {
		t.Fatal("switch branch không tồn tại phải lỗi")
	}
}

func TestSwitchRefuseWhenConflict(t *testing.T) {
	dir := initRepo(t)
	if out, err := exec.Command("git", "-C", dir, "branch", "other").CombinedOutput(); err != nil {
		t.Fatalf("tạo branch: %v\n%s", err, out)
	}
	// Sửa a.txt chưa commit — main và other cùng nội dung a.txt (không đụng chạm)
	// nên phải switch được; thay đổi theo theo sang other. Để test refusal, cần
	// 2 branch khác nội dung: tạo commit trên other rồi quay lại sửa a.txt.
	if err := Switch(dir, "other"); err != nil {
		t.Fatalf("switch sang other: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", dir, "add", ".").CombinedOutput(); err != nil {
		t.Fatal(err)
	} else {
		_ = out
	}
	if out, err := exec.Command("git", "-C", dir, "commit", "-m", "other content").CombinedOutput(); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
	if err := Switch(dir, "main"); err != nil {
		t.Fatalf("switch về main: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("dirty-main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// main giờ có a.txt=dirty-main chưa commit, other có a.txt=other → conflict
	if err := Switch(dir, "other"); err == nil {
		t.Fatal("git PHẢI từ chối switch khi conflict — app không được force")
	}
}

func TestWorktree(t *testing.T) {
	dir := initRepo(t)
	wtDir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "worktree", "add", wtDir, "-b", "wtbranch").CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}

	mainView, _ := Resolve(dir)
	if mainView.IsWorktree {
		t.Fatal("repo chính không được đánh dấu là worktree")
	}
	wtView, _ := Resolve(wtDir)
	if !wtView.IsWorktree {
		t.Fatal("linked worktree phải được đánh dấu isWorktree=true")
	}
	if wtView.Branch != "wtbranch" {
		t.Fatalf("worktree branch sai: %s", wtView.Branch)
	}

	wts, err := Worktrees(dir, mainView.TopLevel)
	if err != nil {
		t.Fatalf("Worktrees: %v", err)
	}
	if len(wts) != 2 {
		t.Fatalf("kỳ vọng 2 worktree, nhận %d: %+v", len(wts), wts)
	}
	var currentCount int
	for _, w := range wts {
		if w.Current {
			currentCount++
		}
	}
	if currentCount != 1 {
		t.Fatalf("kỳ vọng đúng 1 worktree current, nhận %d", currentCount)
	}

	// switch branch trong worktree không đụng repo chính. Lưu ý git CẤM check
	// out 1 branch ở 2 worktree cùng lúc (main đã bị repo chính giữ) → dùng
	// branch riêng cho worktree.
	if out, err := exec.Command("git", "-C", wtDir, "branch", "wtbranch2").CombinedOutput(); err != nil {
		t.Fatalf("tạo branch wtbranch2: %v\n%s", err, out)
	}
	if err := Switch(wtDir, "wtbranch2"); err != nil {
		t.Fatalf("switch trong worktree: %v", err)
	}
	mainView2, _ := Resolve(dir)
	if mainView2.Branch != "main" {
		t.Fatalf("repo chính phải vẫn ở main: %s", mainView2.Branch)
	}
	if wtView2, _ := Resolve(wtDir); wtView2.Branch != "wtbranch2" {
		t.Fatalf("worktree phải ở wtbranch2: %s", wtView2.Branch)
	}
}

func TestDetails(t *testing.T) {
	dir := initRepo(t)
	d, err := Details(dir)
	if err != nil {
		t.Fatalf("Details: %v", err)
	}
	if d.View.Branch != "main" || len(d.Branches) == 0 || len(d.Worktrees) == 0 {
		t.Fatalf("Details thiếu dữ liệu: %+v", d)
	}
	if _, err := Details(t.TempDir()); err == nil {
		t.Fatal("Details trên dir không phải repo phải lỗi")
	}
}
