package scan

import (
	"testing"
	"time"
)

// Fixture là output thật của `ps -axo pid=,lstart=,command=` trên macOS.
// Lưu ý ps pad cột lstart bằng space → ngày 1 chữ số có 2 space ("Oct  6").
const psFixture = `  183 Wed Oct 21 10:54:03 2026     /usr/libexec/sysmond
 432 Wed Oct 21 09:39:05 2026     /Applications/Brave Browser.app/Contents/MacOS/Brave Browser --type=renderer
1001 Mon Oct  6 08:07:06 2025     /bin/sh -c npm run dev
`

func TestParsePsOutput(t *testing.T) {
	infos := ParsePsOutput(psFixture)
	if len(infos) != 3 {
		t.Fatalf("kỳ vọng 3 process, nhận %d: %+v", len(infos), infos)
	}
	p := infos[183]
	if p.Command != "/usr/libexec/sysmond" {
		t.Errorf("command sai: %q", p.Command)
	}
	want := time.Date(2026, 10, 21, 10, 54, 3, 0, time.Local)
	if !p.StartedAt.Equal(want) {
		t.Errorf("startedAt sai: nhận %v, muốn %v", p.StartedAt, want)
	}
}

// Command có dấu cách bên trong phải giữ nguyên, không bị bóp méo.
func TestParsePsOutputKeepsCommandSpaces(t *testing.T) {
	infos := ParsePsOutput(psFixture)
	got := infos[432].Command
	want := "/Applications/Brave Browser.app/Contents/MacOS/Brave Browser --type=renderer"
	if got != want {
		t.Errorf("command sai:\n nhận %q\n muốn %q", got, want)
	}
}

// Ngày 1 chữ số: ps in "Oct  6" (2 space) — parser phải gộp về "6".
func TestParsePsOutputSingleDigitDay(t *testing.T) {
	infos := ParsePsOutput(psFixture)
	p := infos[1001]
	want := time.Date(2025, 10, 6, 8, 7, 6, 0, time.Local)
	if !p.StartedAt.Equal(want) {
		t.Errorf("startedAt sai: nhận %v, muốn %v", p.StartedAt, want)
	}
	if p.Command != "/bin/sh -c npm run dev" {
		t.Errorf("command sai: %q", p.Command)
	}
}

// Dòng rác (header, dòng trống) không được tạo process giả.
func TestParsePsOutputSkipsGarbage(t *testing.T) {
	infos := ParsePsOutput("  PID STARTED COMMAND\n\n   \n")
	if len(infos) != 0 {
		t.Fatalf("kỳ vọng 0 process, nhận %d: %+v", len(infos), infos)
	}
}
