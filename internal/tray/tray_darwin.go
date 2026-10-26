//go:build darwin

// Package tray đặt một status item (icon radar) lên menu bar macOS để bấm
// nhanh: mở app, quét lại, bật/tắt tất cả, thoát. Wails v2 chưa có system tray
// nên dùng thẳng NSStatusItem qua cgo (tray_darwin.m).
//
// Mọi thao tác UI của Cocoa phải chạy trên main thread — nơi Wails đang giữ
// NSApplication runloop — nên file .m tự dispatch_async(dispatch_get_main_queue())
// còn callback về Go thì luôn bọc trong goroutine mới để không chặn main thread.
package tray

/*
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
extern void trayActionCB(char* action);
void traySetup(const void* png, long len, const char* tooltip);
*/
import "C"

import (
	_ "embed"
	"unsafe"
)

//go:embed assets/radar.png
var radarPNG []byte

// onAction nhận key hành động từ menu ("open", "refresh", "start-all",
// "stop-all", "about", "quit").
var onAction func(action string)

//export trayActionCB
func trayActionCB(action *C.char) {
	defer C.free(unsafe.Pointer(action))
	act := C.GoString(action)
	if onAction == nil {
		return
	}
	go onAction(act)
}

// Start đặt status item lên menu bar. Idempotent: gọi nhiều lần vẫn chỉ tạo
// một icon. Chỉ có tác dụng trên darwin (tray_other.go là no-op cho hệ khác).
func Start(callback func(action string)) {
	if callback == nil {
		return
	}
	onAction = callback
	tooltip := C.CString("SMATEC Port Manager")
	defer C.free(unsafe.Pointer(tooltip))
	C.traySetup(unsafe.Pointer(&radarPNG[0]), C.long(len(radarPNG)), tooltip)
}
