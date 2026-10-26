package mirror

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"smatec/port-manager/internal/config"
)

// sampleSection là một mẫu đại diện cho mục 10.1: bảng markdown lẫn văn xuôi,
// port trong ngoặc, dải cổng (EN DASH), ô rỗng (EM DASH), cột "Port đặt ở đâu"
// chứa `--port 3003` / `PORT=6789` / `→ :3005`, dòng hạ tầng văn xuôi và dòng
// "Mẹo nhớ".
const sampleSection = `## 10. Quy hoạch port & quy tắc chạy server (bắt buộc)

Bảng dưới là **nguồn sự thật duy nhất** về port.

### 10.1 Bảng port local dev

**Backend Go:**

| Service | HTTP | gRPC | Port đặt ở đâu |
| --- | --- | --- | --- |
| ` + "`auth-service/backend`" + ` | 3000 | 9090 | ` + "`.env`" + ` — ` + "`HTTP_PORT`" + ` |
| ` + "`notification-service/backend`" + ` | 3004 (REST healthz/version) | 9091 | ` + "`.env`" + ` — ` + "`SERVER_PORT`" + ` |
| ` + "`base/backend`" + ` | 8090 | — (chỉ làm gRPC client) | ` + "`.env`" + ` — ` + "`SERVER_PORT`" + ` |

**Frontend:**

| App | Dev server | Preview (build prod) | Port đặt ở đâu |
| --- | --- | --- | --- |
| ` + "`auth-service/auth-frontend`" + ` | 3003 | 4176 | script ` + "`dev`" + ` (` + "`--port 3003`" + `) + ` + "`vite.config.ts`" + ` (` + "`strictPort`" + `, ` + "`preview.port`" + `) |
| ` + "`warehouse-management/frontend`" + ` | 6789 (utoopack mở thêm 6790 nội bộ) | — | script ` + "`dev`" + ` (` + "`PORT=6789`" + `) |
| ` + "`fe-*`" + ` (npm packages) | 5173–5179 (dải dành riêng, dev thử nghiệm) | — | — |

**Hạ tầng docker-shared — cấm service khác chiếm các port này:** postgres ` + "`5432`" + ` · redis ` + "`6379`" + ` · rabbitmq ` + "`5672`" + ` (15672 chỉ trong network nội bộ) · rustfs ` + "`9000`" + ` (S3) + ` + "`9001`" + ` (console) · mailpit ` + "`1025`" + ` (SMTP) + ` + "`8025`" + ` (web UI) · container lẻ ` + "`wm-frontend-nginx`" + ` ` + "`8080`" + ` · container lẻ ` + "`birthday-nginx`" + ` (trang tĩnh ` + "`others/birthday/`" + `) ` + "`8088`" + ` — user chốt 2026-10-14.

Mẹo nhớ: 3xxx = cụm auth/WM/notification/assistant; 68xx = facility; 8xxx = base + portal; 909x = gRPC; 417x = preview; 517x = ` + "`fe-*`" + `.

### 10.2 Quy tắc cho agent khi chạy test/server

- Chỉ được khởi động thứ lắng nghe port trên **đúng port đã khai báo ở 10.1** — port 9999 nằm trong mục này KHÔNG được lấy.
`

// wantSamplePorts: tập port thuộc phạm vi so sánh của mẫu trên.
var wantSamplePorts = []int{
	1025, 3000, 3003, 3004, 5432, 5672, 6379, 6789, 6790,
	8025, 8080, 8088, 8090, 9000, 9001,
}

func parseSample(t *testing.T) Table {
	t.Helper()
	tbl, err := Parse([]byte(sampleSection))
	if err != nil {
		t.Fatalf("Parse lỗi: %v", err)
	}
	return tbl
}

func TestParse_MauBangMarkdown(t *testing.T) {
	tbl := parseSample(t)

	got := tbl.SortedPorts()
	if !equalInts(got, wantSamplePorts) {
		t.Errorf("tập port so sánh = %v, muốn %v", got, wantSamplePorts)
	}
}

