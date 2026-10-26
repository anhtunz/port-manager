// Package notify gửi thông báo macOS khi service kẹt hoặc crash.
//
// Cơ chế: osascript "display notification" — không thêm phụ thuộc ngoài, chạy
// được cả khi app chưa đóng gói thành .app (chạy `go run`) và không cần xin
// quyền thông báo của hệ điều hành.
//
// Ba nguyên tắc bắt buộc:
//   - Không chặn luồng chính: Notify trả về ngay, osascript chạy nền kèm timeout.
//   - Lỗi thông báo bị bỏ qua: hỏng thông báo không được làm hỏng chức năng khác.
//   - Tắt được: đặt PORT_MANAGER_NOTIFY=0 (off/false/no) để tắt, dùng cho test/CI.
//
// Vì sao KHÔNG dùng runtime.SendNotification của Wails: hàm đó gọi
// getFrontend(ctx) → log.Fatal nếu ctx không phải ctx của Wails (pkg/runtime/runtime.go:15-30),
// tức là một ctx thường (context.Background) sẽ làm sập cả app. osascript thì
// luôn an toàn: lỗi trả về dạng error và bị bỏ qua.
package notify

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// EnvDisable là biến môi trường để tắt thông báo (test/CI, máy không muốn bị
// popup). Giá trị tắt: 0, false, off, no, none, disable, disabled (không phân
// biệt hoa thường). Rỗng/không đặt → bật.
const EnvDisable = "PORT_MANAGER_NOTIFY"

// notifyTimeout: thời gian tối đa cho một lần gọi osascript. osascript bình
// thường trả về trong ~100ms; nếu hệ thống bị vướng thì vẫn phải thoát kịp
// để không giữ goroutine mãi.
const notifyTimeout = 3 * time.Second

// notifier gom phần "gọi hệ điều hành" — tách khỏi phần chuẩn bị nội dung
// để test được mà không chạm vào osascript.
type notifier struct {
	timeout time.Duration
	send    func(ctx context.Context, script string) error
}

// dispatch chạy script trong goroutine riêng với timeout.
// Trả về lỗi của lần gọi cuối cùng (nếu có) — lỗi chỉ để log/kiểm thử.
func (n *notifier) dispatch(ctx context.Context, script string) error {
	runCtx, cancel := context.WithTimeout(baseCtx(ctx), n.timeout)
	defer cancel()
	return n.send(runCtx, script)
}

// baseCtx: ctx nil (CLI, test) → context.Background. context.WithTimeout(nil)
// sẽ panic nên phải chuẩn hóa trước.
func baseCtx(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

var (
	notifierMu sync.RWMutex
	// realNotifier là bộ gửi thật (osascript).
	realNotifier = &notifier{timeout: notifyTimeout, send: runOSAScript}
	// globalNotifier là bộ gửi đang dùng — test thay bằng bộ giả.
	globalNotifier = realNotifier
)

// currentNotifier trả bản sao của bộ gửi hiện tại. Trả bản sao (không trả
// con trỏ) để test đổi bộ gửi không đụng vào goroutine đang chạy.
func currentNotifier() *notifier {
	notifierMu.RLock()
	defer notifierMu.RUnlock()
	n := *globalNotifier
	return &n
}

// setNotifier thay bộ gửi (chỉ dùng trong test).
func setNotifier(n *notifier) {
	notifierMu.Lock()
	globalNotifier = n
	notifierMu.Unlock()
}

// runOSAScript chạy osascript với script cho sẵn.
// Script truyền qua argv (không qua shell) → không cần escape shell.
func runOSAScript(ctx context.Context, script string) error {
	cmd := exec.CommandContext(ctx, "osascript", "-e", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return fmt.Errorf("osascript: %w", err)
		}
		return fmt.Errorf("osascript: %w: %s", err, msg)
	}
	return nil
}

// Disabled báo thông báo có đang bị tắt bằng biến môi trường hay không.
func Disabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvDisable))) {
	case "0", "false", "off", "no", "none", "disable", "disabled":
		return true
	}
	return false
}

// Notify gửi thông báo hệ điều hành với tiêu đề và nội dung.
//
// Không bao giờ chặn luồng gọi: osascript chạy nền, có timeout ngắn, nên
// người gọi (supervisor, app) không bị treo vì thông báo. Lỗi thông báo bị
// bỏ qua triệt để — thông báo hỏng không được làm hỏng chức năng khác.
//
// Trả về luôn nil: xem NotifySync nếu cần biết lỗi.
func Notify(ctx context.Context, title, body string) error {
	if Disabled() {
		return nil
	}
	title = strings.TrimSpace(title)
	body = strings.TrimSpace(body)
	if title == "" && body == "" {
		return nil // không có gì để báo
	}
	script := BuildScript(title, body)
	n := currentNotifier()
	go func() {
		_ = n.dispatch(ctx, script)
	}()
	return nil
}

