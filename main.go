package main

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	_ "smatec/port-manager/internal/cgofix" // link framework UniformTypeIdentifiers (thiếu ở wails v2 + CLT mới)
	"smatec/port-manager/internal/daemon"
	"smatec/port-manager/internal/exitlog"
	"smatec/port-manager/internal/notify"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed ports.yaml
var embeddedPortsYAML []byte

//go:embed VERSION
var embeddedVersion []byte

func main() {
	// Mọi đối số đầu tiên dạng flag (bắt đầu bằng "-") đều vào CLI — kể cả
	// flag KHÔNG có trong danh sách. Nếu chỉ whitelist thì một lỗi gõ flag
	// (vd "-scna") làm app mở cửa sổ GUI oan, trong khi người gọi rõ ràng
	// muốn chạy headless. runCLI tự báo "flag không rõ" + exit 2.
	if len(os.Args) > 1 && strings.HasPrefix(os.Args[1], "-") {
		runCLI(os.Args[1:])
		return
	}
	runGUI()
}

// buildMenu dựng menu ứng dụng macOS (trên thanh menu hệ thống, như mọi app
// native khác). Menu chỉ có trên darwin — Windows/Linux giữ hành vi cũ.
//
// Menu CẦN THIẾT hơn bao giờ hết khi cửa sổ bị treo: vẫn tắt được toàn bộ
// service bằng menu Tắt tất cả kể cả khi webview không vẽ được.
func buildMenu(app *App) *menu.Menu {
	m := menu.NewMenu()
	if runtime.GOOS == "darwin" {
		// macOS bắt buộc có AppMenu (About/Quit) làm menu đầu tiên, không thì
		// menu đầu tiên của ta bị hệ thống dùng làm AppMenu.
		m.Append(menu.AppMenu())
		// WKWebView chỉ nhận ⌘C/⌘V/⌘A khi app có menu Edit với các role tương
		// ứng — thiếu nó thì bôi đen được log nhưng không copy được.
		m.Append(menu.EditMenu())
	}

	ops := m.AddSubmenu("Thao tác")
	ops.AddText("Quét lại", keys.CmdOrCtrl("r"), func(cd *menu.CallbackData) {
		app.menuAction("refresh")
	})
	ops.AddSeparator()
	ops.AddText("Bật tất cả", keys.Combo("b", keys.CmdOrCtrlKey, keys.ShiftKey), func(cd *menu.CallbackData) {
		app.menuAction("start-all")
	})
	ops.AddText("Tắt tất cả", keys.Combo("t", keys.CmdOrCtrlKey, keys.ShiftKey), func(cd *menu.CallbackData) {
		app.menuAction("stop-all")
	})
	ops.AddSeparator()
	ops.AddText("Giới thiệu", nil, func(cd *menu.CallbackData) {
		app.menuAction("about")
	})

	// Chạy ngầm: cài/gỡ LaunchAgent ngay từ menu — dùng được cả khi webview
	// treo (cùng lý do menu "Thao tác" tồn tại). "Thoát hẳn" nhắc rằng nút
	// đỏ chỉ ẩn app; muốn dừng Port Manager thật thì vào đây hoặc ⌘Q.
	bg := m.AddSubmenu("Chạy ngầm")
	bg.AddText("Trạng thái chạy ngầm…", nil, func(cd *menu.CallbackData) {
		app.menuAction("agent")
	})
	bg.AddSeparator()
	bg.AddText("Cài chạy ngầm (LaunchAgent)", nil, func(cd *menu.CallbackData) {
		app.menuAction("agent-install")
	})
	bg.AddText("Gỡ chạy ngầm", nil, func(cd *menu.CallbackData) {
		app.menuAction("agent-uninstall")
	})
	bg.AddSeparator()
	bg.AddText("Thoát hẳn Port Manager", nil, func(cd *menu.CallbackData) {
		app.menuAction("quit")
	})
	return m
}

func runGUI() {
	version := strings.TrimSpace(string(embeddedVersion))
	app, err := NewApp(version)
	if err != nil {
		fmt.Fprintln(os.Stderr, "khởi tạo app lỗi:", err)
		os.Exit(1)
	}

	// Một instance duy nhất. Instance thứ hai: đánh thức instance cũ rồi thoát
	// — mã 0 khi do người dùng mở (không để launchd coi là crash), mã 3 khi do
	// launchd mở (để launchd thử lại sau khi GUI thoát — bàn giao, xem
	// internal/daemon). Lỗi khoá KHÁC (quyền file…) → chạy tiếp, chỉ cảnh báo.
	lock, err := daemon.AcquireLock(app.stateDir)
	var already *daemon.ErrAlreadyRunning
	switch {
	case errors.As(err, &already):
		exitAlreadyRunning(already)
	case err != nil:
		fmt.Fprintln(os.Stderr, "không lấy được khoá một-instance (chạy tiếp):", err)
	default:
		app.lock = lock
	}

	// Ghi dấu phiên mới + kết luận lần thoát trước (frontend hiện banner).
	// Phải gọi trước wails.Run để OnShutdown/signal luôn có phiên để kết thúc.
	info, err := app.xlog.BeginSession(version, daemon.IsAgentProcess())
	if err != nil {
		fmt.Fprintln(os.Stderr, "ghi phiên chạy lỗi:", err)
	}
	app.lastExit = info
	app.watchSignals()

	opts := &options.App{
		Title:            "SMATEC Port Manager",
		Width:            1100,
		Height:           800,
		MinWidth:         940,
		MinHeight:        600,
		BackgroundColour: &options.RGBA{R: 15, G: 23, B: 42, A: 1},
		AssetServer:      &assetserver.Options{Assets: assets},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind:             []interface{}{app},
		// Nút đỏ chỉ ẩn app ([NSApp hide] ở tầng native), không thoát: supervisor,
		// health, tray vẫn chạy. Thoát thật qua ⌘Q / Dock Quit / tray "Thoát" /
		// menu "Thoát hẳn" — các đường đó đi qua message "Q" của Wails, không
		// qua windowShouldClose nên không bị ẩn nhầm.
		HideWindowOnClose: true,
	}
	// Menu native chỉ dành cho macOS (kế hoạch: menu ứng dụng macOS).
	if runtime.GOOS == "darwin" {
		opts.Menu = buildMenu(app)
	}
	err = wails.Run(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		app.endSession(exitlog.KindQuit, "", "wails.Run lỗi: "+err.Error())
		os.Exit(1)
	}
}

// exitAlreadyRunning xử lý khi đã có instance khác giữ khoá rồi thoát.
func exitAlreadyRunning(already *daemon.ErrAlreadyRunning) {
	if daemon.IsAgentProcess() {
		// launchd mở ta trong lúc GUI còn chạy → thoát mã khác 0 để KeepAlive
		// thử lại (~10s) — tới khi GUI thoát thì lấy được khoá.
		fmt.Fprintln(os.Stderr, already.Error(), "— instance agent chờ launchd thử lại")
		os.Exit(daemon.ExitCodeConflict)
	}
	if err := daemon.NotifyRunning(already.PID); err != nil {
		// Không đánh thức được (pid cũ, không có pid) → ít nhất báo cho người dùng.
		_ = notify.NotifySync(nil, "Port Manager đang chạy",
			"Đã có một Port Manager khác đang chạy. Mở lại từ icon radar trên menu bar hoặc Dock.")
	}
	fmt.Fprintln(os.Stderr, already.Error(), "— đã yêu cầu instance đó hiện cửa sổ")
	os.Exit(0)
}
