<!--
FIXTURE — copy nguyên văn mục 10 (10.1) của AGENTS.md ở workspace smatec/.
Nguồn: /Users/tunztunzz/Documents/smatec/AGENTS.md — chụp 2026-10-22.
Mục đích: test internal/mirror chạy được trên mọi máy, không phụ thuộc file ngoài repo.
-->

## 10. Quy hoạch port & quy tắc chạy server (bắt buộc)

Bảng dưới là **nguồn sự thật duy nhất** về port khi chạy service/app trên máy dev local. Thêm service mới hoặc đổi port → sửa bảng này TRƯỚC rồi mới đổi config, trong cùng một commit.

### 10.1 Bảng port local dev

**Backend Go:**

| Service | HTTP | gRPC | Port đặt ở đâu |
| --- | --- | --- | --- |
| `auth-service/backend` | 3000 | 9090 | `.env` — `HTTP_PORT`, `GRPC_PORT` |
| `warehouse-management/backend` | 3001 | 9092 | `.env` — `SERVER_PORT`, `WM_GRPC_PORT` |
| `notification-service/backend` | 3004 (REST healthz/version) | 9091 | `.env` — `SERVER_PORT`, `GRPC_PORT` |
| `assistant-service/backend` (assistant chatbox AI) | 3005 | 9093 (dự phòng — phase 1 chưa bật) | `.env` — `HTTP_PORT`, `GRPC_PORT` |
| `telegram-emulator/` (Bot API giả, chạy binary `build/telegram-emulator` từ gốc repo) | 3010 (web UI cùng port) | — | `configs/config.yaml` — `emulator.port` |
| `facility/backend` (fm-backend) | 6868 | — (chỉ làm gRPC client) | `.env` — `SERVER_PORT` |
| `base/backend` | 8090 | — | `.env` — `SERVER_PORT` |

**Frontend:**

| App | Dev server | Preview (build prod) | Port đặt ở đâu |
| --- | --- | --- | --- |
| `auth-service/auth-frontend` | 3003 | 4176 | script `dev` (`--port 3003`) + `vite.config.ts` (`strictPort`, `preview.port`) |
| `auth-service/portal-frontend` | 8000 | 4173 | `vite.config.ts` — `server.port` + `preview.port`, `strictPort` |
| `base/frontend` | 8091 | 4174 | `vite.config.ts` — `server.port` + `preview.port`, `strictPort` |
| `facility/frontend` | 6869 | 4175 | `vite.config.ts` — `server.port` + `preview.port`, `strictPort` |
| `warehouse-management/frontend` | 6789 (utoopack mở thêm 6790 nội bộ) | — | script `dev` (`PORT=6789`) |
| `assistant-service/frontend` (console quản trị AI) | 5180 | — | `vite.config.ts` — `server.port` + `strictPort`, base `/assistant-admin/`, proxy `/assistant-admin/api` → :3005 |
| `telegram-emulator/web` (Vite dev cho web UI emulator) | 3011 | — | `web/vite.config.js` — `server.port` + `strictPort`, proxy `/api` + `/ws` → :3010; chạy qua `make frontend` |
| `fe-*` (npm packages) | 5173–5179 (dải dành riêng, dev thử nghiệm) | — | — |

**Hạ tầng docker-shared — cấm service khác chiếm các port này:** postgres `5432` · redis `6379` · rabbitmq `5672` (15672 chỉ trong network nội bộ) · rustfs `9000` (S3) + `9001` (console) · mailpit `1025` (SMTP) + `8025` (web UI) · container lẻ `wm-frontend-nginx` `8080` · container lẻ `birthday-nginx` (trang tĩnh `others/birthday/`) `8088` — user chốt 2026-10-14.

Mẹo nhớ: 3xxx = cụm auth/WM/notification/assistant (301x = telegram-emulator); 68xx = facility; 8xxx = base + portal; 909x = gRPC; 417x = preview; 517x = `fe-*`.