func TestParse_SoThuanVaPortTrongNgoac(t *testing.T) {
	tbl := parseSample(t)

	// Số thuần.
	if !tbl.Ports[3000] {
		t.Error("thiếu port 3000 (số thuần trong cột HTTP)")
	}
	// Số + ngoặc → lấy số trong ngoặc, bỏ chữ.
	if !tbl.Ports[3004] {
		t.Error("thiếu port 3004 (ô có ghi chú trong ngoặc)")
	}
	// Port trong ngoặc → đánh dấu "cần kiểm".
	if !tbl.Secondary[6790] {
		t.Error("port 6790 (ghi trong ngoặc) phải được đánh dấu secondary")
	}
	if tbl.Secondary[6789] {
		t.Error("port 6789 đứng ngoài ngoặc → không được đánh dấu secondary")
	}
}

func TestParse_ÔRongEmDashKhongSinhPort(t *testing.T) {
	tbl := parseSample(t)

	// Ô "—" (EM DASH U+2014) ở cột gRPC/preview không sinh port nào.
	for _, p := range []int{4173, 4174, 4175, 9092, 9093} {
		if tbl.Ports[p] {
			t.Errorf("port %d không được lấy từ ô rỗng", p)
		}
	}
}

func TestParse_DaiCongENDash(t *testing.T) {
	tbl := parseSample(t)

	// Dải 5173–5179 (EN DASH U+2013) phải được bung ra 7 port…
	if len(tbl.Ranges) != 1 {
		t.Fatalf("số dải đọc được = %d, muốn 1 (%+v)", len(tbl.Ranges), tbl.Ranges)
	}
	r := tbl.Ranges[0]
	if r.From != 5173 || r.To != 5179 {
		t.Errorf("dải = %d-%d, muốn 5173-5179", r.From, r.To)
	}
	if len(r.Ports) != 7 {
		t.Errorf("dải bung ra %d port, muốn 7 (%v)", len(r.Ports), r.Ports)
	}
	// …nhưng KHÔNG vào tập so sánh 1-1 (dải dành riêng, không phải service).
	for _, p := range r.Ports {
		if tbl.Ports[p] {
			t.Errorf("port %d trong dải không được vào tập so sánh", p)
		}
	}
}

func TestParse_CotKhongPhaiCongKhongSinhPort(t *testing.T) {
	tbl := parseSample(t)

	// Cột "Port đặt ở đâu" có `--port 3003`, `PORT=6789`, `→ :3005` — không lấy.
	// 3003/6789 vẫn phải có (ở cột Dev server), 3005 (chỉ có ở cột 4) thì không.
	if !tbl.Ports[3003] || !tbl.Ports[6789] {
		t.Error("port ở cột Dev server phải được lấy")
	}
	if tbl.Ports[3005] {
		t.Error("port 3005 chỉ nằm ở cột \"Port đặt ở đâu\" → không được lấy")
	}
}

func TestParse_CotGrpcVaPreviewKhongVaoTapSoSanh(t *testing.T) {
	tbl := parseSample(t)

	// Cột gRPC (9090/9091) và preview (4176) bị loại khỏi tập so sánh…
	for _, p := range []int{9090, 9091, 4176} {
		if tbl.Ports[p] {
			t.Errorf("port %d thuộc cột bị loại → không được vào tập so sánh", p)
		}
	}
	// …nhưng vẫn được ghi nhận để loại ở cả phía ports.yaml (không lệch giả).
	for _, p := range []int{9090, 9091, 4176} {
		if !tbl.Ignored[p] {
			t.Errorf("port %d thuộc cột bị loại phải nằm trong Ignored", p)
		}
	}
	// Port ở cột Dev server không bị loại nhầm dù có xuất hiện ở cột 4.
	if tbl.Ignored[3003] || tbl.Ignored[6789] {
		t.Error("port ở cột port không được nằm trong Ignored")
	}
}

func TestParse_DongVanXuanChiLayPortTrongBacktick(t *testing.T) {
	tbl := parseSample(t)

	// Dòng hạ tầng: đúng 9 port trong backtick.
	want := []int{1025, 5432, 5672, 6379, 8025, 8080, 8088, 9000, 9001}
	for _, p := range want {
		if !tbl.Ports[p] {
			t.Errorf("thiếu port hạ tầng %d", p)
		}
	}
	// 15672 nằm ngoài backtick → không lấy.
	if tbl.Ports[15672] {
		t.Error("15672 nằm ngoài backtick → không được lấy")
	}
	// Backtick không phải số (tên container, đường dẫn) → không lấy.
	for _, p := range []int{14, 2026} {
		if tbl.Ports[p] {
			t.Errorf("port %d lấy từ backtick không phải số → không được lấy", p)
		}
	}
}

