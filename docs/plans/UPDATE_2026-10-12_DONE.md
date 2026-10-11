# UPDATE — Port Manager: xem branch/worktree + chuyển nhánh từng service

> **Phân loại: UPDATE** — mở rộng app `port-manager` đang có (thêm dữ liệu scan + bindings + UI modal).
> Branch: `update/git-branch-switch` (từ main). Bump version `y`: 0.1.0 → **0.2.0**.
> Trạng thái: `DONE` (2026-10-12) — xem phần **Kết quả** ở cuối file.

## 1. Bối cảnh

Workspace smatec là nhiều git repo (BE/FE riêng từng project) + có dùng worktree. User muốn nhìn
trên dashboard: service nào đang ở branch/worktree nào, dirty thế nào, và **chuyển nhánh ngay trong app**
thay vì mở terminal từng repo.

## 2. Scope

**Làm (v0.2.0):**
- Mỗi dòng service hiển thị chip `⎇ <branch>` + đếm thay đổi chưa commit (`●N`) + badge `wt` nếu là linked worktree.
- Modal chi tiết repo: đường dẫn toplevel, nhánh hiện tại (+upstream/ahead/behind), dirty, cảnh báo detached HEAD,
  danh sách **branch local** (sort theo committerdate) với nút "Chuyển", ô nhập nhánh khác (local hoặc `origin/...`),
  danh sách **các worktree** của repo (đánh dấu worktree hiện tại).
- `git switch` (KHÔNG force, KHÔNG stash, KHÔNG discard) + modal xác nhận; lỗi git trả nguyên văn lên UI.
- Config thêm `repo_dir` (tùy chọn) cho service mà `dir` không phải repo: `wm-be`, `facility-be`, `infra-wm-nginx`, `infra-fm-redis`.
- CLI: `-scan` in thêm branch; mới: `-branches <id>`, `-switch <id> <branch>`.
- Dedupe resolve git theo repo trong 1 lần quét (telegram-emulator BE/FE chung 1 repo...).

**Không làm (phase 2 nếu user muốn):**
- fetch/pull, tạo branch mới, rebase, merge, xoá branch.
- Auto-restart service sau khi switch.
- Force-switch / discard thay đổi.

## 3. Hợp đồng API (Wails bindings)

| Binding | Signature | Ý nghĩa |
| --- | --- | --- |
| `RepoDetails` | `RepoDetails(id string) *git.RepoDetails` | Chi tiết repo của service: view + branches + worktrees; `error` nếu không phải repo |
| `SwitchBranch` | `SwitchBranch(id, branch string) error` | `git switch` trong repo chứa service dir (có guard tên nhánh) |

`ServiceView` thêm `repo?: { topLevel, branch, detached, isWorktree, dirty, ahead, behind, upstream }` (nil nếu dir không thuộc repo).
`git.RepoDetails = { view, branches []string, worktrees [{path, branch, detached, current}], error? }`.

## 4. Các bước

1. `internal/git`: `Resolve` (1 call `rev-parse --show-toplevel --git-common-dir --git-dir` + 1 call `status --porcelain=v1 --branch`),
   `Branches` (for-each-ref sort committerdate), `Worktrees` (worktree list --porcelain), `Switch`, `Details`.
2. `config.Service` thêm `repo_dir`; `scan.ServiceView` thêm `Repo` + dedupe resolve theo repo dir.
3. `app.go`: bindings; `cli.go`: `-branches`, `-switch`, hiển thị branch trong `-scan`.
4. Frontend: chip trên dòng, modal repo, action switch, CSS.
5. Test: unit `internal/git` trên repo tạm (branch/dirty/switch/worktree/không-phải-repo); E2E `-scan`/`-branches`
   trên repo thật (chỉ đọc); verify UI qua browser mock; đo thời gian scan trước/sau.
6. Version 0.2.0 + changelog + merge main.

## 5. Tác động tiềm ẩn

