# FEAT — Bấm cổng → mở localhost trên trình duyệt — 2026-10-22

> Kết quả: bấm vào chip cổng đang mở (màu xanh) là app mở `http://localhost:<port>`
> bằng trình duyệt mặc định của macOS. Áp dụng cho cả cổng của service trong bảng
> lẫn cổng ở mục "Cổng lạ".

## 1. Yêu cầu

User: *"Bổ sung thêm cho mình khi mình nhấn vào cổng thì sẽ mở link localhost đó lên
trình duyệt cho tiện"*.

Trước đây chip cổng chỉ để đọc (`<span class="port">3000</span>`) — muốn xem service
đang chạy thì phải tự gõ `localhost:3000` vào trình duyệt.

## 2. Sửa

### 2.1 Backend — binding `OpenURL`

`app.go`: thêm method expose cho frontend (Wails tự sinh `window.go.main.App.OpenURL`):

```go
func (a *App) OpenURL(rawURL string) error {
	// parse + chỉ cho phép http/https, rồi runtime.BrowserOpenURL(a.ctx, ...)
}
```

- `runtime.BrowserOpenURL` (Wails v2) trên macOS gọi `open <url>` → mở bằng trình
  duyệt mặc định của hệ điều hành.
- `a.ctx` lấy từ `OnStartup` → có giá trị `"frontend"` nên `runtime` không bị
  `log.Fatalf` (đã kiểm tra lại source wails v2.16.0:
  `internal/app/app_production.go` set `ctx = context.WithValue(ctx, "frontend", appFrontend)`
  rồi mới gọi `OnStartup(f.ctx)`).
- **Chặn scheme khác**: chỉ `http`/`https` được mở, còn lại báo lỗi — không mở
  `file:`/`javascript:` từ dữ liệu quét.

### 2.2 Frontend

- `portChip(port, open)` (mới): cổng đang mở → `<button class="port open"
  data-act="open" data-port="…" title="Mở http://localhost:…">`; cổng đang tắt vẫn là
  `<span class="port closed">` (không có gì để mở).
- `serviceRow()` dùng `portChip` cho từng cổng; dòng **"Cổng lạ"** cũng đưa chip cổng
  vào cột ports (trước đó cột này trống, chỉ có chữ "Cổng 20128" ở cột tên).
- `openPort(port)` (mới): gọi `API.OpenURL("http://localhost:<port>")` trong app Wails;
  ngoài app (chế độ mock, mở file bằng trình duyệt) thì `window.open(url, "_blank")`.
- Handler click dùng chung `button[data-act]` → thêm nhánh `act === "open"`.
- CSS: `button.port` bỏ viền mặc định của browser, `cursor: pointer`, hover đậm nền +
  gạch chân, `:active` đậm hơn.

## 3. Kiểm chứng

- `go build ./...`, `go vet ./...`, `gofmt -l .` sạch; `go test ./...` xanh.
- `make build` → `build/PortManager.app` (v0.5.0).
- UI (chế độ mock, phục vụ `frontend/dist` bằng http server):
  - DOM: 16 chip cổng mở là `<button>`, 11 chip cổng tắt là `<span>`; dòng "Cổng lạ"
    có chip `<button data-port="20128">`.
  - Bấm chip `3000` → handler gọi `window.open` với đúng `http://localhost:3000`
    (đo bằng cách thay `window.open` bằng hàm ghi lại URL rồi bấm chip).
  - Screenshot: chip cổng hiển thị y hệt trước (xanh/ xám), không vỡ layout.

## 4. Ghi chú

- Cổng gRPC (9090/9091/9092) hoặc cổng không phải HTTP (postgres 5432, redis 6379…)
  bấm vẫn mở `http://localhost:<port>` → trình duyệt báo lỗi kết nối. Chấp nhận được:
  không thêm cấu hình per-port để giữ thay đổi nhỏ gọn.
- Không đụng tới `ports.yaml`/`AGENTS.md` mục 10 — thay đổi này không thêm port mới.