func TestParse_DongMeoNhoKhongSinhPort(t *testing.T) {
	tbl := parseSample(t)

	// "Mẹo nhớ" chỉ chứa mẫu số (3xxx, 68xx…) — không được sinh port nào.
	for _, p := range []int{3, 68, 8, 909, 417, 517} {
		if tbl.Ports[p] {
			t.Errorf("dòng \"Mẹo nhớ\" sinh port %d → sai", p)
		}
	}
}

func TestParse_ChiLayTrongMuc101(t *testing.T) {
	tbl := parseSample(t)

	// Port nằm ở mục 10.2 (ngoài 10.1) không được lấy.
	if tbl.Ports[9999] {
		t.Error("port ở mục 10.2 không được lấy")
	}
}

func TestParse_KhongCoMuc101(t *testing.T) {
	_, err := Parse([]byte("# AGENTS.md\n\n## 1. Bản đồ workspace\n"))
	if err == nil {
		t.Fatal("phải báo lỗi khi không có mục 10.1")
	}
}

func TestParseFile_KhongTonTai(t *testing.T) {
	_, err := ParseFile(filepath.Join(t.TempDir(), "khong-co.md"))
	if err == nil {
		t.Fatal("phải báo lỗi khi không đọc được file")
	}
}

func TestParseFile_DocFileThat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(path, []byte(sampleSection), 0o644); err != nil {
		t.Fatal(err)
	}
	tbl, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile lỗi: %v", err)
	}
	if !tbl.Ports[3000] {
		t.Error("ParseFile phải đọc được port trong bảng")
	}

	// Hàm rút gọn trả về tập port.
	ports, err := PortsInFile(path)
	if err != nil {
		t.Fatalf("PortsInFile lỗi: %v", err)
	}
	if !ports[3000] || len(ports) != len(wantSamplePorts) {
		t.Errorf("PortsInFile trả %d port, muốn %d", len(ports), len(wantSamplePorts))
	}
}

// ---------------------------------------------------------------------------
// Fixture: đoạn AGENTS.md 10.1 thật (copy cố định trong testdata)
// ---------------------------------------------------------------------------

func parseFixture(t *testing.T) Table {
	t.Helper()
	tbl, err := ParseFile(filepath.Join("testdata", "agents-10.1.md"))
	if err != nil {
		t.Fatalf("đọc fixture lỗi: %v", err)
	}
	return tbl
}

func TestFixture_TapPortSoSanhChot(t *testing.T) {
	tbl := parseFixture(t)

	// Tập port thuộc phạm vi so sánh — chốt theo mục 10.1 thật.
	want := []int{
		// Backend Go — cột HTTP
		3000, 3001, 3004, 3005, 3010, 6868, 8090,
		// Frontend — cột Dev server (6790 nằm trong ngoặc, vẫn lấy)
		3003, 8000, 8091, 6869, 6789, 6790, 5180, 3011,
		// Hạ tầng — dòng văn xuôi, port trong backtick
		5432, 6379, 5672, 9000, 9001, 1025, 8025, 8080, 8088,
	}
	got := tbl.SortedPorts()
	if !equalInts(got, want) {
		t.Errorf("tập port so sánh = %v\nmuốn = %v", got, want)
	}
}

func TestFixture_KhongCoPortBiLo(t *testing.T) {
	tbl := parseFixture(t)

	// gRPC, preview, dải fe-* không được vào tập so sánh.
	banned := []int{9090, 9091, 9092, 9093, 4173, 4174, 4175, 4176, 5173, 5174, 5175, 5176, 5177, 5178, 5179, 15672}
	for _, p := range banned {
		if tbl.Ports[p] {
			t.Errorf("port %d không được vào tập so sánh", p)
		}
	}
}

func TestFixture_DaiFeMoc(t *testing.T) {
	tbl := parseFixture(t)

	if len(tbl.Ranges) != 1 {
		t.Fatalf("số dải = %d, muốn 1 (%+v)", len(tbl.Ranges), tbl.Ranges)
	}
	r := tbl.Ranges[0]
	if r.From != 5173 || r.To != 5179 || len(r.Ports) != 7 {
		t.Errorf("dải = %d-%d (%d port), muốn 5173-5179 (7 port)", r.From, r.To, len(r.Ports))
	}
}

// ---------------------------------------------------------------------------
// Compare
// ---------------------------------------------------------------------------

