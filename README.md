# Port Manager — quản lý cổng dev workspace smatec

App desktop macOS (Go + Wails) để xem service nào đang chạy trên cổng nào trong workspace
`smatec/`, tắt/bật từng service, và phát hiện process "ma" chiếm cổng lạ.

> **Nguồn sự thật cổng:** bảng quy hoạch port ở `AGENTS.md` mục 10 (root workspace).
> File `ports.yaml` của app chỉ là **mirror** — đổi port phải sửa AGENTS.md TRƯỚC rồi mới mirror sang đây.

## Tính năng (v0.6.0)

- **Icon menu bar (status item)**: radar ở thanh menu macOS, bấm là xổ menu nhanh —
  Mở Port Manager · Quét lại · Bật tất cả · Tắt tất cả · Giới thiệu · Thoát — hành vi
  và hộp thoại confirm giống hệt menu ứng dụng. Icon là ảnh *template* (đen + alpha)
 nên tự đảo màu theo menu bar sáng/tối; code nằm ở `internal/tray` (NSStatusItem qua cgo).
- **Logo riêng cho app**: icon "lưới service" (4 khối + badge xanh "đang chạy") hiện ở
  header GUI, Dock và Finder — `assets/appicon.png` (1024px) được vẽ bằng generator
  thuần Go `make icons`, đóng gói thành `AppIcon.icns` lúc `make build`.
- **Thao tác theo nhóm**: đầu mỗi group có 2 nút **Bật/Tắt nhóm**, toolbar có
  **Bật tất cả / Tắt tất cả** — bật/tắt theo thứ tự `tier` khai trong `ports.yaml`
  (`0` Hạ tầng → `1` Backend Go → `2` Frontend). Bật nhóm chạy lần lượt, mỗi service
  chờ cổng lên tối đa 20s; service lỗi không chặn service sau; service đã chạy thì bỏ qua.
- **Tìm kiếm & lọc**: ô tìm kiếm (tên/id/số cổng/lệnh, nhiều từ ghép bằng VÀ), lọc
  trạng thái ("đang chạy" gồm cả "Một phần"), lọc repo bẩn. Bộ lọc được áp ngay lúc
  dựng HTML nên **không mất** sau mỗi vòng quét 3s. Trạng thái giao diện (bộ lọc +
  group đang thu gọn) giữ qua localStorage (`port-manager-ui-v1`) giữa các lần mở app.
- **Dòng lỗi cuối ngay trên dòng service** (cột thư mục): đọc 256KB cuối log, chỉ xét
  đoạn sau mốc `=== bật lúc ... ===` cuối, phân biệt theo mức log (`level=error`,
  `level=warn` không tính là lỗi); đọc lại có giới hạn 1 lần/10s mỗi file.
- **Ẩn cổng lạ bằng 1 nút**: ghi cổng vào `unknown_ignore_ports` trong `ports.yaml`
  bằng sửa text theo dòng — giữ nguyên comment/định dạng, idempotent, không tự tạo file.
- **Nút Terminal**: mở Terminal.app tại đúng thư mục của service (`osascript … do script "cd …"`).
- **Bảng đối chiếu mirror**: nút Mirror mở bảng so `ports.yaml` với quy hoạch
  AGENTS.md mục 10.1. Trình đọc bảng loại đúng cột gRPC/preview, ô đặt trong ngoặc,
  dải `fe-*` 5173–5179 và hàng "Mẹo nhớ" → không báo lệch giả; lệch hiển thị kèm ghi chú.
- **Chip sức khoẻ HTTP** cạnh mỗi chip cổng: `ok` · `http_error <code>` · `no_response` ·
  `skipped`. Probe song song (tối đa 8 đồng thời, timeout 2s); service compose và cổng
  gRPC khai `health_skip_ports` được skip; chạy tự động sau mỗi lần quét + có nút chạy tay.
- **Tự bật lại service bị crash**: chỉ service do app bật **và** có `auto_restart: true`.
  Backoff 5s → 10s → 20s (trần 60s), tối đa 3 lần; user vừa tắt có chủ ý thì KHÔNG bật lại;
  sống ổn định 5 phút thì reset bộ đếm. Bộ đếm nằm trong RAM → app đóng là dừng hẳn,
  không có tiến trình nền.
- **Thông báo macOS** khi service kẹt trong vòng theo dõi hoặc crash (gửi qua
  `osascript`; tắt được bằng biến môi trường `PORT_MANAGER_NOTIFY`).