// NotifySync giống Notify nhưng chạy đồng bộ và trả về lỗi (nếu có).
// Dùng khi cần biết thông báo có gửi được không (vd CLI) — vẫn có timeout.
func NotifySync(ctx context.Context, title, body string) error {
	if Disabled() {
		return nil
	}
	title = strings.TrimSpace(title)
	body = strings.TrimSpace(body)
	if title == "" && body == "" {
		return nil
	}
	return currentNotifier().dispatch(ctx, BuildScript(title, body))
}

// NotifyStuck thông báo service bị kẹt (process vẫn chạy nhưng chưa lên cổng).
func NotifyStuck(ctx context.Context, id, name, detail string) error {
	m := StuckMessage(id, name, detail)
	return Notify(ctx, m.Title, m.Body)
}

// NotifyCrash thông báo service bị crash (process chết khi đang chạy).
func NotifyCrash(ctx context.Context, id, name, detail string) error {
	m := CrashMessage(id, name, detail)
	return Notify(ctx, m.Title, m.Body)
}

// Message là nội dung thông báo đã chuẩn bị xong (chưa gửi).
type Message struct {
	Title string
	Body  string
}

// StuckMessage dựng nội dung thông báo service bị kẹt.
// Hàm thuần — không chạm hệ thống, test được.
func StuckMessage(id, name, detail string) Message {
	var b strings.Builder
	b.WriteString("Service " + serviceLabel(id, name) + " bị kẹt: process vẫn chạy nhưng chưa lên cổng.")
	writeDetail(&b, detail)
	writeLogHint(&b, id)
	return Message{
		Title: "Service kẹt: " + displayName(id, name),
		Body:  b.String(),
	}
}

// CrashMessage dựng nội dung thông báo service bị crash.
// Hàm thuần — không chạm hệ thống, test được.
func CrashMessage(id, name, detail string) Message {
	var b strings.Builder
	b.WriteString("Service " + serviceLabel(id, name) + " đã crash.")
	writeDetail(&b, detail)
	writeLogHint(&b, id)
	return Message{
		Title: "Service crash: " + displayName(id, name),
		Body:  b.String(),
	}
}

// writeDetail thêm dòng "Chi tiết" nếu có detail.
func writeDetail(b *strings.Builder, detail string) {
	if d := strings.TrimSpace(detail); d != "" {
		b.WriteString("\nChi tiết: " + d)
	}
}

// writeLogHint thêm gợi ý xem log của service.
func writeLogHint(b *strings.Builder, id string) {
	if strings.TrimSpace(id) == "" {
		return
	}
	b.WriteString("\nXem log: logs/" + id + ".log")
}

// serviceLabel: "Tên hiển thị" (id: xxx) — bỏ phần id nếu không có id.
func serviceLabel(id, name string) string {
	dn := displayName(id, name)
	if strings.TrimSpace(id) == "" {
		return `"` + dn + `"`
	}
	return `"` + dn + `" (id: ` + id + `)`
}

// displayName: ưu tiên tên, không có thì dùng id, không có gì thì "service".
func displayName(id, name string) string {
	if n := strings.TrimSpace(name); n != "" {
		return n
	}
	if i := strings.TrimSpace(id); i != "" {
		return i
	}
	return "service"
}

// BuildScript dựng script AppleScript hiển thị notification.
// Nội dung rỗng → dùng title làm nội dung (notification không có body thì vô nghĩa).
func BuildScript(title, body string) string {
	if strings.TrimSpace(body) == "" {
		body = title
	}
	return "display notification " + AppleScriptString(body) + " with title " + AppleScriptString(title)
}

// AppleScriptString trả về string literal của AppleScript (đã có dấu nháy kép)
// cho chuỗi s. Escape: dấu nháy kép, dấu gạch chéo ngược, xuống dòng, tab.
// Luôn trả về literal hợp lệ — không thể quên dấu nháy.
func AppleScriptString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\r\n", `\n`)
	s = strings.ReplaceAll(s, "\r", `\n`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\t", `\t`)
	return `"` + s + `"`
}
