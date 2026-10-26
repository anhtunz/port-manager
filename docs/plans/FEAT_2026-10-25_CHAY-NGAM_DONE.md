# FEAT — Chạy ngầm (ẩn vào tray, LaunchAgent tự khởi động, ghi lý do thoát) — 2026-10-25

> **Phân loại:** FEAT (tính năng mới, có binding + UI + CLI mới)
> **Trạng thái:** DONE (2026-10-25) — xem §9 kết quả
> **Branch:** `feat/chay-ngam` (tạo từ `main` đang có working tree chưa commit của v0.6.0 — xem §8)
> **Version:** 0.6.0 → **0.7.0** (bump `y` — lệch quy ước bump `x` của AGENTS.md, theo đúng thực hành đã ghi nhận trong repo ở các plan trước: FEAT bump `y`)
> **Không commit/push** trong phạm vi plan này — chỉ sửa file, build, test; việc commit do người dùng quyết định.

---

## 1. Bối cảnh và mục tiêu

### 1.1 Bối cảnh

Người dùng chạy `make run` (mở `build/PortManager.app`) rồi "thỉnh thoảng app bị tắt đi không biết lý do".

Nguyên nhân gốc **đã xác định ngày 23/10/2026** (memory `app-sigkilled-by-assistant-makefile-port-cleanup`): không phải bug trong app. Makefile của `assistant-service/backend` dọn cổng bằng `lsof -ti:3005 | xargs kill -9`. `lsof -ti:3005` liệt kê **mọi** process có kết nối tới cổng 3005 — kể cả client. Port Manager probe health qua `http.Client` dùng keep‑alive (`IdleConnTimeout 30s`), nên sau mỗi vòng health‑check app còn giữ một kết nối nhàn rỗi tới `:3005` ⇒ bị `kill -9` nhầm. SIGKILL không chặn được ⇒ app "biến mất khỏi Dock" không log, không thông báo.

Ngoài nguyên nhân cụ thể trên, app hiện cũng không có bất kỳ cơ chế "chạy ngầm" nào:

- Bấm nút đỏ đóng cửa sổ ⇒ Wails thoát app hẳn (supervisor, health, tray mất theo).
- Không có gì tự khởi động lại app khi nó chết hoặc khi đăng nhập lại.
- Không ghi lại lý do thoát ⇒ lần sau mở app không biết lần trước thoát vì ai.

### 1.2 Mục tiêu

| ID | Hạng mục | Mô tả ngắn |
|----|----------|------------|
| F0 | Một instance duy nhất | Khoá `flock` ở `~/.port-manager/state/app.lock`. Mở lần 2 → gửi `SIGUSR1` cho instance đang chạy để nó hiện cửa sổ, rồi thoát mã 0. Nếu instance 2 do launchd khởi động (env `PORT_MANAGER_AGENT=1`) → thoát mã 3 để launchd thử lại (bàn giao). |
| F1 | Đóng cửa sổ = ẩn, không thoát | `options.App.HideWindowOnClose: true` (Wails xử lý ở tầng native: `[NSApp hide]`). Thoát thật chỉ qua ⌘Q / Dock Quit / tray "Thoát" / menu "Thoát". Tray "Mở" + bấm icon Dock hiện lại. |
| F2 | LaunchAgent tự khởi động & tự hồi sinh | Gói `internal/daemon`: copy binary vào `~/.port-manager/bin/port-manager`, ghi plist `~/Library/LaunchAgents/vn.smatec.port-manager.plist` (RunAtLoad, KeepAlive{SuccessfulExit:false}, log `~/.port-manager/logs/agent.{out,err}.log`, env `PORT_MANAGER_AGENT=1`), `launchctl bootstrap/bootout/kickstart gui/<uid>`. Có cài/gỡ/trạng thái từ GUI (modal + menu) và CLI. |
| F3 | Ghi lý do thoát + phát hiện thoát đột ngột | Gói `internal/exitlog`: `app.session.json` (ghi lúc khởi động), `app.lastexit.json` (ghi lúc thoát sạch hoặc nhận SIGTERM/SIGINT/SIGHUP/SIGQUIT). Khởi động mà vẫn còn `session.json` cũ ⇒ lần trước thoát đột ngột (SIGKILL/mất điện). Binding `LastExit()` → FE hiện banner một lần. |
| F4 | Vá nguyên nhân gốc trong repo này | `health.Checker.ProbeAll` gọi `client.CloseIdleConnections()` sau mỗi vòng ⇒ app không còn giữ kết nối nhàn rỗi tới cổng service ⇒ `lsof -ti:<port>` của script dọn cổng không còn khớp app. |

