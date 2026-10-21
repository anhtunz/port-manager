# FIX — "Cổng lạ" phải chỉ ra nguồn gốc (thư mục / container) — 2026-10-21

> Kết quả: 2 cổng lạ trước đây (5180, 8088) giờ đã **không còn** trong mục "Cổng lạ",
> và mọi cổng lạ còn lại đều hiện được **📁 thư mục làm việc** hoặc **🐳 container**
> + **▶ thời gian chạy**.

## 1. Vấn đề

User hỏi: *"Xem nốt phần scan cổng, thấy cổng lạ vẫn có 2 cái 5180 và 8088. Tại sao không
tìm được folder hay sao hiển thị lạ nhỉ"*.

Hai vấn đề tách biệt:

1. **Thiếu thông tin nguồn gốc.** `UnknownListener` chỉ mang `Port / PID / Command`.
   Không hỏi OS "process này chạy ở đâu" nên UI chỉ hiện pid + command — không biết
   thuộc dự án nào. Với cổng do container publish, `command` lại là
   `com.docker.backend` → nhìn vô nghĩa.
2. **Hai cổng này vốn đã có trong quy hoạch nhưng thiếu ở `ports.yaml`** (mirror lệch
   với AGENTS.md mục 10) → app tưởng là "lạ".

## 2. Chẩn đoán từng cổng

| Cổng | Thực tế | Vì sao bị gọi là "lạ" |
| --- | --- | --- |
| **8088** | container `birthday-nginx` (`others/birthday/docker-compose.yml`, service `birthday-nginx`), publish `8088:80` | AGENTS.md mục 10 **đã chốt** ("container lẻ `birthday-nginx` … `8088` — user chốt 2026-10-14") nhưng `ports.yaml` thiếu dòng này |
| **5180** | `node …/assistant-service/frontend/node_modules/.bin/vite --host` (cwd `smatec/assistant-service/frontend`) | `vite.config.ts` **cố ý** đặt `port: 5180, strictPort: true` (cổng host LAN cố định) nhưng bảng FE ở AGENTS.md mục 10.1 chưa có dòng cho app này |

Hiển thị "lạ" của 8088 còn do: trên macOS, **pid giữ cổng publish của container là
`com.docker.backend`** (pid của Docker Desktop), không phải pid trong container →
app hiện một đường dẫn khổng lồ của Docker, không có "folder" nào cả.

## 3. Sửa

### 3.1 Backend — tra nguồn gốc

- `internal/scan/cwd.go` (mới): `ProcessCwds(pids)` chạy
  `lsof -a -p <pids> -d cwd -Fn` (1 lệnh cho nhiều pid) → map pid → thư mục làm việc;
  `ParseLsofCwd` parse output (`p<pid>` / `fcwd` / `n<path>`).
- `internal/scan/docker.go` (viết lại): `DockerSnapshot` giờ giữ thêm
  `byPort map[int]PortOwner` — host port ← container publish (đọc từ `docker ps`
  cột `.Ports`, `ParseDockerPorts` hỗ trợ cả dải `9000-9001->9000-9001`).
  `PortOwner` = tên container + thư mục project (label `working_dir` của compose)
  + thời điểm start.
- `internal/scan/scan.go`: `UnknownListener` thêm `Cwd`, `Container`, `Project`,
  `StartedAt`. Sau khi dựng danh sách cổng lạ, app gọi `ProcessCwds` + snapshot
  docker (lazy) để điền nguồn gốc; đường dẫn rút gọn tương đối so với gốc
  workspace (`relToRoot`).
- `internal/proc/proc.go`: thêm `StopContainer(name)` → `docker stop <name>`.
- `app.go`: thêm binding `StopUnknownContainer(name)`.

### 3.2 An toàn — không kill nhầm Docker Desktop

Nút "Tắt process" cũ gọi `KillUnknown(port, pid)` → `kill` pid mà `lsof` thấy. Với
cổng do container giữ, pid đó là **`com.docker.backend`** → kill là **tắt cả
Docker Desktop** (mất toàn bộ container). Nay:

- cổng lạ có `container` → nút **"Tắt container"** → `docker stop <container>`;
- cổng lạ không có container → nút "Tắt process" như cũ.

### 3.3 Frontend

- Dòng "Cổng lạ" hiện thêm dòng nguồn gốc (`📁 <thư mục>` hoặc `🐳 container <tên> · <project>`)
  và chip thời gian chạy `▶ lúc bắt đầu · đã chạy` (ticker 1s).
- `doStopUnknownContainer(name, port)` + nhánh `act === "stopc"` trong handler click
  dùng chung, có modal xác nhận.
- Mock data cập nhật theo kịch bản thật (một dòng `cwd`, một dòng `container`).

### 3.4 Đồng bộ quy hoạch (`ports.yaml` + AGENTS.md mục 10)

- `ports.yaml`: thêm `infra-birthday-nginx` (8088, `others/birthday/docker-compose.yml`,
  service `birthday-nginx`) — **AGENTS.md đã chốt sẵn**, đây là sửa mirror lệch.
- `AGENTS.md` mục 10.1 (bảng Frontend): thêm dòng `assistant-service/frontend` → 5180
  (port đã tồn tại sẵn trong `vite.config.ts`, chỉ là bảng quy hoạch thiếu).
- `ports.yaml`: thêm `assistant-fe` (5180, `assistant-service/frontend`, `npm run dev`).

## 4. Kiểm chứng

- `go test ./...` xanh (thêm `internal/scan/unknown_test.go`: `ParseLsofCwd`,
  `ParseDockerPorts`, `relToRoot`).
- `go vet ./...` sạch, `gofmt` sạch.
- CLI `-scan` thật (gỡ 2 dòng khỏi `ports.yaml` để tái lập tình huống):

```
== Cổng lạ (không nằm trong cấu hình) ==
  5180   pid=68514   node …/assistant-service/frontend/…  📁 assistant-service/frontend  ▶ 16:52:32 · 5d 19h
  8088   pid=14825   /Applications/Docker.app/…/com.docker.…  🐳 birthday-nginx · others/birthday  ▶ 00:00:47 · 6d 12h
```

- Sau khi mirror đủ: `Không có cổng lạ.` (cả 5180 và 8088 đã thành service có hàng
  trong bảng, hiện thời gian chạy riêng của container).
- UI (browser, chế độ mock): dòng cổng lạ hiện đúng `📁`/`🐳` + thời gian chạy; bấm
  "Tắt container" mở modal *"Dừng container ở cổng 8088? — birthday-nginx"*.
- `make build` xong → `build/PortManager.app` (v0.3.0).

## 5. Ghi chú

- `lsof` và `docker` là lệnh hệ thống, không cài thêm gì; cả hai đều có timeout
  (2s / 3s) để không chặn lần quét.
- `docker ps` chỉ đọc metadata container, không đụng vào container nào.