// yamlPortsFixture: tập port service khai trong ports.yaml (bản thật trong repo).
var yamlPortsFixture = []int{
	3000, 9090, 3001, 9092, 3004, 9091, 3005, 3010, 6868, 8090,
	3003, 8000, 8091, 6869, 6789, 5180, 3011,
	5432, 6379, 5672, 1025, 8025, 9000, 9001, 8080, 6381, 8088,
}

func TestCompare_LechHaiChieu(t *testing.T) {
	tbl := parseFixture(t)

	yaml := map[int]bool{}
	for _, p := range yamlPortsFixture {
		yaml[p] = true
	}
	rep := Compare(tbl, yaml)

	// Đúng 2 lệch: 6790 (chỉ ở AGENTS.md) và 6381 (chỉ ở ports.yaml).
	got := []int{}
	for _, m := range rep.Mismatches {
		got = append(got, m.Port)
	}
	want := []int{6381, 6790}
	if !equalInts(got, want) {
		t.Fatalf("danh sách lệch = %v, muốn %v (chi tiết: %+v)", got, want, rep.Mismatches)
	}

	// 6381: chỉ có trong ports.yaml.
	m := rep.Mismatches[0]
	if m.Port != 6381 || m.InAgents || !m.InPortsYaml {
		t.Errorf("mismatch 6381 sai: %+v", m)
	}
	if m.Note == "" {
		t.Error("mismatch 6381 phải có ghi chú")
	}

	// 6790: chỉ có trong AGENTS.md + ghi chú "cần kiểm".
	m = rep.Mismatches[1]
	if m.Port != 6790 || !m.InAgents || m.InPortsYaml {
		t.Errorf("mismatch 6790 sai: %+v", m)
	}
	if !contains(m.Note, "cần kiểm") {
		t.Errorf("ghi chú 6790 phải nói \"cần kiểm\": %q", m.Note)
	}
}

func TestCompare_LoaiPortGrpcKhoiHaiPhia(t *testing.T) {
	tbl := parseFixture(t)

	yaml := map[int]bool{}
	for _, p := range yamlPortsFixture {
		yaml[p] = true
	}
	rep := Compare(tbl, yaml)

	// gRPC (9090/9091/9092) khai ở ports.yaml nhưng không thuộc phạm vi so
	// sánh → không được báo lệch.
	for _, m := range rep.Mismatches {
		switch m.Port {
		case 9090, 9091, 9092, 9093:
			t.Errorf("port gRPC %d không được báo lệch", m.Port)
		}
	}
}

func TestCompare_KhongLlech(t *testing.T) {
	tbl := parseSample(t)

	yaml := map[int]bool{}
	for _, p := range wantSamplePorts {
		yaml[p] = true
	}
	rep := Compare(tbl, yaml)
	if len(rep.Mismatches) != 0 {
		t.Errorf("phải không có lệch, thực tế: %+v", rep.Mismatches)
	}
	if rep.Mismatches == nil {
		t.Error("mismatches phải là mảng rỗng, không phải null")
	}
	if rep.CheckedAt == "" {
		t.Error("checkedAt phải có giá trị")
	}
}

func TestCompare_GhiChuServiceKhiCoOwners(t *testing.T) {
	tbl := parseFixture(t)

	yaml := map[int]bool{}
	for _, p := range yamlPortsFixture {
		yaml[p] = true
	}
	owners := Owners{6381: {Service: "infra-fm-redis", Group: "Hạ tầng (docker)"}}
	rep := CompareWithOwners(tbl, yaml, owners)

	if len(rep.Mismatches) != 2 {
		t.Fatalf("số lệch = %d, muốn 2 (%+v)", len(rep.Mismatches), rep.Mismatches)
	}
	m := rep.Mismatches[0]
	if m.Port != 6381 {
		t.Fatalf("lệch đầu tiên phải là 6381: %+v", m)
	}
	if m.Service != "infra-fm-redis" || m.Group != "Hạ tầng (docker)" {
		t.Errorf("service/group sai: %+v", m)
	}
	if !contains(m.Note, "infra-fm-redis") {
		t.Errorf("ghi chú phải nói rõ service: %q", m.Note)
	}
}

// ---------------------------------------------------------------------------
// SuggestPort
// ---------------------------------------------------------------------------

