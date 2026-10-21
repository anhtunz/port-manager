package scan

import (
	"testing"
	"time"
)

// TestServiceStatus: trạng thái hiển thị của service. Điểm mấu chốt: process
// do app bật mà chưa lên cổng phải là "starting"/"stuck" chứ không phải
// "stopped" — nếu không, UI không hiện nút Tắt và user không tắt được service
// kẹt lúc khởi động.
func TestServiceStatus(t *testing.T) {
	now := time.Now()
	justStarted := ManagedInfo{PID: 4242, Started: now.Add(-10 * time.Second).UnixMilli(), Alive: true}
	longAgo := ManagedInfo{PID: 4242, Started: now.Add(-5 * time.Minute).UnixMilli(), Alive: true}
	dead := ManagedInfo{}

	cases := []struct {
		name      string
		open, tot int
		mi        ManagedInfo
		want      string
	}{
		{"đủ cổng → running", 2, 2, dead, "running"},
		{"một phần → partial", 1, 2, dead, "partial"},
		{"không có process → stopped", 0, 2, dead, "stopped"},
		{"app bật, chưa lên cổng, mới bật → starting", 0, 2, justStarted, "starting"},
		{"app bật, chưa lên cổng, lâu rồi → stuck", 0, 2, longAgo, "stuck"},
		{"app bật, đã lên 1 cổng → partial", 1, 2, longAgo, "partial"},
		{"app bật, đủ cổng → running", 2, 2, longAgo, "running"},
	}
	for _, c := range cases {
		got := ServiceStatus(c.open, c.tot, c.mi, now)
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
