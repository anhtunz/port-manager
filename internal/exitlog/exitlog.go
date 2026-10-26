// Package exitlog ghi lại "phiên chạy" của app và lý do app thoát, để lần mở
// sau biết lần trước thoát vì ai.
//
// Hai file trong thư mục state (~/.port-manager/state):
//
//   - app.session.json  — ghi lúc KHỞI ĐỘNG (pid, giờ bắt đầu, version), xoá
//     lúc thoát sạch. Khởi động mà file này còn ⇒ phiên trước kết thúc mà
//     không kịp ghi lý do ⇒ thoát ĐỘT NGỘT (kill -9, mất điện, crash cứng).
//   - app.lastexit.json — ghi lúc thoát sạch (người dùng thoát) hoặc khi nhận
//     signal (SIGTERM/SIGINT/SIGHUP) — những trường hợp app còn kịp ghi.
//
// Vì sao không chỉ dùng một file: SIGKILL không cho app chạy bất kỳ dòng code
// nào, nên "không có dấu vết thoát" chính là bằng chứng — phải để lại dấu
// "đang chạy" từ lúc khởi động mới suy ra được.
//
// Mọi hàm ghi đều nuốt lỗi file ở mức "trả error cho caller log", không panic:
// hỏng exitlog không được làm app không mở được.
package exitlog

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Kind của lần thoát trước — hợp đồng với frontend (LastExitInfo.kind).
const (
	KindNone   = "none"   // không có dữ liệu (lần đầu chạy)
	KindQuit   = "quit"   // thoát bình thường (⌘Q, tray Thoát, Dock Quit)
	KindSignal = "signal" // bị gửi signal (kill, launchctl bootout, Ctrl-C)
	KindAbrupt = "abrupt" // chết không kịp ghi (kill -9, mất điện)
)

const (
	SessionFile  = "app.session.json"
	LastExitFile = "app.lastexit.json"
)

// Info là kết luận về lần thoát trước, trả cho GUI/CLI. JSON camelCase là hợp
// đồng với frontend — không đổi.
type Info struct {
	Kind   string `json:"kind"`   // none|quit|signal|abrupt
	At     string `json:"at"`     // RFC3339: lúc thoát (quit/signal) hoặc lúc phiên bắt đầu (abrupt)
	Signal string `json:"signal"` // SIGTERM|SIGINT|SIGHUP… khi kind=signal
	PID    int    `json:"pid"`    // pid của phiên trước
	Detail string `json:"detail"` // câu giải thích cho người dùng (tiếng Việt)
}

// session là dấu "đang chạy" ghi lúc khởi động.
type session struct {
	PID       int    `json:"pid"`
	Exe       string `json:"exe"`
	Version   string `json:"version"`
	StartedAt string `json:"startedAt"`
	Agent     bool   `json:"agent"` // được launchd khởi động
}

// exitRecord là nội dung app.lastexit.json.
type exitRecord struct {
	At     string `json:"at"`
	Kind   string `json:"kind"`
	Signal string `json:"signal,omitempty"`
	PID    int    `json:"pid"`
	Detail string `json:"detail,omitempty"`
}

// Store ghi/đọc exitlog trong một thư mục.
type Store struct {
	dir string
	now func() time.Time
}

// New tạo Store ở thư mục dir (thường là stateDir của app).
func New(dir string) *Store {
	return &Store{dir: dir, now: time.Now}
}

// Dir trả thư mục exitlog.
func (s *Store) Dir() string { return s.dir }

func (s *Store) sessionPath() string  { return filepath.Join(s.dir, SessionFile) }
func (s *Store) lastExitPath() string { return filepath.Join(s.dir, LastExitFile) }

// BeginSession kết luận về phiên TRƯỚC rồi ghi dấu phiên MỚI.
//
// Thứ tự quan trọng: phải đọc dấu phiên cũ trước khi ghi đè — đó là bằng
// chứng duy nhất của thoát đột ngột. Kết luận trả về nên được cache (App giữ
// lại) vì gọi lần hai sẽ không còn thấy phiên cũ.
//
// Lỗi ghi file không chặn app: trả Info kèm err để caller log.
func (s *Store) BeginSession(version string, agent bool) (Info, error) {
	info := s.concludePrevious()
	// Thoát đột ngột → ghi luôn thành lastexit để CLI/lần sau đọc được cùng
	// kết luận, không phải suy lại từ session cũ (đã bị ghi đè).
	if info.Kind == KindAbrupt {
		_ = s.writeJSON(s.lastExitPath(), exitRecord{
			At: info.At, Kind: KindAbrupt, PID: info.PID, Detail: info.Detail,
		})
	}
	exe, _ := os.Executable()
	err := s.writeJSON(s.sessionPath(), session{
		PID:       os.Getpid(),
		Exe:       exe,
		Version:   version,
		StartedAt: s.now().Format(time.RFC3339),
		Agent:     agent,
	})
	return info, err
}