- **Lịch sử chạy** của từng service (JSONL tại `~/.port-manager/history/<id>.history.jsonl`):
  Bật/Tắt/Restart/Crash/Kẹt/Lỗi/Sửa cấu hình đều ghi lại, modal xem được trong app,
  CLI `-history <id>`; file vượt 256KB tự rotate giữ 200 dòng cuối; app chết giữa lúc
  ghi chỉ hỏng tối đa 1 dòng.
- **Thêm/sửa service từ app**: modal Add/Edit ghi `ports.yaml` bằng sửa text theo dòng
  (giữ comment, flow style `ports: [a, b]`, thứ tự group/service) — validate trước khi
  ghi (port trùng → báo lỗi, file không đổi); nút **Gợi ý cổng trống** chỉ lấy cổng có
  trong bảng quy hoạch AGENTS.md mục 10.1; nút **nạp lại ports.yaml** đổi config nóng.
- **Menu ứng dụng macOS native** trên thanh menu (Quét lại ⌘R · Bật tất cả ⌘⇧B ·
  Tắt tất cả ⌘⇧T · Giới thiệu) + phím tắt trong cửa sổ app (⌘R quét lại,
  ⌘F/⌘K nhảy vào ô tìm kiếm).
- **CLI mới**: `-start-group <tên>`, `-stop-all`, `-json` (in kết quả quét dạng JSON),
  `-history <id>`; exit code `0` thành công · `1` lỗi thao tác · `2` sai cú pháp/tham số.
- `ports.yaml` thêm field tuỳ chọn `tier`, `auto_restart`, `health_path`,
  `health_skip_ports` — file cũ **không có** các field này vẫn đọc/ghi được như thường.

**Giới hạn đã biết của đợt này (chưa làm / làm một phần):**

- Bật nhóm **tuần tự** trong group (không song song trong tier như kế hoạch gốc —
  logic bật/tắt nằm thẳng trong `app.go`, chưa tách package `internal/orchestrator` riêng);
  tốc độ thực tế vẫn ổn vì mỗi service chỉ chờ tối đa 20s.
- Thông báo đi bằng `osascript` — **chưa** dùng `runtime.SendNotification` của Wails
  và chưa xin quyền thông báo hệ thống (hàm đó gọi `log.Fatalf` khi ctx thường):
  thông báo hiện dưới tên "Script Editor".
- `MirrorReport` JSON chỉ có `{mismatches, checkedAt}` — chưa có `note` cấp report và
  cột service/group trong từng lệch (frontend tự ghép service/group từ kết quả quét).
- Form sửa service **chưa có ô** `health_path`/`health_skip_ports` (hợp đồng
  `ServiceSpec` chỉ có 11 field) — sửa bằng tay trong `ports.yaml` rồi bấm nạp lại.
- 2 binding dự phòng `SaveUIState`/`LoadUIState` (mục C5 hợp đồng) **chưa làm** — chỉ cần khi
  localStorage không bền trong `.app` đóng gói; chưa test tay trong `.app` (mục 6.6 plan).
- Chưa có: test cho package `main` (`app_test.go`), test khoá JSON cho `internal/api`
  (`api_test.go`), target `make json`, và `scripts/package-mac.sh` chưa copy `ports.yaml`
  vào bundle (tạm chưa bị chặn nhờ fallback tìm config theo workspace `$HOME/Documents/smatec`).
- Phát hiện **stuck** nằm trong app (`app.go`; supervisor chỉ can thiệp crash) —
  đúng ý thiết kế: lịch sử thông báo "kẹt" vẫn hoạt động đầy đủ.

## Tính năng (v0.5.0)

- Quét trạng thái từng service theo 3 nhóm: **Backend Go** · **Frontend** · **Hạ tầng (docker)**,
  nhận diện PID + full command của process đang lắng nghe (`lsof` + `ps`, không cần cài thêm gì).
