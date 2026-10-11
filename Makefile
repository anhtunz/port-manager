# Makefile — SMATEC Port Manager
#
# Repo nằm trong workspace smatec/ có go.work ở root (yêu cầu Go ≥1.26) →
# mọi lệnh go phải GOWORK=off để không bị kéo vào workspace module khác.

export GOWORK := off

.DEFAULT_GOAL := help
.PHONY: help build run scan start stop test clean

help: ## Liệt kê các lệnh
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

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

test: ## Chạy unit test (go test được phép dùng port test 5178)
	go test ./...

clean: ## Dọn build/ + logs/
	rm -rf build logs