func TestSuggestPort_GoiYPortTrongBangDangTrong(t *testing.T) {
	tbl := parseSample(t)

	// 3000 đang trống → gợi ý port dev server đầu tiên còn trống.
	used := map[int]bool{6789: true, 6790: true, 3000: true}
	got := SuggestPort(tbl, used)
	if got != 3003 {
		t.Errorf("SuggestPort = %d, muốn 3003", got)
	}
	if tbl.Ports[got] != true {
		t.Errorf("port gợi ý %d phải nằm trong bảng quy hoạch", got)
	}
}

func TestSuggestPort_KhongGoiYPortDaKhai(t *testing.T) {
	tbl := parseSample(t)

	// Mọi port dev server trong bảng đều đã khai → không gợi ý port đó.
	used := map[int]bool{}
	for _, p := range tbl.DevPorts {
		used[p] = true
	}
	got := SuggestPort(tbl, used)
	if got == 0 {
		return // hết ứng viên dev → rơi xuống hạ tầng cũng đã khai hết ở test dưới
	}
	if used[got] {
		t.Errorf("SuggestPort = %d nhưng port này đã khai rồi", got)
	}
}

func TestSuggestPort_KhongConCongTrong(t *testing.T) {
	tbl := parseSample(t)

	used := map[int]bool{}
	for _, p := range tbl.SortedPorts() {
		used[p] = true
	}
	if got := SuggestPort(tbl, used); got != 0 {
		t.Errorf("SuggestPort = %d, muốn 0 (không còn cổng trống)", got)
	}
}

// ---------------------------------------------------------------------------
// Đối chiếu với workspace thật (bỏ qua nếu máy không có workspace smatec/)
// ---------------------------------------------------------------------------

// TestThatDungVoiWorkspaceThat chạy trên máy dev có workspace smatec/ thật:
// đọc AGENTS.md 10.1 và so với ports.yaml trong repo. Kỳ vọng ngoại lệ đã biết
// (mục 3.5 của plan v0.6.0): 6381 khai ở ports.yaml (infra-fm-redis) nhưng
// CHƯA có trong AGENTS.md → đây là lệch thật, phải được báo.
func TestThatDungVoiWorkspaceThat(t *testing.T) {
	agentsPath := filepath.Join("..", "..", "..", "AGENTS.md")
	if _, err := os.Stat(agentsPath); err != nil {
		t.Skip("không có workspace smatec/ thật — bỏ qua test tích hợp")
	}
	tbl, err := ParseFile(agentsPath)
	if err != nil {
		t.Fatalf("đọc AGENTS.md thật lỗi: %v", err)
	}
	cfg, _, err := config.Load(filepath.Join("..", "..", "ports.yaml"))
	if err != nil {
		t.Fatalf("đọc ports.yaml lỗi: %v", err)
	}
	rep := Compare(tbl, cfg.ConfiguredPorts())

	found6381 := false
	for _, m := range rep.Mismatches {
		if m.Port == 6381 {
			found6381 = true
			if m.InAgents {
				t.Error("6381 phải KHÔNG có trong AGENTS.md mục 10.1")
			}
			if !m.InPortsYaml {
				t.Error("6381 phải có trong ports.yaml")
			}
		}
	}
	if !found6381 {
		t.Errorf("phải báo lệch 6381 (chỉ có trong ports.yaml); các lệch: %+v", rep.Mismatches)
	}
	// Không được báo lệch cho port gRPC (9090/9091/9092) dù ports.yaml có khai.
	for _, m := range rep.Mismatches {
		switch m.Port {
		case 9090, 9091, 9092, 9093:
			t.Errorf("port gRPC %d không được báo lệch", m.Port)
		}
	}
}

// ---------------------------------------------------------------------------
// Hình dạng JSON — khoá hợp đồng
// ---------------------------------------------------------------------------
func TestMirrorReport_JSONShape(t *testing.T) {
	rep := MirrorReport{
		Mismatches: []Mismatch{{Port: 6381, InAgents: false, InPortsYaml: true, Note: "ghi chú"}},
		CheckedAt:  "2026-10-22T10:00:00+07:00",
	}
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"mismatches":[{"port":6381,"inAgents":false,"inPortsYaml":true,"note":"ghi chú"}],"checkedAt":"2026-10-22T10:00:00+07:00"}`
	if string(b) != want {
		t.Errorf("JSON sai:\n got = %s\nwant = %s", b, want)
	}
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]int(nil), a...)
	y := append([]int(nil), b...)
	sort.Ints(x)
	sort.Ints(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