- **Bấm cổng → mở trình duyệt**: chip cổng đang mở (màu xanh) bấm được → app mở
  `http://localhost:<port>` bằng trình duyệt mặc định của hệ điều hành. Áp dụng cho cả
  cổng của service trong bảng lẫn cổng ở mục **"Cổng lạ"**. Cổng đang tắt thì chip xám,
  không bấm được.
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
  (log mới không giật con trỏ), nút **"↓ Cuối"** để nhảy về đáy. **Copy được**: bôi đen
  (log tạm dừng cập nhật khi đang chọn, ⌘C hoạt động nhờ menu Edit) hoặc bấm nút
  **"Copy"** (copy vùng chọn, không thì copy toàn bộ dòng đang hiện qua clipboard hệ
  thống). **Lọc lỗi**: ô lọc theo từ khóa/mã lỗi (nhiều từ = AND) + nút **"Chỉ lỗi"**
  (error/fatal/panic/connection refused…), dòng lỗi tô đỏ, dòng warn tô vàng, hiện
  "N/M dòng · K lỗi".
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
make test    # go test ./... + node --test frontend/test/
make agent-install    # build rồi cài LaunchAgent chạy ngầm (xem mục "Chạy ngầm")
make agent-status     # trạng thái LaunchAgent (exit 1 nếu chưa chạy)
make agent-uninstall  # gỡ LaunchAgent
make last-exit        # vì sao app tắt lần trước (quit / signal / tắt đột ngột)
```

Yêu cầu: Go ≥ 1.25 (máy dev chuẩn hoá Go 1.26.8 ở `~/sdk/go1.26.8`), Xcode CLT (cgo),
Docker Desktop đang chạy nếu cần điều khiển hạ tầng.

> Repo nằm trong workspace có `go.work` ở root → mọi lệnh go trong Makefile đã export `GOWORK=off`.
> Nếu chạy lệnh go thủ công bên ngoài make, nhớ `GOWORK=off go ...`.

## CLI headless

```bash
./build/port-manager -scan                    # in trạng thái toàn bộ service + cổng lạ (+ ⎇ branch)
./build/port-manager -json                    # in kết quả quét dạng JSON (cho script, jq được)
./build/port-manager -start portal-fe
./build/port-manager -stop portal-fe
./build/port-manager -restart portal-fe        # tắt rồi bật lại
./build/port-manager -start-group "Backend Go"  # bật lần lượt cả group (chờ cổng ≤ 20s/service)
./build/port-manager -stop-all                 # tắt toàn bộ workspace (ngược thứ tự tier)
./build/port-manager -history base-be          # in lịch sử chạy của service (mới nhất trước)
./build/port-manager -branches base-be        # chi tiết repo: branch, worktree, dirty
./build/port-manager -switch base-be master   # chuyển nhánh (git switch thuần, không force)
./build/port-manager -install-agent           # cài LaunchAgent chạy ngầm (chép binary + plist + bootstrap)
./build/port-manager -uninstall-agent         # gỡ LaunchAgent
./build/port-manager -agent-status            # trạng thái LaunchAgent (exit 1 nếu chưa cài/chưa chạy)
./build/port-manager -last-exit               # lý do app thoát lần trước
./build/port-manager -version
```

## Lưu ý vận hành

- Service do app bật **không tự tắt khi quit app** — thiết kế có chủ ý; muốn dọn thì tắt từ app
  (mở lại) hoặc `make stop` của service tương ứng. Log còn lại trong `logs/`.
- App GUI launch từ Finder có PATH tối thiểu → app tự bơm PATH lấy từ login shell (`zsh -lc`);
  nếu service vẫn không bật được, mở Log của service để xem lỗi thực tế.
- Nhóm "Hạ tầng": **Tắt** = `docker compose stop` (container được giữ, không xoá volume),
  **Bật** = `docker compose up -d`.

## Chạy ngầm (v0.7.0)

App được thiết kế để **sống lâu như một tiến trình nền**: đóng cửa sổ không làm app thoát, và
có thể giao cho `launchd` giữ app sống.

- **Nút đỏ chỉ ẩn cửa sổ** (`HideWindowOnClose`). Mở lại bằng icon trên menu bar, bấm Dock,
  hoặc mở app lần nữa (bản thứ hai sẽ đánh thức bản đang chạy rồi tự thoát). Muốn tắt thật:
  **⌘Q**, menu **Chạy ngầm → Thoát hẳn Port Manager**, hoặc menu bar → Thoát.
- **Một bản duy nhất**: khoá `flock` ở `~/.port-manager/state/app.lock`; bản thứ hai gửi
  `SIGUSR1` cho bản đang chạy để hiện cửa sổ rồi thoát mã 0.
- **LaunchAgent `vn.smatec.port-manager`** (nút **Chạy ngầm** trên toolbar, menu
  **Chạy ngầm**, hoặc `make agent-install`): chép binary hiện tại vào
  `~/.port-manager/bin/port-manager`, ghi plist vào `~/Library/LaunchAgents/` với `RunAtLoad`
  (tự mở khi đăng nhập) và `KeepAlive{SuccessfulExit:false}` (tự mở lại khi bị tắt đột ngột —
  crash, `kill -9`; thoát có chủ ý mã 0 thì KHÔNG mở lại). stdout/stderr của app ghi ở
  `~/.port-manager/logs/agent.{out,err}.log`. Cài từ GUI là **bàn giao**: app đang chạy tự thoát
  sau ~1.5s, launchd mở bản ngầm lên thay. Binary là **bản chụp** — sau `make build` cần cài lại
  ("Cài lại (bàn giao)") để chạy bản mới. Không cài được khi đang chạy qua `go run` (binary tạm).
- **Lý do thoát lần trước**: app ghi `app.session.json` khi mở và `app.lastexit.json` khi thoát
  sạch / nhận `SIGTERM`/`SIGINT`/`SIGHUP`. Lần mở sau còn sót session ⇒ lần trước **tắt đột
  ngột** → banner vàng đầu trang giải thích (và `-last-exit` ở CLI). Nguyên nhân thường gặp trong
  workspace: Makefile của service khác chạy `lsof -ti:<port> | xargs kill -9` để dọn cổng —
  app giữ kết nối keep-alive tới cổng đó (probe sức khoẻ) nên bị giết nhầm. Từ v0.7.0 app đóng
  kết nối rảnh ngay sau mỗi vòng probe; phía Makefile nên dùng `lsof -ti:<port> -sTCP:LISTEN`
  để chỉ giết tiến trình đang **lắng nghe**.

## Changelog

## v0.7.0 - 2026-10-25

- (FEAT) **Chạy ngầm — LaunchAgent**: package `internal/daemon` cài/gỡ LaunchAgent
  `vn.smatec.port-manager` (chép binary vào `~/.port-manager/bin`, plist `RunAtLoad` +
  `KeepAlive{SuccessfulExit:false}`, PATH đang có được ghi vào `EnvironmentVariables` để
  service dưới launchd vẫn thấy `make/go/npm`); cài từ GUI là bàn giao (script tách
  `setsid` bootout→bootstrap→kickstart sau 2s, app tự thoát sau 1.5s). Binding
  `AgentStatus`/`AgentInstall`/`AgentUninstall`; menu **Chạy ngầm** (Trạng thái… / Cài /
  Gỡ / Thoát hẳn); toolbar nút **Chạy ngầm** + modal `#agent-modal`; CLI
  `-install-agent` / `-uninstall-agent` / `-agent-status`; make `agent-install` /
  `agent-status` / `agent-uninstall`. Từ chối cài khi chạy qua `go run`.
