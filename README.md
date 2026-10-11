# Port Manager — quản lý cổng dev workspace smatec

App desktop macOS (Go + Wails) để xem service nào đang chạy trên cổng nào trong workspace
`smatec/`, tắt/bật từng service, và phát hiện process "ma" chiếm cổng lạ.

> **Nguồn sự thật cổng:** bảng quy hoạch port ở `AGENTS.md` mục 10 (root workspace).
> File `ports.yaml` của app chỉ là **mirror** — đổi port phải sửa AGENTS.md TRƯỚC rồi mới mirror sang đây.

## Tính năng (v0.1.0)

- Quét trạng thái từng service theo 3 nhóm: **Backend Go** · **Frontend** · **Hạ tầng (docker)**,
  nhận diện PID + full command của process đang lắng nghe (`lsof` + `ps`, không cần cài thêm gì).
- **Tắt** process chiếm cổng (TERM → KILL, có modal xác nhận); **Bật** theo lệnh start
  khai trong `ports.yaml` (`make` / `npm run dev`), process chạy ở process group riêng, log ghi vào `logs/<id>.log`.
- Hạ tầng docker điều khiển qua `docker compose stop` / `up -d` đúng compose_dir/service — không kill lung tung bên trong container.
- Mục **"Cổng lạ"**: listener không nằm trong danh sách cấu hình (lọc bớt noise hệ thống qua
  `unknown_ignore_commands` + `unknown_ignore_ports`) — chỗ bắt vite ma / process ma.
- Auto-refresh 3 giây (tắt được ở toolbar).

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
./build/port-manager -scan          # in trạng thái toàn bộ service + cổng lạ
./build/port-manager -start portal-fe
./build/port-manager -stop portal-fe
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

## v0.1.0 - 2026-10-11

- (FEAT) Bản đầu tiên: dashboard 3 nhóm service, bật/tắt theo config, kill process ma, CLI headless.