### 1.3 Nguyên tắc bất di bất dịch

1. **Không tự sửa repo khác.** Khuyến nghị cho `assistant-service/backend/Makefile` chỉ ghi ở §7 (Tác động tiềm ẩn).
2. **Lỗi của tính năng chạy ngầm không được làm sập app**: ghi exitlog hỏng, launchctl lỗi, notify lỗi → trả `error`/bỏ qua, không panic, không `log.Fatal`.
3. **Thoát sạch là mã 0.** Chỉ instance do launchd khởi động mà bị trùng khoá mới thoát mã 3 (để launchd KeepAlive thử lại). Không bao giờ để launchd rơi vào vòng restart vô hạn vì lỗi cấu hình (mã 0 khi conflict thường, `SuccessfulExit:false` không restart mã 0).
4. **GUI và CLI dùng chung đường dẫn state** (`resolveStateDir()` đã có: env `PORT_MANAGER_STATE` → `~/.port-manager/state`). CLI **không** lấy khoá F0 (CLI chạy song song GUI là bình thường).
5. **Từ chối cài agent khi đang chạy `go run`** (đường dẫn exe nằm trong thư mục build tạm `go-build`): binary tạm sẽ bị xoá ⇒ launchd không khởi động được. Thông báo bảo người dùng `make build` rồi mở `.app`.
6. `internal/api` chỉ chứa alias kiểu — `AgentStatus`, `LastExitInfo` định nghĩa trong gói lõi, alias ở `api`.
7. Code/comment/thông điệp tiếng Việt, comment giải thích "vì sao".
8. Tray (`internal/tray`, `.m` MRC) **không đụng**.

---

## 2. Bảng hạng mục

| ID | Tên | Nhóm | Ưu tiên | Hạt nhân Go | Binding/CLI | Ghi chú |
|----|-----|------|---------|-------------|-------------|---------|
| F0 | Khoá một instance | BE | Cao | `daemon.AcquireLock(stateDir)` → `*Lock`, `Lock.Release()`; `daemon.NotifyRunning(pid)` gửi SIGUSR1 | — | Chỉ `runGUI`. Lock file ghi pid để instance 2 biết gửi tín hiệu cho ai. |
| F1 | Ẩn khi đóng | BE | Cao | `options.App{HideWindowOnClose: true}` | — | Không cần `OnBeforeClose`. Tray "Mở"/SIGUSR1 → `runtime.Show + WindowUnminimise + WindowShow`. |
| F2 | LaunchAgent | BE+FE+CLI | Cao | `daemon.Install(opts)`, `daemon.Uninstall()`, `daemon.Status()`, `daemon.IsAgentProcess()`, `daemon.RefuseGoRun()` | `AgentInstall()`, `AgentUninstall()`, `AgentStatus()`; CLI `-install-agent`, `-uninstall-agent`, `-agent-status`; Makefile `agent-install/agent-uninstall/agent-status` | Cài từ GUI = bàn giao: ghi file → spawn `sh -c 'sleep 2; launchctl bootstrap …; launchctl kickstart -k …'` tách khỏi app → app thoát (mã 0). |
| F3 | Exitlog | BE+FE | Cao | `exitlog.New(stateDir)`: `BeginSession()`, `EndSession(reason)`, `RecordSignal(sig)`, `Last()` | `LastExit()` | FE banner `#exit-banner` (ngoài `#content`), chỉ hiện khi `kind` ∈ {abrupt, signal}. |
| F4 | Đóng kết nối nhàn rỗi | BE | Cao | `health.Checker.ProbeAll` → `c.client.CloseIdleConnections()` | — | Vá nguyên nhân gốc trong repo này. |
| F5 | Menu "Chạy ngầm" | BE | TB | `buildMenu`: submenu "Chạy ngầm" → `menuAction("agent")` phát event `menu:action` `agent` | — | FE nghe event → mở `#agent-modal`. |