- (FEAT) **Đóng cửa sổ chỉ ẩn, một bản duy nhất**: `HideWindowOnClose` (nút đỏ không thoát
  app — ⌘Q / menu Thoát hẳn / menu bar mới thoát); khoá `flock`
  `~/.port-manager/state/app.lock`, bản thứ hai gửi `SIGUSR1` đánh thức bản đang chạy rồi
  thoát 0 (bản do launchd mở mà trùng thì thoát 3 để KeepAlive thử lại — cơ chế bàn giao).
- (FEAT) **Lý do thoát lần trước**: package `internal/exitlog` ghi `app.session.json` lúc
  mở, `app.lastexit.json` lúc thoát sạch / `SIGTERM`/`SIGINT`/`SIGHUP` (handler ghi rồi
  thoát 0 để launchd không mở lại khi dừng có chủ ý); session sót ⇒ "tắt đột ngột". Binding
  `LastExit`, banner vàng `#exit-banner` đầu trang (không tự ẩn), CLI `-last-exit`, make
  `last-exit`.
- (FIX) **Không còn bị `kill -9` nhầm khi service khác dọn cổng**: `health.Checker` đóng
  kết nối keep-alive rảnh (`CloseIdleConnections`) sau mỗi vòng probe — trước đó app giữ
  kết nối idle 30s tới cổng service, `lsof -ti:<port> | xargs kill -9` trong Makefile của
  service bắt luôn pid app. Test `TestProbeAllDongKetNoiNhanRoi` (0 kết nối mở sau probe).
  Khuyến nghị Makefile các service dùng `lsof -ti:<port> -sTCP:LISTEN`.
- (FEAT) Hàm thuần `exitBanner`/`agentSummary`/`agentRows`/`installLabel`/`fmtTime` trong
  `app.logic.js` + 10 test node (`frontend/test/agent.logic.test.js`); `make test` chạy
  cả `node --test frontend/test/`.

## v0.6.0 - 2026-10-22