| # | Vấn đề | Đề xuất | Chốt |
| --- | --- | --- | --- |
| 1 | Switch branch khi service đang chạy → code cũ vẫn chạy tới khi restart | Modal cảnh báo rõ; user tự restart service từ app sau khi switch | |
| 2 | Repo dirty → git tự từ chối switch nếu conflict | Hiện lỗi git nguyên văn; app KHÔNG tự stash/force/discard | |
| 3 | Quét git mỗi 3s có thể chậm trên repo lớn (WM FE) | Đo thực tế; nếu scan > ~1s mới thêm throttle dirty-check | |
| 4 | Detached HEAD (sau checkout tag/commit) | Hiển thị `(detached @ sha)` để không hiểu nhầm là branch | |
| 5 | Worktree isolation | `git switch` chỉ tác động worktree chứa service dir, không đụng worktree khác (đặc tính git) | |
| 6 | Rủi ro agent tự test switch trên repo thật của user | CẤM — chỉ switch trong repo tạm của unit test; repo thật chỉ đọc | |

## 6. Tiêu chí nghiệm thu

**Tự động/tự test:**
- `go test ./internal/git` xanh: branch/dirty/switch/worktree/detected-not-repo.
- `-scan` hiện đúng branch từng repo thật (auth BE, WM BE/FE, portal...); repo không tồn tại → ẩn chip, không lỗi scan.
- `-branches portal-fe` in chi tiết; đo thời gian `-scan` trước/sau (đánh giá tác động mục 5.3).
- UI mock: chip + modal render đúng (ảnh browser).

**User test thủ công:**
1. Mở app → chip branch đúng thực tế từng service (so `git branch --show-current` tay).
2. Click chip → modal hiện branches/worktrees đúng.
3. Chuyển một service sang nhánh khác (repo sạch) → chip cập nhật sau ~3s; service đang chạy giữ nguyên.
4. Tạo thay đổi chưa commit rồi switch sang nhánh đụng cùng file → app hiện lỗi git nguyên văn, không mất gì.
5. Switch về nhánh cũ + restart service từ app nếu muốn chạy code mới.

---

## Kết quả (DONE — 2026-10-12)

**Đã làm:** package `internal/git` mới (`Resolve`/`Branches`/`Worktrees`/`Switch`/`Details`, worktree
detect qua `--git-common-dir` ≠ `--git-dir`); `ports.yaml` thêm `repo_dir` (wm-be, facility-be,
infra-wm-nginx, infra-fm-redis) + `ServiceView.Repo` với dedupe resolve theo repo; bindings
`RepoDetails`/`SwitchBranch`; CLI thêm `-branches`, `-switch`, `-scan` in thêm ⎇ branch; FE chip
`⎇ branch` + modal repo (branches/worktrees/ô nhập nhánh/cảnh báo dirty + service đang chạy).

**File chính:** `internal/git/git.go`, `internal/git/git_test.go` (mới); `internal/config/config.go`,
`internal/scan/scan.go`, `app.go`, `cli.go`, `main.go`, `ports.yaml`, `frontend/dist/{app.js,style.css,index.html}`,
`README.md`, `VERSION` (mới: 0.2.0).

**Version:** 0.1.0 → **0.2.0** (UPDATE → bump `y`).

**Kết quả build/test:**
- `go test ./...` xanh (unit git 7 test trên repo tạm: branch/dirty/switch/refuse-conflict/worktree/details).
- E2E thật (chỉ đọc): `-scan` hiện đúng branch 13 service (auth master, assistant
  update/generic-api-prompt, base BE/FE update/*, wm-be/fm-redis qua repo_dir, docker-shared ẩn chip);
  thời gian scan ~0.83s — git không làm scan chậm đáng kể (tác động 5.3 không cần throttle).
- Guard `-switch`: tên nhánh bất hợp lệ (`-x`) bị chặn; nhánh không tồn tại → lỗi git nguyên văn; không switch repo thật.
- UI mock qua browser: chip render đúng 5 service có repo, ẩn với service không repo; modal đúng cả
  kịch bản worktree (wm-be) lẫn dirty (auth-be); ảnh screenshot modal xác nhận layout.

**Case test thủ công cho user:** như mục "User test thủ công" ở trên (5 bước).