---

## 3. Hợp đồng API (binding Go ↔ FE)

Tất cả binding là method exported trên `*App`, gọi qua `window.go.main.App.*`. JSON camelCase.

### 3.1 `AgentStatus() (api.AgentStatus, error)`

```json
{
  "label": "vn.smatec.port-manager",
  "plistPath": "/Users/x/Library/LaunchAgents/vn.smatec.port-manager.plist",
  "binPath": "/Users/x/.port-manager/bin/port-manager",
  "installed": true,
  "loaded": true,
  "pid": 12345,
  "thisIsAgent": false,
  "canInstall": true,
  "cannotInstallReason": ""
}
```

- `installed`: plist tồn tại. `loaded`: `launchctl print gui/<uid>/<label>` thành công. `pid`: parse `pid = N` (0 nếu không chạy).
- `thisIsAgent`: process hiện tại có env `PORT_MANAGER_AGENT=1` (được launchd khởi động).
- `canInstall=false` + `cannotInstallReason` khi đang `go run` hoặc không phải darwin.
- Không bao giờ trả lỗi vì launchctl không có/không chạy — trạng thái "không loaded" là dữ liệu, không phải lỗi.

### 3.2 `AgentInstall() error`

Thành công ⇒ file đã ghi và bàn giao đã lên lịch; **app sẽ tự thoát sau ~1s** (goroutine `time.Sleep` → `runtime.Quit`). FE phải hiện thông điệp trước khi gọi (hoặc ngay sau khi resolve) và không gọi gì khác. Lỗi: `go run`, không copy được binary, không ghi được plist.

### 3.3 `AgentUninstall() error`

`launchctl bootout` (bỏ qua lỗi "not loaded"), xoá plist, xoá binary copy. Nếu process hiện tại **là** agent, bootout sẽ SIGTERM chính nó ⇒ exitlog ghi "signal SIGTERM" ⇒ cửa sổ đóng. FE cảnh báo trước bằng `confirmModal` khi `thisIsAgent=true`.

### 3.4 `LastExit() api.LastExitInfo`

```json
{ "kind": "abrupt", "at": "2026-10-25T09:12:33+07:00", "signal": "", "detail": "Phiên trước (pid 4242, bắt đầu 09:10:01) kết thúc mà không ghi được lý do — thường là kill -9 hoặc mất điện." }
```

- `kind`: `none` (lần đầu / lần trước thoát bình thường) · `abrupt` · `signal` (có `signal` = `SIGTERM|SIGINT|SIGHUP|SIGQUIT`) · `quit` (thoát bình thường — FE không hiện banner).
- Thông tin được tính **một lần lúc khởi động** (trước khi ghi session mới) và cache trong `App` ⇒ gọi nhiều lần vẫn cùng kết quả, không có race với file.

### 3.5 Ánh xạ

| Binding | Go (`app.go`) | Gói lõi |
|---------|---------------|---------|
| `AgentStatus` | `(*App).AgentStatus` | `daemon.Status()` |
| `AgentInstall` | `(*App).AgentInstall` | `daemon.Install(daemon.InstallOptions{Handover: true})` + `a.quitLater()` |
| `AgentUninstall` | `(*App).AgentUninstall` | `daemon.Uninstall()` |
| `LastExit` | `(*App).LastExit` | `exitlog` (cache `a.lastExit`) |
| menu `agent` | `menuAction("agent")` → `emitMenu("agent")` | — |

### 3.6 CLI (`cli.go`)

```
port-manager -install-agent     # cài LaunchAgent (bootout → bootstrap → kickstart, không bàn giao vì CLI không giữ khoá)
port-manager -uninstall-agent   # gỡ
port-manager -agent-status      # in trạng thái dạng text + mã thoát 0/1 (1 = chưa cài hoặc không chạy)
port-manager -last-exit         # in lý do thoát lần trước (kind/at/signal/pid/detail) — bổ sung khi triển khai
```

Makefile tương ứng: `agent-install` (phụ thuộc `build`), `agent-uninstall`, `agent-status`, `last-exit`.

---

## 4. Hợp đồng giao diện

### 4.1 DOM thêm mới (`index.html`)

