package scan

import (
	"path/filepath"
	"reflect"
	"testing"
)

// Fixture là output thật của `lsof -a -p <pids> -d cwd -Fn` trên macOS.
const lsofCwdFixture = `p183
fcwd
n/Users/tunztunzz/Documents/smatec/assistant-service/frontend
p14825
fcwd
n/Users/tunztunzz/Library/Containers/com.docker.docker
`

func TestParseLsofCwd(t *testing.T) {
	got := ParseLsofCwd(lsofCwdFixture)
	if len(got) != 2 {
		t.Fatalf("kỳ vọng 2 pid, nhận %d: %+v", len(got), got)
	}
	if got[183] != "/Users/tunztunzz/Documents/smatec/assistant-service/frontend" {
		t.Errorf("cwd của pid 183 sai: %q", got[183])
	}
	if got[14825] != "/Users/tunztunzz/Library/Containers/com.docker.docker" {
		t.Errorf("cwd của pid 14825 sai: %q", got[14825])
	}
}

// Output rác (dòng lạ, pid không parse được) không được làm hỏng kết quả.
func TestParseLsofCwdIgnoresGarbage(t *testing.T) {
	got := ParseLsofCwd("p\nfcwd\nn/no-pid\np42\nfcwd\nn/tmp/x\r\n")
	if len(got) != 1 {
		t.Fatalf("kỳ vọng chỉ 1 pid hợp lệ, nhận %d: %+v", len(got), got)
	}
	if got[42] != "/tmp/x" {
		t.Errorf("cwd sai: %q", got[42])
	}
}

func TestParseDockerPorts(t *testing.T) {
	cases := []struct {
		field string
		want  []int
	}{
		{"0.0.0.0:8088->80/tcp, [::]:8088->80/tcp", []int{8088}},
		{"0.0.0.0:9000-9001->9000-9001/tcp, [::]:9000-9001->9000-9001/tcp", []int{9000, 9001}},
		{"0.0.0.0:5432->5432/tcp, [::]:5432->5432/tcp", []int{5432}},
		{"", nil},
		{"5432/tcp", nil}, // port nội bộ, không publish ra host
	}
	for _, c := range cases {
		got := ParseDockerPorts(c.field)
		if len(got) == 0 && len(c.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseDockerPorts(%q) = %v, muốn %v", c.field, got, c.want)
		}
	}
}

// relToRoot rút gọn đường dẫn trong workspace về tương đối, giữ nguyên đường dẫn
// nằm ngoài workspace.
func TestRelToRoot(t *testing.T) {
	root := filepath.FromSlash("/Users/tunztunzz/Documents/smatec")
	cases := []struct {
		in   string
		want string
	}{
		{filepath.FromSlash("/Users/tunztunzz/Documents/smatec/assistant-service/frontend"), "assistant-service/frontend"},
		{filepath.FromSlash("/Users/tunztunzz/Documents/smatec/others/birthday"), "others/birthday"},
		{root, "."},
		{filepath.FromSlash("/opt/homebrew/bin/node"), "/opt/homebrew/bin/node"},
		{"", ""},
	}
	for _, c := range cases {
		if got := relToRoot(root, c.in); got != c.want {
			t.Errorf("relToRoot(%q) = %q, muốn %q", c.in, got, c.want)
		}
	}
}