- (FEAT) **Modal log copy + lọc lỗi được**: bôi đen log để copy (log tạm dừng cập nhật
  khi đang chọn — không còn mất vùng chọn vì poll 1s ghi đè `textContent`), nút Copy
  (vùng chọn hoặc toàn bộ dòng đang hiện) qua clipboard hệ thống (binding `CopyText` /
  `runtime.ClipboardSetText`); menu macOS thêm **Edit** (⌘C/⌘V/⌘A — thiếu menu này
  WKWebView không nhận phím copy); kéo bôi đen thả ra ngoài modal không còn đóng modal.
  Lọc log: ô từ khóa (nhiều từ = AND, tìm mã lỗi 500/ECONNREFUSED…) + nút "Chỉ lỗi";
  dòng lỗi tô đỏ / warn tô vàng, đếm "N/M dòng · K lỗi". Logic thuần
  `logLineLevel`/`filterLogLines` trong `app.logic.js` + 6 test node --test.

- (FEAT) **Thao tác theo nhóm**: nút Bật/Tắt nhóm ở đầu mỗi group; nút Bật tất cả / Tắt
  tất cả trên toolbar — bật/tắt theo thứ tự `tier` của group (Hạ tầng → Backend Go →
  Frontend). Bật nhóm chạy lần lượt, mỗi service chờ cổng lên tối đa 20s; service lỗi
  không chặn service sau; service đã chạy được bỏ qua. 13 binding Wails mới
  (`StartGroup`, `StopGroup`, `StartAll`, `StopAll`, `HideUnknownPort`, `OpenTerminal`,
  `MirrorReport`, `HealthCheckAll`, `History`, `SaveService`, `SuggestPort`,
  `ReloadConfig`, `SetAutoRestart`); config đọc/ghi qua `RWMutex` (an toàn khi frontend
  poll 3s trong khi binding đang ghi config).
- (FEAT) **Tìm kiếm & lọc + giữ trạng thái giao diện**: ô tìm kiếm (tên/id/cổng/lệnh),
  lọc trạng thái (`all|running|starting|stuck|stopped`, `running` gồm cả "Một phần"),
  lọc repo bẩn; lọc áp ngay lúc dựng HTML nên không mất sau mỗi vòng quét 3s; bấm tiêu
  đề group thu gọn/mở; toàn bộ state (lọc + group thu gọn) lưu localStorage
  `port-manager-ui-v1`. Có test node --test (`frontend/test/app.logic.test.js`, 14 test).
- (FEAT) **Dòng lỗi cuối trên dòng service**: package `internal/lastlog` đọc 256KB cuối
  log, chỉ tính đoạn sau mốc `=== bật lúc ... ===` cuối, nhận biết mức log
  (`level=error` của JSON/zap/text là lỗi, `level=warn` thì không), giới hạn đọc lại
  1 lần/10s/file; `ServiceView.lastError` điền trong `App.Scan` (FE: `span.last-error`).
- (FEAT) **Ẩn cổng lạ bằng 1 nút**: `internal/config.AddIgnorePort` ghi cổng vào
  `unknown_ignore_ports` bằng sửa text theo dòng (giữ comment/format, idempotent);
  FE: nút Ẩn trên từng dòng cổng lạ.
- (FEAT) **Nút Terminal**: mở Terminal.app tại đúng thư mục service qua
  `osascript … do script "cd …"` (backend nhận id, tự resolve đường dẫn).
- (FEAT) **Bảng đối chiếu mirror**: package `internal/mirror` parse bảng quy hoạch
  AGENTS.md mục 10.1 (chọn cột theo tiêu đề, loại cột gRPC/preview, dải `fe-*`
  5173–5179, ô ngoặc, hàng "Mẹo nhớ") rồi so với `ports.yaml`; binding `MirrorReport`
  + panel `#mirror-panel` (nút Mirror). Trên workspace thật báo đúng 2 lệch: cổng
  `6381` chỉ có trong `ports.yaml`, cổng `6790` chỉ trong AGENTS.md.
- (FEAT) **Chip sức khoẻ HTTP**: package `internal/health` probe song song (tối đa 8
  đồng thời, timeout vào hàm là tham số — app gọi 2s), 4 trạng thái `ok` / `http_error`
  / `no_response` / `skipped`; service compose và cổng gRPC khai `health_skip_ports`
  được skip; chip cổng đi kèm chip sức khoẻ, chạy sau mỗi lần quét (không chặn poll)
  + nút chạy tay `#btn-health`.
