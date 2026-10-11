package scan

import (
	"testing"
)

// Fixture mô phỏng byte thật của `lsof +c0 -F0pcn -nP -iTCP -sTCP:LISTEN`:
// mỗi record kết thúc bằng \x00\n → sau split theo NUL, "\n" dính ở ĐẦU field kế.
// Bug cũ: TrimRight chỉ strip phải → mọi field 'p' sau record đầu bị bỏ qua,
// toàn bộ listener bị gán sai pid của process đầu tiên.
const fixture = "p745\x00crapportd\x00\n" +
	"f9\x00n*:49152\x00\n" +
	"p17210\x00cserver\x00\n" +
	"f18\x00n*:3000\x00\n" +
	"f21\x00n*:9090\x00\n" +
	"p75197\x00cmain\x00\n" +
	"f14\x00n*:3001\x00\n" +
	"p1078\x00cCode Helper (Plugin)\x00\n" +
	"f36\x00n127.0.0.1:49253\x00\n"

func TestParseListeners(t *testing.T) {
	ls := ParseListeners([]byte(fixture))
	if len(ls) != 5 {
		t.Fatalf("kỳ vọng 5 listener, nhận %d: %+v", len(ls), ls)
	}
	want := []Listener{
		{PID: 17210, Command: "server", Port: 3000},
		{PID: 75197, Command: "main", Port: 3001},
		{PID: 17210, Command: "server", Port: 9090},
		{PID: 745, Command: "rapportd", Port: 49152},
		{PID: 1078, Command: "Code Helper (Plugin)", Port: 49253},
	}
	for i, w := range want {
		if ls[i] != w {
			t.Errorf("listener %d: nhận %+v, muốn %+v", i, ls[i], w)
		}
	}
}

// Process có nhiều socket phải giữ đúng pid của chính nó (không kế thừa pid cũ).
func TestParseListenersNoPidLeak(t *testing.T) {
	ls := ParseListeners([]byte("p100\x00cfirst\x00\nf1\x00n*:1111\x00\np200\x00csecond\x00\nf2\x00n*:2222\x00\n"))
	if len(ls) != 2 {
		t.Fatalf("kỳ vọng 2 listener, nhận %d", len(ls))
	}
	if ls[1].PID != 200 || ls[1].Command != "second" || ls[1].Port != 2222 {
		t.Fatalf("listener 2 sai: %+v", ls[1])
	}
}