| Id / selector | Vị trí | Vai trò |
|---------------|--------|---------|
| `#btn-agent` (`button[data-act="agent"]`) | `#toolbar`, sau `#btn-reload-config` | Mở modal Chạy ngầm |
| `#exit-banner` (`.banner.warn`, hidden) | Trước `<main id="content">`, sau `#errbar` | Banner "lần trước app thoát vì…"; nút `button[data-act="dismiss-exit"]` |
| `#agent-modal` (`.modal`, hidden) trong `#overlay` | Sau `#history-modal` | Panel trạng thái + nút `data-act="agent-install"`, `data-act="agent-uninstall"`, `data-act="agent-refresh"`; nút đóng `#agent-close` |

`#agent-modal` phải được thêm vào danh sách `hidePanels()` và `showPanel()` trong `app.js` (panel nằm ngoài `#content` để `render()` không ghi đè).

### 4.2 Luồng tương tác

- **Mở modal:** click `#btn-agent` hoặc menu "Chạy ngầm › Trạng thái…" (`menu:action` = `agent`) → `openAgentModal()` → `API.AgentStatus()` → render. Mock mode (`API === null`) render dữ liệu giả.
- **Cài:** `confirmModal("Cài chạy ngầm?… App sẽ thoát và tự mở lại ngầm trong vài giây.")` → `API.AgentInstall()` → thành công: `showOpError`-kiểu info "Đã cài. App sẽ thoát và tự mở lại ngầm…" (app tự thoát sau ~1s). Lỗi: hiển thị trong modal (`#agent-error`).
- **Gỡ:** confirm (cảnh báo thêm nếu `thisIsAgent`) → `API.AgentUninstall()` → reload status.
- **Banner thoát:** trong `init`, sau `Version()`: `API.LastExit()` → `uiLogic.exitBanner(info)` → `{title, text}` hoặc `null`. Hiện `#exit-banner`; `dismiss-exit` ẩn. Không tự ẩn theo thời gian (khác `showOpError`) vì đây là cảnh báo cần người dùng đọc.
- **Bị ẩn (nút đỏ):** không có FE; tray "Mở" / icon Dock / SIGUSR1 hiện lại.

### 4.3 Hàm thuần (`app.logic.js`, test node)

- `exitBanner(info)`: `null` khi `info` rỗng hoặc `kind ∈ {none, quit}`; `abrupt` → `{kind, title: "Lần trước Port Manager bị tắt đột ngột (pid N) lúc …", text: detail | gợi ý kill -9}`; `signal` → `title: "… bị dừng bởi tín hiệu <SIG> lúc …"` + detail. Giờ qua `fmtTime(at)` (RFC3339 → `dd/mm/yyyy HH:MM:SS`, chuỗi lạ trả nguyên).
- `agentSummary(st)` → `{tone: ok|warn|off, label}` (một dòng tóm tắt, tone để tô viền); `agentRows(st)` → `[{k, v}]` các cặp nhãn/giá trị (Label, Tiến trình này, Plist, Binary, Log, lý do không cài được), bỏ dòng rỗng; `installLabel(st)` ("Cài & chạy ngầm" / "Cài lại (bàn giao)"). *(Triển khai tách `agentStatusText` dự kiến thành `agentSummary` + `agentRows` để modal render được bảng.)*

### 4.4 Ràng buộc kỹ thuật

- Mọi dữ liệu từ Go qua `esc()` trước khi vào innerHTML.
- Chỉ dùng listener uỷ quyền `button[data-act]` đã có; không thêm `onclick`.
- `#exit-banner` nằm ngoài `#content` để `render()` không ghi đè; khác `#errbar` (bị `hideError()` xoá mỗi poll).

---

## 5. Sơ đồ file

### Mới

