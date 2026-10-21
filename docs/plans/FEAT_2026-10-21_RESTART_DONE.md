# FEAT — Tắt & restart service kẹt trong lúc khởi động — 2026-10-21

> **Phân loại: FEAT** — user hỏi: *"Nếu mình khởi động một service, mà service đó gặp
> trục trặc trong lúc khởi động thì có cách nào để mình tắt đi rồi restart lại không"*.
> Trạng thái: `DONE` (đổi tên file thành `_DONE` khi xong).

## 1. Vấn đề

Trạng thái của service trong app được suy ra **chỉ từ port listener**
(`internal/scan/scan.go`). Hệ quả: một service mà process **còn sống nhưng chưa lên
cổng** (đang build, chờ DB, `go run` còn compile, treo trong lúc khởi động) bị
hiển thị là **"Tắt"** — dù nó đang chạy.

Chuỗi lỗi cụ thể:

1. `scan` → `open == 0` → `status = "stopped"`.
2. UI chỉ hiện nút **Bật** cho `status == "stopped"` (`frontend/dist/app.js:151`),
   **không có nút Tắt** → không có đường nào tắt được process treo.
3. Bấm "Bật" → `proc.Manager.Start` từ chối: *"service X đang chạy do app quản lý"*
   (`internal/proc/proc.go:105`) → **kẹt cứng**.

Ngoài app, CLI `-stop` cũng không cứu được: mỗi lần chạy CLI là một process riêng
nên `proc.Manager` mới → map `procs` rỗng → không biết PGID để kill → rơi xuống
nhánh "kill đúng pid listener" → chưa có listener → lỗi *"không tìm thấy process
nào đang lắng nghe"*.

Tệ hơn: app giữ process trong **RAM**. Mở lại app (hoặc chạy CLI) là mất thông tin
process group → service do app bật mà chưa lên cổng trở thành **không tắt được
bằng bất kỳ đường nào** trong app.

## 2. Mục tiêu

1. Tắt & restart được service kẹt lúc khởi động — từ **app** và từ **CLI**.
2. Phân biệt rõ 3 trạng thái: đang chạy / **đang khởi động** / **kẹt** (thay vì gộp
   tất cả thành "Tắt").
3. Không mất thông tin process khi app đóng/mở lại (state file trên đĩa).
4. Không spawn trùng service đang chạy (hiện tại app mở lại có thể bật 2 bản).

## 3. Thay đổi

### 3.1 `internal/proc` — state file + Restart

- **State file** `~/.port-manager/state/<id>.proc.json`: `{pgid, started}` — ghi khi
  bật, xoá khi tắt. Nằm ở HOME (không phụ thuộc cwd) để app GUI và CLI dùng chung.
  - `ManagedInfos()` đọc map trong RAM **+** state file → app mở lại vẫn nhận ra
    service do app bật.
  - Kiểm tra "còn sống" = pid còn tồn tại **và** pgid khớp **và** thời điểm bắt đầu
    khớp (chống kill nhầm khi pid bị tái sử dụng).
- `Stop` tìm process theo thứ tự: map trong RAM → state file → pid listener.
- `Restart(s, listeners, workDir)`: dừng (nếu đang chạy) → bật lại. `Stop` giữ nguyên
  lỗi cũ, `Restart` thì bỏ qua trường hợp "không có gì để tắt".
- `Start` từ chối (kèm pid + giờ bật) nếu service đang sống — kể cả khi biết qua
  state file, không chỉ map trong RAM.
- Log **không truncate** khi bật lại (ghi thêm dòng mốc `=== bật lúc ... ===`) — log
  của lần chạy trước là thứ cần nhìn nhất khi service fail lúc khởi động.

### 3.2 `internal/scan` — trạng thái `starting` / `stuck`

- `ServiceView` thêm `managedPid`, `managedSince`.
- `ScanAll` nhận `ManagedProvider` (interface, để `scan` không import `proc`) thay cho
  callback `func(id) bool`.
- Status: `running` (đủ cổng) · `partial` (một phần) · `starting` (app quản lý,
  chưa lên cổng, < 90s) · `stuck` (app quản lý, chưa lên cổng, ≥ 90s) · `stopped`.

### 3.3 `app.go` / `cli.go` / `main.go`

- `App.RestartService(id)` — binding Wails.
- CLI `-restart <id>`.

### 3.4 Frontend

- Nút **🔄 Restart** cho mọi service đang "lên" (running/partial/starting/stuck).
- Nút **Tắt** hiện cho `starting`/`stuck` (trước đây không có → không tắt được).
- Pill mới: **"Đang khởi động"** (info) và **"Kẹt"** (danger).
- Dòng process hiện pid của app khi chưa có listener (`pid 12345 (app) · chưa lên cổng`).

## 4. Kiểm chứng

- `go test ./...` xanh. Test mới:
  - `internal/proc`: `TestRestartLifecycle` (pid đổi, cổng mở lại),
    `TestStartRefusesWhenAlreadyRunning`, `TestStopViaStateFileAfterAppRestart`
    (manager mới tắt được service kẹt qua state file),
    `TestStartRefusesServiceFromPreviousSession`, `TestLogAppendAcrossRestarts`,
    `TestStateAliveDetectsReusedPid`.
  - `internal/scan/status_test.go`: `TestServiceStatus` (running/partial/stopped/
    starting/stuck).
- `go vet ./...` sạch, `gofmt` sạch.
- **CLI thật** (service `sleep 300` — không lên cổng, đúng tình huống kẹt):

```
$ port-manager -start stuck-svc
Đã bật.
$ port-manager -scan
* Stuck service    5199·   ĐANG KHỞI ĐỘNG pid=5568   (app đang giữ — chưa lên cổng)
$ port-manager -stop stuck-svc        # process 5568 đã chết, state file đã xoá
```

  Trước đây bước `-stop` này **luôn fail** ("không tìm thấy process nào đang lắng nghe").
- Restart thật: `-start ok-svc` → pid 5808 → `-restart ok-svc` → pid **5889** (đổi pid,
  cổng mở lại). `-start` lần hai bị từ chối đúng lý do:
  *"service ok-svc đang chạy (pid 5889, bật lúc 15:13:15) — hãy Restart thay vì Bật"*.
- Trạng thái "Kẹt": giả thời điểm bật là 5 phút trước → `-scan` in `KẸT`, `-stop`
  vẫn tắt được.
- **UI** (browser, chế độ mock): dòng "Đang khởi động" / "Kẹt" hiện pill màu xanh/đỏ,
  dòng process hiện `pid 54321 · app đang giữ · chưa lên cổng`, có nút **Tắt** +
  **🔄 Restart**; modal Restart hiện lệnh start + pid.
- `make build` xong → `build/PortManager.app` (v0.4.0).

## 5. Ghi chú

- State file nằm ở `~/.port-manager/state/` (không theo cwd) để app GUI và CLI dùng
  chung một chỗ. Đổi vị trí bằng env `PORT_MANAGER_STATE`.
- `StartingWindow = 90s` (hằng số trong `internal/scan/scan.go`): quá mốc này mà vẫn
  chưa lên cổng thì chuyển từ "Đang khởi động" sang "Kẹt".
- Service do app bật **vẫn không tự tắt khi quit app** (giữ nguyên thiết kế cũ) — nhưng
  nay vẫn tắt được sau khi mở lại app nhờ state file.
