# Makefile — SMATEC Port Manager
#
# Repo nằm trong workspace smatec/ có go.work ở root (yêu cầu Go ≥1.26) →
# mọi lệnh go phải GOWORK=off để không bị kéo vào workspace module khác.

export GOWORK := off

.DEFAULT_GOAL := help
.PHONY: help build run scan start stop test icons clean agent-install agent-uninstall agent-status last-exit

help: ## Liệt kê các lệnh
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

build: ## Build app desktop → build/PortManager.app
	go build -tags desktop,production -trimpath -ldflags "-s -w" -o build/port-manager .
	@bash scripts/package-mac.sh

run: build ## Build rồi mở app
	open build/PortManager.app

scan: ## Quét headless in bảng ra terminal
	go run . -scan

start: ## Bật service: make start ID=portal-fe
	go run . -start $(ID)

stop: ## Tắt service: make stop ID=portal-fe
	go run . -stop $(ID)

test: ## Chạy unit test Go + test frontend thuần (node --test)
	go test ./...
	node --test frontend/test/

# Chạy ngầm: LaunchAgent dùng BẢN COPY của binary lúc cài — sau khi build bản
# mới phải `make agent-install` lại. Cài từ binary đã build (không dùng go run:
# binary tạm của go run bị xoá ngay, launchd sẽ không mở được).
agent-install: build ## Cài LaunchAgent chạy ngầm (tự mở khi đăng nhập, tự mở lại khi bị tắt)
	./build/port-manager -install-agent

agent-uninstall: ## Gỡ LaunchAgent chạy ngầm
	go run . -uninstall-agent

agent-status: ## Xem trạng thái LaunchAgent (exit 1 nếu chưa cài / không chạy)
	go run . -agent-status

last-exit: ## In lý do app GUI thoát lần trước (quit / signal / đột ngột)
	go run . -last-exit

icons: ## Vẽ lại assets/appicon.png + icon radar menu bar (thuần Go, không cần tool ngoài)
	go run scripts/genicons.go

clean: ## Dọn build/ + logs/
	rm -rf build logs