| File | Nội dung |
|------|----------|
| `internal/daemon/lock.go` | `AcquireLock`, `Lock.Release`, `NotifyRunning` (SIGUSR1), `LockPath`, `IsAgentProcess`, `ExitCodeConflict` |
| `internal/daemon/agent.go` | `Label`, `PlistPath`, `BinPath`, `Status`, `Install`, `Uninstall`, `RenderPlist`, `RefuseGoRun`, `parseLaunchctlPID` |
| `internal/daemon/daemon_test.go` | test `RenderPlist`, `parseLaunchctlPID`, `AcquireLock` (2 lần trong tmp dir), `isGoRunPath` |
| `internal/exitlog/exitlog.go` | `Store`, `New`, `BeginSession`, `EndSession`, `RecordSignal`, `Last`, `Info`, kinds |
| `internal/exitlog/exitlog_test.go` | phiên sạch → `quit`; phiên bỏ dở → `abrupt`; signal → `signal`; lần đầu → `none` |
| `frontend/test/agent.logic.test.js` | 10 test: `exitBanner`, `fmtTime`, `agentSummary`, `agentRows`, `installLabel` |
| `docs/plans/FEAT_2026-10-25_CHAY-NGAM_*.md` | plan này |

### Sửa

| File | Thay đổi |
|------|----------|
| `internal/health/health.go` | `Checker.ProbeAll`: `CloseIdleConnections()` sau `wg.Wait()` + comment vì sao |
| `internal/api/api.go` | alias `AgentStatus = daemon.AgentStatus`, `LastExitInfo = exitlog.Info` |
| `main.go` | `runGUI`: khoá F0, exitlog BeginSession, signal handler (TERM/INT/HUP/QUIT → EndSession + exit 0; USR1 → hiện cửa sổ), `HideWindowOnClose: true`; `buildMenu`: submenu "Chạy ngầm" |
| `app.go` | field `exitlog`, `lastExit`, `showWindow()`, `quitLater()`; bindings `AgentStatus/AgentInstall/AgentUninstall/LastExit`; `menuAction` case `agent`, `agent-install`, `agent-uninstall`; `shutdown`: `EndSession("quit")`; tray `open` dùng `showWindow()` |
| `cli.go` | 4 flag mới (`-install-agent`, `-uninstall-agent`, `-agent-status`, `-last-exit`) + usage + `printAgentStatus`/`printLastExit` |
| `Makefile` | `agent-install` (phụ thuộc `build`), `agent-uninstall`, `agent-status`, `last-exit`; `test` chạy thêm `node --test frontend/test/` |
| `frontend/dist/index.html` | `#btn-agent`, `#exit-banner`, `#agent-modal` |
| `frontend/dist/app.js` | `openAgentModal`, `renderAgentModal`, case `agent/agent-install/agent-uninstall/agent-refresh/dismiss-exit`, menu event `agent`, init: LastExit banner, panel list |
| `frontend/dist/app.logic.js` | `exitBanner`, `agentSummary`, `agentRows`, `installLabel`, `fmtTime` |
| `frontend/dist/style.css` | `.exit-banner*`, `.agent-intro`, `.agent-status`, `.agent-summary.agent-{ok,warn,off}`, `.agent-kv/.agent-k/.agent-v` |
| `VERSION` | 0.7.0 |
| `README.md` | mục "Chạy ngầm" + changelog v0.7.0 |

---

## 6. Kế hoạch kiểm thử

| # | Kiểm thử | Cách |
|---|----------|------|
| T1 | Go unit | `GOWORK=off go test ./...` (daemon, exitlog, health, các gói cũ) |
| T2 | FE thuần | `node --test frontend/test/` |
| T3 | Build | `make build` (gói `.app`) |
| T4 | Tay — F1 | Mở app, bấm nút đỏ → app còn trong Dock + tray; tray "Mở" → hiện lại; ⌘Q → thoát, `app.lastexit.json` kind `quit` |
| T5 | Tay — F0 | Mở app 2 lần → lần 2 thoát ngay, cửa sổ lần 1 hiện lên |
| T6 | Tay — F3 | `kill -9 <pid>` rồi mở lại → banner "tắt đột ngột"; `kill -TERM` → banner "SIGTERM" |
| T7 | Tay — F2 | Modal Cài → app thoát → `launchctl print gui/501/vn.smatec.port-manager` có pid; `kill -9` pid → launchd mở lại sau ≤10s; Gỡ → plist mất |
| T8 | Tay — F4 | Khi app chạy: `lsof -ti:3005` **không** còn chứa pid app sau vòng health |

Tay (T4–T8) ghi lại kết quả ở phần summary khi chuyển `_DONE`; phần nào không thử được trong phiên này thì ghi rõ.

