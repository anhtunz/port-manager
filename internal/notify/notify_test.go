package notify

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Phần thuần: chuẩn bị nội dung — không chạm osascript, không chạm hệ thống
// ---------------------------------------------------------------------------

// TestStuckMessage: nội dung thông báo service kẹt phải nói rõ service nào,
// vì sao kẹt và xem log ở đâu.
func TestStuckMessage(t *testing.T) {
	m := StuckMessage("wm-be", "WM Backend", "đã chạy 120s mà chưa lên cổng 8080")
	if m.Title != "Service kẹt: WM Backend" {
		t.Fatalf("sai title: %q", m.Title)
	}
	for _, want := range []string{"WM Backend", "id: wm-be", "bị kẹt", "chưa lên cổng", "120s", "logs/wm-be.log"} {
		if !strings.Contains(m.Body, want) {
			t.Errorf("body thiếu %q: %q", want, m.Body)
		}
	}
}

// TestCrashMessage: nội dung thông báo crash phải nêu service và log.
func TestCrashMessage(t *testing.T) {
	m := CrashMessage("wm-fe", "WM Frontend", "exit status 1")
	if m.Title != "Service crash: WM Frontend" {
		t.Fatalf("sai title: %q", m.Title)
	}
	for _, want := range []string{"WM Frontend", "id: wm-fe", "crash", "exit status 1", "logs/wm-fe.log"} {
		if !strings.Contains(m.Body, want) {
			t.Errorf("body thiếu %q: %q", want, m.Body)
		}
	}
}

// TestMessageNoDetail: không có detail thì không dòng "Chi tiết" rỗng.
func TestMessageNoDetail(t *testing.T) {
	m := StuckMessage("wm-be", "WM Backend", "   ")
	if strings.Contains(m.Body, "Chi tiết") {
		t.Errorf("không được có dòng Chi tiết khi detail rỗng: %q", m.Body)
	}
	if !strings.Contains(m.Body, "logs/wm-be.log") {
		t.Errorf("thiếu gợi ý log: %q", m.Body)
	}
}

// TestMessageNoID: không có id thì không gợi ý log (tránh "logs/.log").
func TestMessageNoID(t *testing.T) {
	m := CrashMessage("", "WM Backend", "")
	if strings.Contains(m.Body, "logs/") {
		t.Errorf("không có id thì không được gợi ý log: %q", m.Body)
	}
	if !strings.Contains(m.Body, "WM Backend") {
		t.Errorf("thiếu tên service: %q", m.Body)
	}
}

// TestDisplayNameFallback: không có tên thì dùng id, không có gì thì "service".
func TestDisplayNameFallback(t *testing.T) {
	if got := displayName("wm-be", ""); got != "wm-be" {
		t.Errorf("không có tên phải dùng id, nhận %q", got)
	}
	if got := displayName("wm-be", "  "); got != "wm-be" {
		t.Errorf("tên chỉ khoảng trắng phải dùng id, nhận %q", got)
	}
	if got := displayName("", ""); got != "service" {
		t.Errorf("không có gì phải dùng \"service\", nhận %q", got)
	}
}

// TestAppleScriptStringEscape: ký tự đặc biệt phải escape để không vỡ script.
// Kết quả luôn là string literal (có dấu nháy kép bao ngoài).
func TestAppleScriptStringEscape(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`không có gì đặc biệt`, `"không có gì đặc biệt"`},
		{`có "dấu nháy kép"`, `"có \"dấu nháy kép\""`},
		{`có \ dấu gạch chéo`, `"có \\ dấu gạch chéo"`},
		{"dòng 1\ndòng 2", `"dòng 1\ndòng 2"`},
		{"dòng 1\r\ndòng 2", `"dòng 1\ndòng 2"`},
		{"tab\there", `"tab\there"`},
		{`\n giữ nguyên nghĩa là backslash+n`, `"\\n giữ nguyên nghĩa là backslash+n"`},
	}
	for _, c := range cases {
		if got := AppleScriptString(c.in); got != c.want {
			t.Errorf("AppleScriptString(%q) = %q, muốn %q", c.in, got, c.want)
		}
	}
}