// concludePrevious đọc dấu vết còn lại và suy ra lần thoát trước.
func (s *Store) concludePrevious() Info {
	var sess session
	hasSess := s.readJSON(s.sessionPath(), &sess) == nil && sess.PID > 0
	var last exitRecord
	hasLast := s.readJSON(s.lastExitPath(), &last) == nil && last.Kind != ""

	if hasSess {
		// Có lastexit ghi SAU lúc phiên cũ bắt đầu ⇒ phiên cũ đã ghi lý do
		// nhưng chết trước khi kịp xoá session (hiếm) → tin lastexit.
		if hasLast && !before(last.At, sess.StartedAt) {
			return fromRecord(last)
		}
		started := fmtVN(sess.StartedAt)
		return Info{
			Kind: KindAbrupt,
			At:   sess.StartedAt,
			PID:  sess.PID,
			Detail: fmt.Sprintf(
				"Phiên trước (pid %d, mở lúc %s) kết thúc mà không ghi được lý do — thường là bị kill -9 (vd script dọn cổng `lsof -ti:<port> | xargs kill -9`), mất điện hoặc hệ thống tắt cứng.",
				sess.PID, started),
		}
	}
	if hasLast {
		return fromRecord(last)
	}
	return Info{Kind: KindNone}
}

func fromRecord(r exitRecord) Info {
	kind := r.Kind
	switch kind {
	case KindQuit, KindSignal, KindAbrupt:
	default:
		kind = KindNone
	}
	return Info{Kind: kind, At: r.At, Signal: r.Signal, PID: r.PID, Detail: r.Detail}
}

// EndSession ghi lý do thoát và xoá dấu phiên. Gọi ở OnShutdown (kind=quit)
// hoặc trong signal handler (kind=signal).
func (s *Store) EndSession(kind, sig, detail string) error {
	if kind == "" {
		kind = KindQuit
	}
	rec := exitRecord{
		At:     s.now().Format(time.RFC3339),
		Kind:   kind,
		Signal: sig,
		PID:    os.Getpid(),
		Detail: detail,
	}
	err := s.writeJSON(s.lastExitPath(), rec)
	// Xoá session SAU khi ghi lastexit: nếu chết giữa hai bước, lần sau vẫn
	// còn session + lastexit mới hơn → concludePrevious tin lastexit.
	if rmErr := os.Remove(s.sessionPath()); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) && err == nil {
		err = rmErr
	}
	return err
}

// RecordSignal ghi lý do thoát vì signal (tên chuẩn SIGTERM/SIGINT/…).
func (s *Store) RecordSignal(sig os.Signal) error {
	name := SignalName(sig)
	return s.EndSession(KindSignal, name, signalDetail(name))
}

// Last đọc kết luận đã ghi (lastexit) — dùng cho CLI; GUI dùng kết quả cache
// của BeginSession.
func (s *Store) Last() Info {
	var last exitRecord
	if err := s.readJSON(s.lastExitPath(), &last); err != nil || last.Kind == "" {
		return Info{Kind: KindNone}
	}
	return fromRecord(last)
}

// SignalName trả tên chuẩn (SIGTERM…) thay cho sig.String() ("terminated").
func SignalName(sig os.Signal) string {
	switch sig {
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGHUP:
		return "SIGHUP"
	case syscall.SIGQUIT:
		return "SIGQUIT"
	}
	if sig == nil {
		return ""
	}
	return strings.ToUpper(sig.String())
}

// signalDetail giải thích signal theo ngôn ngữ người dùng.
func signalDetail(name string) string {
	switch name {
	case "SIGTERM":
		return "Bị yêu cầu dừng (kill/`launchctl bootout`/tắt máy)."
	case "SIGINT":
		return "Bị ngắt (Ctrl-C ở terminal)."
	case "SIGHUP":
		return "Terminal mở app đã đóng (SIGHUP) — chạy ngầm qua LaunchAgent sẽ không bị thế này."
	}
	return "Bị gửi tín hiệu " + name + "."
}

// before: a < b theo RFC3339; không parse được → false (coi như không sớm hơn).
func before(a, b string) bool {
	ta, err1 := time.Parse(time.RFC3339, a)
	tb, err2 := time.Parse(time.RFC3339, b)
	if err1 != nil || err2 != nil {
		return false
	}
	return ta.Before(tb)
}

// fmtVN định dạng RFC3339 → "HH:MM:SS DD/MM/YYYY" cho câu giải thích.
func fmtVN(rfc string) string {
	t, err := time.Parse(time.RFC3339, rfc)
	if err != nil {
		return rfc
	}
	return t.Local().Format("15:04:05 02/01/2006")
}

func (s *Store) readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// writeJSON ghi file tạm + rename để không bao giờ có file nửa vời.
func (s *Store) writeJSON(path string, v any) error {
	if strings.TrimSpace(s.dir) == "" {
		return errors.New("chưa có thư mục state")
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("tạo thư mục state: %w", err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