---

## 7. Tác động tiềm ẩn & khuyến nghị ngoài repo

1. **`assistant-service/backend/Makefile`** (và mọi Makefile dọn cổng tương tự): đổi `lsof -ti:3005 | xargs kill -9` → `lsof -ti:3005 -sTCP:LISTEN | xargs kill -9` để chỉ giết process **đang nghe** cổng, không giết client. F4 làm app miễn nhiễm, nhưng trình duyệt/`curl` khác vẫn có thể bị giết nhầm.
2. LaunchAgent chạy **binary trần** (`~/.port-manager/bin/port-manager`, không phải `.app`) ⇒ icon Dock là icon mặc định, tên process `port-manager`. Chấp nhận ở v0.7.0; cải thiện (chạy `open -a` hoặc copy cả `.app`) để sau.
3. Binary copy là **snapshot lúc cài** ⇒ sau `make build` mới phải "Cài lại" để agent chạy bản mới. Modal ghi rõ.
4. `KeepAlive{SuccessfulExit:false}`: ⌘Q (mã 0) ⇒ launchd **không** mở lại cho tới lần đăng nhập sau hoặc khi người dùng bấm "Chạy ngầm lại". Đây là chủ ý (người dùng thoát là thoát).
5. Ẩn bằng `[NSApp hide]` ⇒ cả app ẩn khỏi màn hình (không chỉ cửa sổ), bấm icon Dock hiện lại — hành vi chuẩn macOS.

## 8. Lưu ý git

Working tree lúc tạo branch đang chứa toàn bộ thay đổi v0.6.0 **chưa commit** (Makefile, README, app.go, internal/*, frontend/dist/*, docs/plans/*_2026-10-22_*). Branch `feat/chay-ngam` được tạo trên tree này nên phần v0.6.0 và v0.7.0 trộn lẫn trong working tree. Khuyến nghị khi commit: `git add -p`/add theo file để tách **commit 1 = v0.6.0** (file đã có từ trước phiên này) và **commit 2 = v0.7.0** (file mới: `internal/daemon`, `internal/exitlog`, `frontend/test/agent.logic.test.js`, plan 2026-10-25 + phần sửa thuộc chạy ngầm). Không commit trong plan này.

---

## 9. Kết quả triển khai (2026-10-25)

| # | Kết quả |
|---|---------|
| T1 | ✅ `GOWORK=off go test ./...` — tất cả gói pass (daemon 7 test, exitlog 7 test, health thêm `TestProbeAllDongKetNoiNhanRoi`). |
| T2 | ✅ `node --test frontend/test/` — 30/30 pass (20 cũ + 10 mới trong `agent.logic.test.js`); `node --check app.js` OK. |
| T3 | ✅ `make build` → `build/PortManager.app` (v0.7.0). Smoke CLI: `-version` → `0.7.0`; `-agent-status` → "Đã cài: không", exit 1 (đúng hợp đồng); `-last-exit` → "Chưa có dữ liệu lần thoát trước.", exit 0. |
| T4–T8 | ⏳ **Chưa chạy tay trong phiên này** (cần mở app GUI, kill process, cài LaunchAgent thật trên máy người dùng). Người dùng kiểm tra theo bảng §6; đặc biệt T7 (cài → bàn giao → `kill -9` → launchd mở lại) và T8 (`lsof -ti:3005` không còn pid app). |

Lệch so với bản DRAFT, đã sửa trong thân plan: thêm flag `-last-exit` + make `last-exit`; `agentStatusText` tách thành `agentSummary` + `agentRows`; file test đổi tên `agent.logic.test.js`; `Uninstall` xoá plist + binary **trước** rồi mới `bootout` (nếu bootout trước, launchd SIGTERM chính app đang gỡ → không kịp xoá plist); `xmlEscape` dùng `strings.NewReplacer` (không dùng `json.Marshal`); tạo `~/Library/LaunchAgents` nếu thiếu.

Chưa làm (ngoài phạm vi, ghi nhận cho sau): LaunchAgent chạy binary trần (icon Dock mặc định); sửa `assistant-service/backend/Makefile` dùng `lsof -ti:3005 -sTCP:LISTEN` (repo khác — chỉ khuyến nghị).
