# Port Manager — quản lý cổng dev workspace smatec

App desktop macOS (Go + Wails) để xem service nào đang chạy trên cổng nào trong workspace
`smatec/`, tắt/bật từng service, và phát hiện process "ma" chiếm cổng lạ.

> **Nguồn sự thật cổng:** bảng quy hoạch port ở `AGENTS.md` mục 10 (root workspace).
> File `ports.yaml` của app chỉ là **mirror** — đổi port phải sửa AGENTS.md TRƯỚC rồi mới mirror sang đây.

## Tính năng (v0.4.0)

- Quét trạng thái từng service theo 3 nhóm: **Backend Go** · **Frontend** · **Hạ tầng (docker)**,
  nhận diện PID + full command của process đang lắng nghe (`lsof` + `ps`, không cần cài thêm gì).
- **Thời gian chạy**: mỗi dòng service hiện `▶ lúc bắt đầu · đã chạy được bao lâu`
  (process lấy từ `ps lstart`, container lấy từ `docker inspect State.StartedAt`);
  phần "đã chạy" tự tăng mỗi giây. CLI `-scan` cũng in thời gian chạy.
- **Tắt / Restart / Bật**: mọi service đang có process đều có nút **Tắt** và **🔄 Restart**
  (tắt rồi bật lại). Trạng thái phân biệt rõ:
  **Đang chạy** · **Một phần** · **Đang khởi động** (vừa bật, chưa lên cổng) ·
  **Kẹt** (đã bật > 90s mà vẫn chưa lên cổng) · **Tắt**.
  Nhờ vậy **service kẹt trong lúc khởi động vẫn tắt & restart được** — trước đây nó bị
  hiện như "Tắt" nên không có nút Tắt, bấm "Bật" lại thì báo *"đang chạy do app quản lý"*.
- **State file** `~/.port-manager/state/<id>.proc.json` nhớ process group của service do app bật →
  **app mở lại hoặc CLI `-stop` vẫn tắt được** service kẹt (chưa lên cổng, không có listener để kill).
  Có chống kill nhầm khi pid bị tái sử dụng (so process group + thời điểm bắt đầu).
- **Bật** theo lệnh start khai trong `ports.yaml` (`make` / `npm run dev`), process chạy ở process group riêng, log ghi vào `logs/<id>.log`.
  Stdin của process con được giữ mở (pipe + fd 3) → các service cần stdin sống (ví dụ
  **umi max**: plugin tailwind abort khi stdin EOF) vẫn lên cổng đúng khi bật từ app.
  Log **không bị xoá** khi bật lại (ghi thêm dòng mốc `=== bật lúc ... ===`) → còn xem được
  log của lần chạy trước, rất cần khi service fail lúc khởi động.
- Hạ tầng docker điều khiển qua `docker compose stop` / `up -d` đúng compose_dir/service — không kill lung tung bên trong container.
- **Log realtime**: modal Log tự làm mới mỗi giây và tự cuộn xuống đáy; cuộn lên thì bỏ ghim
  (log mới không giật con trỏ), nút **"↓ Cuối"** để nhảy về đáy.
- Mục **"Cổng lạ"**: listener không nằm trong danh sách cấu hình (lọc bớt noise hệ thống qua
  `unknown_ignore_commands` + `unknown_ignore_ports`) — chỗ bắt vite ma / process ma.
  Mỗi dòng cổng lạ hiện **nguồn gốc**: `📁 thư mục làm việc` của process (`lsof -d cwd`)
  hoặc `🐳 container <tên> · <thư mục project>` khi container publish cổng ra host
  (`docker ps`), kèm thời gian chạy. Cổng lạ do container giữ chỉ dừng được bằng
  `docker stop <container>` — không kill pid của Docker Desktop.
- Auto-refresh 3 giây (tắt được ở toolbar).
- **Git branch/worktree**: mỗi service có repo hiển thị chip `⎇ branch` kèm `●N` (số thay đổi
  chưa commit) và `wt` (linked worktree); bấm chip mở modal chi tiết repo — danh sách branch local
  (chuyển bằng 1 click có xác nhận), ô nhập branch khác (Enter để chuyển), danh sách worktree.
  Repo của service xác định qua `dir`, khác thì khai `repo_dir` trong `ports.yaml`.
  Chỉ `git switch` thuần — KHÔNG force/stash/discard; repo bẩn mà git từ chối thì báo lỗi nguyên văn.

## Build & chạy

```bash
make build   # go build -tags desktop,production + gói build/PortManager.app
make run     # build rồi mở app
make scan    # quét headless in bảng ra terminal (không mở cửa sổ)
make test    # go test ./...
```

Yêu cầu: Go ≥ 1.25 (máy dev chuẩn hoá Go 1.26.8 ở `~/sdk/go1.26.8`), Xcode CLT (cgo),
Docker Desktop đang chạy nếu cần điều khiển hạ tầng.

> Repo nằm trong workspace có `go.work` ở root → mọi lệnh go trong Makefile đã export `GOWORK=off`.
> Nếu chạy lệnh go thủ công bên ngoài make, nhớ `GOWORK=off go ...`.

## CLI headless

