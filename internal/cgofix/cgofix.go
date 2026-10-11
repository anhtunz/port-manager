// Package cgofix bổ sung framework UniformTypeIdentifiers vào link flags.
// Wails v2 darwin frontend tham chiếu class UTType nhưng không tự link
// framework này với Xcode CLT mới → lỗi "Undefined symbols _OBJC_CLASS_$_UTType".
package cgofix

// #cgo LDFLAGS: -framework UniformTypeIdentifiers
import "C"