- (FEAT) **Tự bật lại service bị crash**: package `internal/supervisor` — chỉ can thiệp
  process do app bật (xác thực qua state file + process group; pid tái sử dụng không
  nhầm), chỉ service có `auto_restart: true`; backoff 5s→10s→20s (trần 60s), tối đa
  3 lần (mặc định, KHÔNG bao giờ vô hạn); chặn khi user tắt có chủ ý (`ExpectStop` + event
  `stop`/`restart`) và khi thấy process group mới; ổn định 5 phút reset bộ đếm; hết
  lượt → event `fail` + ngừng tới khi service ổn định. Bộ đếm nằm trong RAM,
  app đóng là dừng hẳn — không có tiến trình nền.
- (FEAT) **Thông báo macOS khi kẹt/crash**: package `internal/notify` (osascript, không
  thêm phụ thuộc; chạy nền timeout 3s nên không chặn luồng chính; lỗi gửi bị bỏ qua;
  tắt bằng `PORT_MANAGER_NOTIFY`). App có watcher **stuck** riêng: ticker 30s, service
  kẹt được ghi lịch sử + thông báo đúng 1 lần mỗi lần kẹt.
- (FEAT) **Lịch sử chạy**: package `internal/history` — JSONL
  `~/.port-manager/history/<id>.history.jsonl` (mới nhất trước), một lần Write duy nhất
  + fsync mỗi ghi (app chết giữa lúc ghi mất tối đa 1 dòng), rotate 256KB giữ 200 dòng;
  enum kind đúng `start|stop|restart|crash|stuck|fail|edit`. `internal/proc` phát event
  qua observer (`AddObserver`) — đúng 1 event `restart` cho 1 lần Restart, phân biệt
  crash bằng cờ `expectedStop`. Modal lịch sử + CLI `-history`.
- (FEAT) **Thêm/sửa service từ app**: `internal/config/edit.go` sửa `ports.yaml` bằng
  chỉnh text theo dòng qua `yaml.Node` (giữ nguyên byte mọi dòng không đụng, giữ flow
  style + comment, ghi atomic, validate lại bằng `Parse` trước khi ghi; không tự tạo
  file). `UpsertService` (thêm/sửa/chuyển group), `AddIgnorePort`, `SetAutoRestart`,
  `DeleteService`; `SuggestPort` chỉ gợi ý cổng có trong bảng quy hoạch AGENTS.md
  10.1; `ReloadConfig` swap config nóng. FE: modal `#svc-modal` + nút Gợi ý cổng
  (0 = "không còn cổng trống trong bảng quy hoạch").
- (FEAT) **Menu ứng dụng macOS native + phím tắt**: menu Quét lại ⌘R · Bật tất cả ⌘⇧B ·
  Tắt tất cả ⌘⇧T · Giới thiệu; callback menu phát
  event `menu:action` cho FE; phím tắt trong app ⌘R quét lại, ⌘F/⌘K focus tìm kiếm,
  Escape đóng modal. `OnShutdown` gọi `sup.Stop()`.
- (FEAT) **CLI mới**: `-json` (quét in JSON — mode riêng, không gộp `-scan -json`),
  `-start-group <tên>` (exit 2 khi sai tên), `-stop-all`, `-history <id>`; exit code
  0 thành công · 1 lỗi thao tác · 2 sai cú pháp.
- (FEAT) `ports.yaml` thêm `tier` (Hạ tầng 0 / Backend Go 1 / Frontend 2),
  `health_skip_ports` cho cổng gRPC (auth-be 9090, notification-be 9091, wm-be 9092),
  `auto_restart: true` cho service thường (service compose không bật — docker đã có
  restart policy). Field tuỳ chọn: file cũ thiếu `tier` vẫn đọc được (tier = -1, xếp theo
  thứ tự file). `ServiceView` thêm `lastError`/`autoRestart` (omitempty).

## v0.5.0 - 2026-10-22

- (FEAT) **Bấm chip cổng → mở `http://localhost:<port>` trên trình duyệt.** Chip cổng đang
  mở (màu xanh) giờ là nút bấm được, hover có gạch chân + tooltip hiện URL.
  Áp dụng cho cả cổng của service trong bảng lẫn cổng ở mục "Cổng lạ".
  Cổng đang tắt vẫn là chip xám, không bấm được.
- (FEAT) Binding mới `App.OpenURL(url)` → `runtime.BrowserOpenURL` (mở bằng trình duyệt
  mặc định của macOS). Chỉ cho phép scheme `http`/`https` — chặn `file:`/`javascript:`.

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