// TestBuildScript: script phải đúng cú pháp, có escape.
func TestBuildScript(t *testing.T) {
	got := BuildScript("Tiêu đề", "Nội dung")
	want := `display notification "Nội dung" with title "Tiêu đề"`
	if got != want {
		t.Fatalf("sai script:\n nhận %q\n muốn %q", got, want)
	}

	got = BuildScript(`a"b`, `c"d`)
	want = `display notification "c\"d" with title "a\"b"`
	if got != want {
		t.Fatalf("sai script khi có dấu nháy:\n nhận %q\n muốn %q", got, want)
	}
}

// TestBuildScriptNoBody: body rỗng thì dùng title làm nội dung.
func TestBuildScriptNoBody(t *testing.T) {
	got := BuildScript("Chỉ có title", "   ")
	want := `display notification "Chỉ có title" with title "Chỉ có title"`
	if got != want {
		t.Fatalf("sai script:\n nhận %q\n muốn %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// Phần gửi: dùng bộ gửi giả — KHÔNG gọi osascript thật
// ---------------------------------------------------------------------------

// installSender thay bộ gửi bằng hàm giả cho test, tự khôi phục khi test xong.
func installSender(t *testing.T, fn func(ctx context.Context, script string) error) {
	t.Helper()
	setNotifier(&notifier{timeout: 500 * time.Millisecond, send: fn})
	t.Cleanup(func() { setNotifier(realNotifier) })
}

// TestDisabledByEnv: biến môi trường tắt thông báo — không gọi gì cả.
func TestDisabledByEnv(t *testing.T) {
	for _, v := range []string{"0", "false", "off", "no", "none", "disable", "disabled", "OFF", " Off "} {
		t.Setenv(EnvDisable, v)
		if !Disabled() {
			t.Errorf("%s=%q phải tắt thông báo", EnvDisable, v)
		}
	}
	for _, v := range []string{"", "1", "true", "on", "yes"} {
		t.Setenv(EnvDisable, v)
		if Disabled() {
			t.Errorf("%s=%q phải BẬT thông báo", EnvDisable, v)
		}
	}
}

// TestNotifyDisabledDoesNotSend: tắt bằng biến môi trường thì không gửi gì.
func TestNotifyDisabledDoesNotSend(t *testing.T) {
	t.Setenv(EnvDisable, "0")
	called := make(chan string, 1)
	installSender(t, func(ctx context.Context, script string) error {
		called <- script
		return nil
	})
	if err := Notify(context.Background(), "tiêu đề", "nội dung"); err != nil {
		t.Fatalf("Notify phải trả nil, nhận %v", err)
	}
	select {
	case s := <-called:
		t.Fatalf("đã tắt thông báo mà vẫn gửi: %q", s)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestNotifySendsScript: nội dung gửi đi đúng bằng script dựng từ title/body.
func TestNotifySendsScript(t *testing.T) {
	got := make(chan string, 1)
	installSender(t, func(ctx context.Context, script string) error {
		got <- script
		return nil
	})
	if err := Notify(context.Background(), "Tiêu đề", "Nội dung"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	select {
	case s := <-got:
		want := BuildScript("Tiêu đề", "Nội dung")
		if s != want {
			t.Fatalf("sai script:\n nhận %q\n muốn %q", s, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("không gửi thông báo")
	}
}

// TestNotifyDoesNotBlock: osascript treo KHÔNG được chặn luồng gọi.
func TestNotifyDoesNotBlock(t *testing.T) {
	release := make(chan struct{})
	finished := make(chan struct{})
	installSender(t, func(ctx context.Context, script string) error {
		defer close(finished)
		<-release // treo cho tới khi test giải phóng
		return nil
	})
	start := time.Now()
	if err := Notify(context.Background(), "Tiêu đề", "Nội dung"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 200*time.Millisecond {
		t.Fatalf("Notify chặn luồng chính %v — phải trả về ngay", elapsed)
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine gửi không kết thúc")
	}
}

// TestNotifyIgnoresSenderError: lỗi thông báo bị bỏ qua, không nổi lên.
func TestNotifyIgnoresSenderError(t *testing.T) {
	called := make(chan struct{}, 1)
	installSender(t, func(ctx context.Context, script string) error {
		called <- struct{}{}
		return errors.New("osascript: hỏng")
	})
	if err := Notify(context.Background(), "tiêu đề", "nội dung"); err != nil {
		t.Fatalf("lỗi thông báo phải bị bỏ qua, nhận %v", err)
	}
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("không gọi bộ gửi")
	}
}

// TestNotifyEmptyMessage: không có gì để báo thì không gọi osascript.
func TestNotifyEmptyMessage(t *testing.T) {
	called := make(chan struct{}, 1)
	installSender(t, func(ctx context.Context, script string) error {
		called <- struct{}{}
		return nil
	})
	if err := Notify(context.Background(), "   ", "  "); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	select {
	case <-called:
		t.Fatal("không được gửi thông báo rỗng")
	case <-time.After(200 * time.Millisecond):
	}
}

// TestNotifyNilContext: ctx nil (CLI) không được panic.
func TestNotifyNilContext(t *testing.T) {
	got := make(chan string, 1)
	installSender(t, func(ctx context.Context, script string) error {
		got <- script
		return nil
	})
	//nolint:staticcheck // cố tình truyền nil để kiểm tra không panic
	if err := Notify(nil, "tiêu đề", "nội dung"); err != nil {
		t.Fatalf("Notify(nil ctx): %v", err)
	}
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("không gửi thông báo")
	}
}

// TestNotifyStuckAndCrash: 2 hàm chuyên biệt dựng đúng tiêu đề/nội dung.
func TestNotifyStuckAndCrash(t *testing.T) {
	scripts := make(chan string, 4)
	installSender(t, func(ctx context.Context, script string) error {
		scripts <- script
		return nil
	})
	if err := NotifyStuck(context.Background(), "wm-be", "WM Backend", "chưa lên cổng 8080"); err != nil {
		t.Fatalf("NotifyStuck: %v", err)
	}
	if err := NotifyCrash(context.Background(), "wm-fe", "WM Frontend", "exit status 1"); err != nil {
		t.Fatalf("NotifyCrash: %v", err)
	}
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case s := <-scripts:
			seen[s] = true
		case <-time.After(2 * time.Second):
			t.Fatal("không đủ 2 thông báo")
		}
	}
	if !seen[BuildScript("Service kẹt: WM Backend", StuckMessage("wm-be", "WM Backend", "chưa lên cổng 8080").Body)] {
		t.Errorf("thiếu thông báo kẹt: %v", seen)
	}
	if !seen[BuildScript("Service crash: WM Frontend", CrashMessage("wm-fe", "WM Frontend", "exit status 1").Body)] {
		t.Errorf("thiếu thông báo crash: %v", seen)
	}
}

// TestDispatchTimeout: osascript treo phải bị cắt sau timeout.
func TestDispatchTimeout(t *testing.T) {
	n := &notifier{
		timeout: 50 * time.Millisecond,
		send: func(ctx context.Context, script string) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	start := time.Now()
	err := n.dispatch(context.Background(), "display notification \"x\"")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("phải trả lỗi khi hết timeout")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("timeout không có hiệu lực: %v", elapsed)
	}
}

// TestDispatchRespectsCancelledContext: ctx đã hủy thì không được im lặng coi
// như thành công — phải trả lỗi.
func TestDispatchRespectsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n := &notifier{
		timeout: time.Second,
		send: func(ctx context.Context, script string) error {
			return ctx.Err()
		},
	}
	if err := n.dispatch(ctx, "display notification \"x\""); err == nil {
		t.Fatal("ctx đã hủy thì phải trả lỗi")
	}
}

// TestNotifySyncReturnsError: bản đồng bộ trả lỗi để caller biết.
func TestNotifySyncReturnsError(t *testing.T) {
	installSender(t, func(ctx context.Context, script string) error {
		return errors.New("osascript: hỏng")
	})
	err := NotifySync(context.Background(), "tiêu đề", "nội dung")
	if err == nil {
		t.Fatal("NotifySync phải trả lỗi khi osascript hỏng")
	}
}
