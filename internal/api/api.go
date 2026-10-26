// Package api định nghĩa hợp đồng dữ liệu giữa backend Go và frontend
// (window.go.main.App.*) — tên kiểu frontend dùng để tham chiếu.
//
// Đây chỉ là ALIAS sang kiểu của package lõi: lõi giữ nguồn sự thật về hình
// dạng dữ liệu (JSON tags), api chỉ đặt tên hợp đồng cho gọn. Hình dạng JSON
// (camelCase) đã nằm sẵn ở package lõi:
//
//	MirrorReport { mismatches: [{ port, inAgents, inPortsYaml, note, service, group }], checkedAt, note }
//	            → internal/mirror.MirrorReport (mọi field đều marshal ra JSON — C2 + C6)
//	HealthEntry { id, port, state, code, latencyMs, detail }
//	            → internal/health.HealthEntry
//	HistoryEvent { at, kind, detail }
//	            → internal/history.Event
//	ServiceSpec { id, name, group, ports, dir, start, repoDir, composeDir, composeFile, composeService, autoRestart, healthPath, healthSkipPorts }
//	            → internal/config.ServiceSpec
//	AgentStatus { label, plistPath, binPath, logOutPath, logErrPath, installed, loaded, pid, thisIsAgent, canInstall, cannotInstallReason }
//	            → internal/daemon.AgentStatus
//	LastExitInfo { kind, at, signal, pid, detail }
//	            → internal/exitlog.Info
package api

import (
	"smatec/port-manager/internal/config"
	"smatec/port-manager/internal/daemon"
	"smatec/port-manager/internal/exitlog"
	"smatec/port-manager/internal/health"
	"smatec/port-manager/internal/history"
	"smatec/port-manager/internal/mirror"
)

// MirrorReport: kết quả đối chiếu bảng port AGENTS.md 10.1 ↔ ports.yaml.
type MirrorReport = mirror.MirrorReport

// Mismatch: một cổng lệch giữa hai nguồn.
type Mismatch = mirror.Mismatch

// HealthEntry: kết quả probe HTTP một cổng của một service.
type HealthEntry = health.HealthEntry

// HistoryEvent: một sự kiện lịch sử chạy của service.
type HistoryEvent = history.Event

// ServiceSpec: cấu hình đầy đủ của một service (thêm mới/sửa qua app).
type ServiceSpec = config.ServiceSpec

// AgentStatus: trạng thái LaunchAgent chạy ngầm (đã cài? đang chạy? pid?).
type AgentStatus = daemon.AgentStatus

// LastExitInfo: kết luận về lần thoát trước của app (quit/signal/abrupt/none).
type LastExitInfo = exitlog.Info