```bash
./build/port-manager -scan                    # in trạng thái toàn bộ service + cổng lạ (+ ⎇ branch)
./build/port-manager -start portal-fe
./build/port-manager -stop portal-fe
./build/port-manager -restart portal-fe        # tắt rồi bật lại
./build/port-manager -branches base-be        # chi tiết repo: branch, worktree, dirty
./build/port-manager -switch base-be master   # chuyển nhánh (git switch thuần, không force)
./build/port-manager -version
```

## Lưu ý vận hành

- Service do app bật **không tự tắt khi quit app** — thiết kế có chủ ý; muốn dọn thì tắt từ app
  (mở lại) hoặc `make stop` của service tương ứng. Log còn lại trong `logs/`.
- App GUI launch từ Finder có PATH tối thiểu → app tự bơm PATH lấy từ login shell (`zsh -lc`);
  nếu service vẫn không bật được, mở Log của service để xem lỗi thực tế.
- Nhóm "Hạ tầng": **Tắt** = `docker compose stop` (container được giữ, không xoá volume),
  **Bật** = `docker compose up -d`.

## Changelog

## v0.4.0 - 2026-10-21

- (FEAT) **Tắt & restart được service kẹt trong lúc khởi động.** Trước đây service
  đang chạy mà chưa lên cổng bị hiện như "Tắt" → không có nút Tắt, bấm "Bật" lại thì
  báo *"đang chạy do app quản lý"* → không có đường nào thoát ra.
- (FEAT) Trạng thái mới **"Đang khởi động"** (vừa bật < 90s, chưa lên cổng) và
  **"Kẹt"** (đã bật > 90s mà vẫn chưa lên cổng) — thay vì gộp chung vào "Tắt".
- (FEAT) Nút **🔄 Restart** cho mọi service đang có process; nút **Tắt** hiện cho cả
  trạng thái "đang khởi động"/"kẹt". Dòng process hiện pid của app khi chưa có listener.
- (FEAT) **State file** `~/.port-manager/state/<id>.proc.json` nhớ process group của
  service do app bật → app mở lại hoặc CLI `-stop` vẫn tắt được service kẹt (trước đây
  không tắt được bằng bất kỳ đường nào vì chưa lên cổng nên không có listener để kill).
  Có chống kill nhầm khi pid bị tái sử dụng.
- (FEAT) CLI `-restart <id>`; `-start` khi service đang chạy giờ báo rõ pid + hướng
  user tới Restart thay vì lỗi chung chung.
- (FIX) Log **không bị xoá** khi bật lại (ghi thêm dòng mốc `=== bật lúc ... ===`) →
  còn xem được log của lần chạy trước, cần thiết khi service fail lúc khởi động.
- (FIX) File log của process đã chết được đóng lại (không rò fd).

## v0.3.0 - 2026-10-21

- (FIX) Mục **"Cổng lạ"** giờ chỉ ra **nguồn gốc** thay vì chỉ pid + command:
  `📁 <thư mục làm việc>` (đọc bằng `lsof -d cwd`) hoặc `🐳 <container> · <project>`
  (container đang publish cổng ra host, đọc bằng `docker ps`), kèm thời gian chạy.
- (FIX) **An toàn**: cổng lạ do container giữ trước đây bấm "Tắt process" là kill pid
  `com.docker.backend` — tức tắt cả Docker Desktop; nay nút là **"Tắt container"**
  (`docker stop <container>`).
- (FIX) Đồng bộ `ports.yaml` với AGENTS.md mục 10: thêm `birthday-nginx` (8088,
  `others/birthday`) và `assistant-service/frontend` (5180, `vite.config.ts` đã đặt
  cứng) → hai cổng này không còn bị báo là "lạ".

- (FEAT) Hiện **thời gian bắt đầu chạy** và **thời gian đã chạy** của từng service
  (process: `ps lstart`; container: `docker inspect State.StartedAt`), phần "đã chạy" tăng mỗi giây;
  CLI `-scan` cũng in.
- (FIX) **FE Umi (umi max) không khởi động được từ app**: stdin của process con bị EOF
  (Go đưa `/dev/null` khi `cmd.Stdin = nil`) → watcher tailwind của umi abort → CSS không sinh →
  `tailwindcss generate failed` → `max dev` chết. Giờ spawn với pipe stdin + `ExtraFiles` (fd 3)
  để cây process con giữ đầu ghi — stdin không EOF kể cả khi app/CLI đã thoát.
- (FEAT) **Log realtime**: modal Log tự làm mới mỗi giây, tự cuộn xuống đáy, bỏ ghim khi user
  cuộn lên, nút "↓ Cuối"; đóng modal thì dọn timer.
- (FIX) Auto-refresh 3s không còn ghi đè `innerHTML` khi nội dung không đổi → nút bấm giữa chừng
  không bị "chết" (sửa lỗi click vào nút không ăn).

## v0.2.0 - 2026-10-12

- (UPDATE) Xem branch/worktree của từng service (chip ⎇ + modal repo) và chuyển nhánh local
  từ app (`git switch` thuần, không force/stash/discard); CLI thêm `-branches <id>` / `-switch <id> <branch>`;
  `ports.yaml` thêm trường `repo_dir` cho service mà thư mục chạy ≠ gốc repo.

## v0.1.0 - 2026-10-11

- (FEAT) Bản đầu tiên: dashboard 3 nhóm service, bật/tắt theo config, kill process ma, CLI headless.
