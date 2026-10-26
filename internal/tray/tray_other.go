//go:build !darwin

// Package tray chỉ có status item trên macOS; ở hệ khác Start là no-op để
// phần còn lại của app gọi thoải mái, không cần if theo hệ điều hành.
package tray

func Start(callback func(action string)) {}
