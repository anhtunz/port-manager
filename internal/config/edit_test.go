package config

// Test cho phần ghi ports.yaml bằng sửa text theo dòng.
//
// Nguyên tắc kiểm tra ở mọi test: sau khi sửa, đọc lại file và so sánh:
//   - phần comment đầu file phải y nguyên;
//   - mọi dòng không liên quan phải y nguyên (byte-for-byte);
//   - file sau khi sửa phải parse được (config.Parse không lỗi).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sampleYAML là file ports.yaml mẫu — copy cấu trúc thật (comment đầu file,
// flow style `ports: [...]`, comment đuôi dòng, key repo_dir).
const sampleYAML = `# ============================================================================
# ports.yaml — SMATEC Port Manager
#
# ⚠️  NGUỒN SỰ THẬT về port là bảng quy hoạch ở **AGENTS.md mục 10** (root
#     workspace smatec/). File này chỉ là MIRROR.
# ============================================================================

workspace_root: ".."

unknown_ignore_commands:
  - rapportd          # macOS Continuity
  - cloudflared

unknown_ignore_ports:
  - 6790              # utoopack nội bộ cạnh WM FE 6789
  - 6791              # utoopack nội bộ cạnh WM FE 6789

groups:
  - name: Backend Go
    services:
      - id: auth-be
        name: auth-service BE (Go · Gin · gRPC)
        ports: [3000, 9090]
        dir: auth-service/backend
        start: make run
      - id: wm-be
        name: warehouse-management BE
        ports: [3001, 9092]
        dir: warehouse-management
        repo_dir: warehouse-management/backend   # dir gốc chỉ chứa Makefile tiện ích — repo là backend/
        start: make run-be

  - name: Frontend
    services:
      - id: auth-fe
        name: auth-frontend (React admin)
        ports: [3003]
        dir: auth-service/auth-frontend
        start: npm run dev

  - name: Hạ tầng (docker)
    services:
      - id: infra-postgres
        name: PostgreSQL (docker-shared)
        ports: [5432]
        compose_dir: docker-shared
        compose_file: docker-compose.yml
        compose_service: postgres
`

// writeSample tạo file ports.yaml trong thư mục tạm và trả về đường dẫn.
func writeSample(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ports.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("ghi file mẫu: %v", err)
	}
	return path
}

// readFile đọc lại file sau khi sửa.
func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("đọc lại file: %v", err)
	}
	return string(data)
}

// headerOf trả phần comment đầu file (mọi dòng bắt đầu bằng '#' ở đầu file
// cộng dòng trống ngay sau) — phần này phải KHÔNG đổi sau mọi thao tác.
func headerOf(content string) string {
	lines := strings.Split(content, "\n")
	var out []string
	for _, ln := range lines {
		if strings.HasPrefix(ln, "#") || strings.TrimSpace(ln) == "" {
			out = append(out, ln)
			continue
		}
		break
	}
	return strings.Join(out, "\n")
}

// linesOf trả file thành list dòng (bỏ dòng rỗng cuối do newline cuối).
func linesOf(content string) []string {
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// assertUnchangedLines kiểm tra mọi dòng của `before` (trừ những dòng được
// liệt kê là được phép đổi) vẫn còn nguyên trong `after` — theo đúng thứ tự
// và cùng nội dung.
//
// allowedChanged liệt kê CẢ dòng cũ lẫn dòng mới của phần được đổi (vd khi
// sửa `ports: [3000, 9090]` → `ports: [3000, 9091]` thì phải khai cả hai).
func assertUnchangedLines(t *testing.T, before, after string, allowedChanged ...string) {
	t.Helper()
	beforeLines := linesOf(before)
	afterLines := linesOf(after)

	// Đếm số lần xuất hiện của mỗi dòng ở before và after.
	beforeCount := map[string]int{}
	for _, ln := range beforeLines {
		beforeCount[ln]++
	}
	afterCount := map[string]int{}
	for _, ln := range afterLines {
		afterCount[ln]++
	}

	// Dòng được phép đổi: được bỏ qua khi so sánh (cả bản cũ lẫn bản mới).
	allowed := map[string]bool{}
	for _, ln := range allowedChanged {
		allowed[ln] = true
	}

	// Dòng mất đi (có ở before, không ở after) → phải nằm trong allowed.
	for ln, n := range beforeCount {
		if afterCount[ln] >= n {
			continue
		}
		missing := n - afterCount[ln]
		if allowed[ln] {
			continue
		}
		t.Errorf("dòng bị mất hoặc đổi: %q", ln)
		_ = missing
	}
	// Dòng mới xuất hiện (không ở before, có ở after) → phải nằm trong allowed.
	for ln, n := range afterCount {
		if beforeCount[ln] >= n {
			continue
		}
		if allowed[ln] {
			continue
		}
		t.Errorf("dòng mới xuất hiện: %q (x%d)", ln, n)
	}
}

// ---------------------------------------------------------------------------
// AddIgnorePort (F1.4 — ẩn cổng lạ)
// ---------------------------------------------------------------------------

func TestAddIgnorePort_ThemDongVaGiuNguyenComment(t *testing.T) {
	path := writeSample(t, sampleYAML)
	before := readFile(t, path)

	if err := AddIgnorePort(path, 6793); err != nil {
		t.Fatalf("AddIgnorePort: %v", err)
	}
	after := readFile(t, path)

	// Đã có key unknown_ignore_ports → chỉ thêm đúng 1 dòng.
	beforeLines := linesOf(before)
	afterLines := linesOf(after)
	if len(afterLines) != len(beforeLines)+1 {
		t.Fatalf("thêm %d dòng, cần đúng 1", len(afterLines)-len(beforeLines))
	}
	if !strings.Contains(after, "  - 6793\n") {
		t.Errorf("thiếu dòng port mới:\n%s", after)
	}
	// Port mới phải nằm cuối danh sách unknown_ignore_ports (trước `groups:`).
	idxNew := strings.Index(after, "  - 6793\n")
	idxGroups := strings.Index(after, "groups:\n")
	if idxNew < 0 || idxGroups < 0 || idxNew > idxGroups {
		t.Errorf("port mới không nằm trong danh sách unknown_ignore_ports")
	}
	// Comment đầu file giữ nguyên.
	if headerOf(before) != headerOf(after) {
		t.Errorf("comment đầu file đã đổi:\n%s", headerOf(after))
	}
	// Mọi dòng khác giữ nguyên.
	assertUnchangedLines(t, before, after, "  - 6793")

	// File vẫn parse được.
	if _, err := Parse([]byte(after)); err != nil {
		t.Fatalf("file sau khi sửa không parse được: %v", err)
	}
}

func TestAddIgnorePort_Idempotent(t *testing.T) {
	path := writeSample(t, sampleYAML)
	if err := AddIgnorePort(path, 6790); err != nil { // 6790 đã có sẵn
		t.Fatalf("AddIgnorePort: %v", err)
	}
	after := readFile(t, path)
	if strings.Count(after, "- 6790") != 1 {
		t.Errorf("port đã có bị thêm lại:\n%s", after)
	}
}

func TestAddIgnorePort_FileChuaCoKey_TaoKeyTopLevel(t *testing.T) {
	// File không có unknown_ignore_ports → phải tạo key top-level (indent 0)
	// + item indent 2, chèn sau khối unknown_ignore_commands.
	noPorts := strings.Replace(sampleYAML, `unknown_ignore_ports:
  - 6790              # utoopack nội bộ cạnh WM FE 6789
  - 6791              # utoopack nội bộ cạnh WM FE 6789

`, "", 1)
	path := writeSample(t, noPorts)

	if err := AddIgnorePort(path, 6793); err != nil {
		t.Fatalf("AddIgnorePort: %v", err)
	}
	after := readFile(t, path)

	if !strings.Contains(after, "\nunknown_ignore_ports:\n  - 6793\n") {
		t.Errorf("chưa tạo đúng key top-level + item indent 2:\n%s", after)
	}
	// Key phải nằm sau unknown_ignore_commands, trước groups.
	if strings.Index(after, "unknown_ignore_ports:") < strings.Index(after, "unknown_ignore_commands:") {
		t.Errorf("key chèn sai vị trí (phải sau unknown_ignore_commands)")
	}
	if strings.Index(after, "unknown_ignore_ports:") > strings.Index(after, "groups:") {
		t.Errorf("key chèn sai vị trí (phải trước groups)")
	}
	if _, err := Parse([]byte(after)); err != nil {
		t.Fatalf("file sau khi sửa không parse được: %v", err)
	}
}

func TestAddIgnorePort_KeyCoSanNhungRong(t *testing.T) {
	// File có key unknown_ignore_ports nhưng rỗng → phải thêm item vào key
	// đó, KHÔNG tạo key trùng (yaml sẽ lỗi duplicate key).
	empty := strings.Replace(sampleYAML, `unknown_ignore_ports:
  - 6790              # utoopack nội bộ cạnh WM FE 6789
  - 6791              # utoopack nội bộ cạnh WM FE 6789
`, "unknown_ignore_ports:\n", 1)
	path := writeSample(t, empty)

	if err := AddIgnorePort(path, 6793); err != nil {
		t.Fatalf("AddIgnorePort: %v", err)
	}
	after := readFile(t, path)

	if strings.Count(after, "unknown_ignore_ports:") != 1 {
		t.Errorf("phải có đúng 1 key unknown_ignore_ports:\n%s", after)
	}
	if !strings.Contains(after, "unknown_ignore_ports:\n  - 6793\n") {
		t.Errorf("chưa thêm item vào key sẵn có:\n%s", after)
	}
	if _, err := Parse([]byte(after)); err != nil {
		t.Fatalf("file sau khi sửa không parse được: %v", err)
	}
}

// ---------------------------------------------------------------------------
// UpsertService (F3.4 — thêm/sửa service)
// ---------------------------------------------------------------------------

func TestUpsertService_ThemMoi_GiuIndentVaComment(t *testing.T) {
	path := writeSample(t, sampleYAML)
	before := readFile(t, path)

	spec := ServiceSpec{
		ID:    "notification-be",
		Name:  "notification-service BE",
		Group: "Backend Go",
		Ports: []int{3004, 9091},
		Dir:   "notification-service/backend",
		Start: "make dev",
	}
	if err := UpsertService(path, spec); err != nil {
		t.Fatalf("UpsertService: %v", err)
	}
	after := readFile(t, path)

	// Indent đúng: `- id:` 6 space, key 8 space (khớp ports.yaml).
	want := []string{
		"      - id: notification-be",
		"        name: notification-service BE",
		"        ports: [3004, 9091]",
		"        dir: notification-service/backend",
		"        start: make dev",
	}
	for _, w := range want {
		if !strings.Contains(after, w+"\n") {
			t.Errorf("thiếu dòng %q trong file sau khi sửa:\n%s", w, after)
		}
	}
	// Chèn CUỐI danh sách services của group Backend Go (trước group Frontend).
	if strings.Index(after, "- id: notification-be") > strings.Index(after, "- name: Frontend") {
		t.Errorf("service mới phải nằm trong group Backend Go, trước group Frontend")
	}
	// Comment đầu file giữ nguyên.
	if headerOf(before) != headerOf(after) {
		t.Errorf("comment đầu file đã đổi")
	}
	// Mọi dòng cũ giữ nguyên.
	assertUnchangedLines(t, before, after, want...)

	if _, err := Parse([]byte(after)); err != nil {
		t.Fatalf("file sau khi sửa không parse được: %v", err)
	}
	// Config đọc lại thấy service mới đúng group.
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	svc, ok := cfg.Service("notification-be")
	if !ok {
		t.Fatalf("không thấy service vừa thêm")
	}
	if svc.Name != spec.Name || len(svc.Ports) != 2 || svc.Ports[0] != 3004 {
		t.Errorf("service đọc lại sai: %+v", svc)
	}
	var found bool
	for _, g := range cfg.Groups {
		if g.Name != "Backend Go" {
			continue
		}
		for _, s := range g.Services {
			if s.ID == "notification-be" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("service mới không nằm trong group Backend Go")
	}
}

func TestUpsertService_SuaServiceCu_GiuFlowStyleVaComment(t *testing.T) {
	path := writeSample(t, sampleYAML)
	before := readFile(t, path)

	spec := ServiceSpec{
		ID:    "auth-be",
		Name:  "auth-service BE đã sửa",
		Group: "Backend Go",
		Ports: []int{3000, 9091},
		Dir:   "auth-service/backend",
		Start: "make run",
	}
	if err := UpsertService(path, spec); err != nil {
		t.Fatalf("UpsertService: %v", err)
	}
	after := readFile(t, path)

	// ports giữ flow style.
	if !strings.Contains(after, "        ports: [3000, 9091]\n") {
		t.Errorf("ports không giữ flow style:\n%s", after)
	}
	// name đã đổi.
	if !strings.Contains(after, "        name: auth-service BE đã sửa\n") {
		t.Errorf("name chưa được sửa:\n%s", after)
	}
	// Comment đuôi dòng của service KHÁC (wm-be) giữ nguyên.
	if !strings.Contains(after, "        repo_dir: warehouse-management/backend   # dir gốc chỉ chứa Makefile tiện ích — repo là backend/\n") {
		t.Errorf("comment đuôi dòng của service khác bị mất:\n%s", after)
	}
	// Dòng dir/start không đổi → không bị đụng.
	if !strings.Contains(after, "        dir: auth-service/backend\n") {
		t.Errorf("dòng dir của service bị mất:\n%s", after)
	}
	if headerOf(before) != headerOf(after) {
		t.Errorf("comment đầu file đã đổi")
	}
	// Chỉ 2 dòng đổi (name + ports của auth-be).
	assertUnchangedLines(t, before, after,
		"        name: auth-service BE (Go · Gin · gRPC)",
		"        ports: [3000, 9090]",
		"        name: auth-service BE đã sửa",
		"        ports: [3000, 9091]",
	)

	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	svc, _ := cfg.Service("auth-be")
	if svc.Name != spec.Name || svc.Ports[1] != 9091 {
		t.Errorf("service đọc lại sai: %+v", svc)
	}
}

func TestUpsertService_SuaServiceKhongLamDongServiceKhac(t *testing.T) {
	path := writeSample(t, sampleYAML)
	before := readFile(t, path)

	spec := ServiceSpec{
		ID:    "auth-fe",
		Name:  "auth-frontend đổi tên",
		Group: "Frontend",
		Ports: []int{3003},
		Dir:   "auth-service/auth-frontend",
		Start: "npm run dev",
	}
	if err := UpsertService(path, spec); err != nil {
		t.Fatalf("UpsertService: %v", err)
	}
	after := readFile(t, path)

	// Khối Backend Go phải y nguyên.
	for _, ln := range []string{
		"      - id: auth-be",
		"        ports: [3000, 9090]",
		"      - id: wm-be",
		"        ports: [3001, 9092]",
		"        repo_dir: warehouse-management/backend   # dir gốc chỉ chứa Makefile tiện ích — repo là backend/",
	} {
		if !strings.Contains(after, ln+"\n") {
			t.Errorf("dòng của service khác bị đụng: %q\n%s", ln, after)
		}
	}
	if headerOf(before) != headerOf(after) {
		t.Errorf("comment đầu file đã đổi")
	}
}

func TestUpsertService_PortTrung_TranLoiVaKhongGhiFile(t *testing.T) {
	path := writeSample(t, sampleYAML)
	before := readFile(t, path)

	spec := ServiceSpec{
		ID:    "new-be",
		Name:  "Trùng port",
		Group: "Backend Go",
		Ports: []int{3000}, // đã dùng bởi auth-be
		Dir:   "x",
		Start: "make run",
	}
	err := UpsertService(path, spec)
	if err == nil {
		t.Fatalf("phải báo lỗi port trùng")
	}
	if !strings.Contains(err.Error(), "trùng") {
		t.Errorf("lỗi phải nói rõ port trùng, nhận: %v", err)
	}
	// File KHÔNG bị đổi.
	if after := readFile(t, path); after != before {
		t.Errorf("file đã bị ghi dù lỗi:\n%s", after)
	}
}

func TestUpsertService_ThieuIdHoacPorts(t *testing.T) {
	path := writeSample(t, sampleYAML)
	before := readFile(t, path)

	if err := UpsertService(path, ServiceSpec{Name: "x", Group: "Backend Go", Ports: []int{4000}}); err == nil {
		t.Errorf("phải báo lỗi thiếu id")
	}
	if err := UpsertService(path, ServiceSpec{ID: "x", Name: "x", Group: "Backend Go"}); err == nil {
		t.Errorf("phải báo lỗi thiếu ports")
	}
	if after := readFile(t, path); after != before {
		t.Errorf("file đã bị ghi dù lỗi validate")
	}
}

func TestUpsertService_ChuyenGroup(t *testing.T) {
	path := writeSample(t, sampleYAML)
	before := readFile(t, path)

	spec := ServiceSpec{
		ID:    "auth-be",
		Name:  "auth-service BE",
		Group: "Frontend",
		Ports: []int{3000, 9090},
		Dir:   "auth-service/backend",
		Start: "make run",
	}
	if err := UpsertService(path, spec); err != nil {
		t.Fatalf("UpsertService: %v", err)
	}
	after := readFile(t, path)

	// Service phải nằm trong group Frontend, và chỉ xuất hiện MỘT lần.
	if strings.Count(after, "- id: auth-be") != 1 {
		t.Errorf("service bị trùng (chuyển group phải xoá khỏi group cũ):\n%s", after)
	}
	idxSvc := strings.Index(after, "- id: auth-be")
	idxFrontend := strings.Index(after, "- name: Frontend")
	idxInfra := strings.Index(after, "- name: Hạ tầng")
	if idxSvc < idxFrontend || idxSvc > idxInfra {
		t.Errorf("service chưa nằm trong group Frontend")
	}
	// Group cũ (Backend Go) vẫn còn service khác.
	if !strings.Contains(after, "- id: wm-be") {
		t.Errorf("mất service khác của group cũ:\n%s", after)
	}
	// Comment đầu file giữ nguyên.
	if headerOf(before) != headerOf(after) {
		t.Errorf("comment đầu file đã đổi")
	}
	if _, err := Parse([]byte(after)); err != nil {
		t.Fatalf("file sau khi sửa không parse được: %v", err)
	}
}

// TestUpsertService_ChuyenGroup_GiuKeyNgoaiHopDong: wm-be có repo_dir +
// health_path + health_skip_ports (key bất kỳ do user sửa tay). Chuyển sang
// group khác KHÔNG được xoá các key đó kèm comment đuôi dòng (từng là lỗi
// thật — mất dữ liệu cấu hình, health gRPC bị probe HTTP).
func TestUpsertService_ChuyenGroup_GiuKeyNgoaiHopDong(t *testing.T) {
	sample := strings.Replace(sampleYAML,
		"        repo_dir: warehouse-management/backend   # dir gốc chỉ chứa Makefile tiện ích — repo là backend/",
		"        repo_dir: warehouse-management/backend   # dir gốc chỉ chứa Makefile tiện ích — repo là backend/\n"+
			"        health_path: /healthz\n"+
			"        health_skip_ports: [9092]", 1)
	path := writeSample(t, sample)

	spec := ServiceSpec{
		ID:              "wm-be",
		Name:            "warehouse-management BE",
		Group:           "Frontend",
		Ports:           []int{3001, 9092},
		Dir:             "warehouse-management",
		Start:           "make run-be",
		RepoDir:         "warehouse-management/backend",
		HealthPath:      "/healthz",
		HealthSkipPorts: []int{9092},
	}
	if err := UpsertService(path, spec); err != nil {
		t.Fatalf("UpsertService: %v", err)
	}
	after := readFile(t, path)

	if strings.Count(after, "- id: wm-be") != 1 {
		t.Fatalf("service bị trùng sau khi chuyển group:\n%s", after)
	}
	idxSvc := strings.Index(after, "- id: wm-be")
	idxFrontend := strings.Index(after, "- name: Frontend")
	idxInfra := strings.Index(after, "- name: Hạ tầng")
	if idxSvc < idxFrontend || idxSvc > idxInfra {
		t.Fatalf("service chưa nằm trong group Frontend:\n%s", after)
	}
	for _, want := range []string{
		"repo_dir: warehouse-management/backend   # dir gốc chỉ chứa Makefile tiện ích — repo là backend/",
		"health_path: /healthz",
		"health_skip_ports: [9092]",
	} {
		if !strings.Contains(after, want) {
			t.Errorf("chuyển group làm mất/khác %q:\n%s", want, after)
		}
	}
	// Giá trị phải nằm TRONG khối service wm-be ở group Frontend: sau
	// "- id: wm-be", trước group kế ("- name: Hạ tầng") — theo tính toán của
	// newServicePos, service chuyển group được chèn CUỐI danh sách services
	// của group đích (giống service thêm mới).
	for _, want := range []string{"repo_dir: warehouse-management/backend", "health_path: /healthz", "health_skip_ports: [9092]"} {
		i := strings.Index(after, want)
		if i < idxSvc || i > idxInfra {
			t.Errorf("%q nằm sai vị trí (không trong khối service wm-be)", want)
		}
	}
	if _, err := Parse([]byte(after)); err != nil {
		t.Fatalf("file sau khi sửa không parse được: %v", err)
	}
}

// TestUpsertService_ChuyenGroup_GiuKeyHoanToanLa (không có trong ServiceSpec):
// key lạ ngoài managed set (vd "notes:") phải được giữ nguyên dòng gốc kèm
// comment + indent theo vị trí mới.
func TestUpsertService_ChuyenGroup_GiuKeyHoanToanLa(t *testing.T) {
	sample := strings.Replace(sampleYAML,
		"        dir: warehouse-management\n",
		"        dir: warehouse-management\n        notes: do-them-tay  # ghi chú tùy ý\n", 1)
	path := writeSample(t, sample)

	spec := ServiceSpec{
		ID:    "wm-be",
		Name:  "warehouse-management BE",
		Group: "Frontend",
		Ports: []int{3001, 9092},
		Dir:   "warehouse-management",
		Start: "make run-be",
	}
	if err := UpsertService(path, spec); err != nil {
		t.Fatalf("UpsertService: %v", err)
	}
	after := readFile(t, path)
	if !strings.Contains(after, "notes: do-them-tay  # ghi chú tùy ý") {
		t.Errorf("key lạ bị xoá khi chuyển group:\n%s", after)
	}
	// Indent của key lạ phải về đúng indent của key trong group mới (8Space
	// — khối service có "- id" indent 6 với key con indent 8).
	found := false
	for _, ln := range strings.Split(after, "\n") {
		if strings.Contains(ln, "notes: do-them-tay") {
			if !strings.HasPrefix(ln, "        notes:") {
				t.Errorf("indent của key lạ sai:\n%q", ln)
			}
			found = true
		}
	}
	if !found {
		t.Errorf("không tìm thấy key lạ trong file sau khi chuyển group")
	}
	if _, err := Parse([]byte(after)); err != nil {
		t.Fatalf("file sau khi sửa không parse được: %v", err)
	}
}

func TestUpsertService_SuaServiceCompose_GiuComposeFields(t *testing.T) {
	path := writeSample(t, sampleYAML)
	before := readFile(t, path)

	spec := ServiceSpec{
		ID:             "infra-postgres",
		Name:           "PostgreSQL (docker-shared)",
		Group:          "Hạ tầng (docker)",
		Ports:          []int{5432},
		ComposeDir:     "docker-shared",
		ComposeFile:    "docker-compose.yml",
		ComposeService: "postgres",
	}
	if err := UpsertService(path, spec); err != nil {
		t.Fatalf("UpsertService: %v", err)
	}
	after := readFile(t, path)

	// Compose fields giữ nguyên, không sinh dir/start.
	for _, w := range []string{
		"        compose_dir: docker-shared",
		"        compose_file: docker-compose.yml",
		"        compose_service: postgres",
	} {
		if !strings.Contains(after, w+"\n") {
			t.Errorf("thiếu %q:\n%s", w, after)
		}
	}
	// Khối service compose không được có dir/start.
	idx := strings.Index(after, "- id: infra-postgres")
	if idx < 0 {
		t.Fatalf("không thấy service compose")
	}
	block := after[idx:]
	if end := strings.Index(block, "- id: "); end > 0 {
		block = block[:end]
	}
	// Khối service compose không được có dir/start (kiểm tra theo đầu dòng
	// để không khớp nhầm với compose_dir).
	for _, forbidden := range []string{"\n        dir:", "\n        start:"} {
		if strings.Contains(block, forbidden) {
			t.Errorf("service compose không được có %q:\n%s", forbidden, block)
		}
	}
	if headerOf(before) != headerOf(after) {
		t.Errorf("comment đầu file đã đổi")
	}
	// Không có dòng nào bị đổi (spec giống hệt file).
	assertUnchangedLines(t, before, after)

	if _, err := Parse([]byte(after)); err != nil {
		t.Fatalf("file sau khi sửa không parse được: %v", err)
	}
}

func TestUpsertService_ThemServiceVaoGroupMoi(t *testing.T) {
	path := writeSample(t, sampleYAML)
	before := readFile(t, path)

	spec := ServiceSpec{
		ID:    "moi-be",
		Name:  "service mới",
		Group: "Nhóm mới",
		Ports: []int{3999},
		Dir:   "moi/dir",
		Start: "make dev",
	}
	if err := UpsertService(path, spec); err != nil {
		t.Fatalf("UpsertService: %v", err)
	}
	after := readFile(t, path)

	if !strings.Contains(after, "  - name: Nhóm mới\n") {
		t.Errorf("chưa tạo group mới:\n%s", after)
	}
	if !strings.Contains(after, "      - id: moi-be\n") {
		t.Errorf("chưa thêm service vào group mới:\n%s", after)
	}
	// Group mới phải ở CUỐI danh sách group.
	if strings.Index(after, "- name: Nhóm mới") < strings.Index(after, "- name: Hạ tầng") {
		t.Errorf("group mới phải ở cuối danh sách group")
	}
	// Comment đầu file giữ nguyên.
	if headerOf(before) != headerOf(after) {
		t.Errorf("comment đầu file đã đổi")
	}
	if _, err := Parse([]byte(after)); err != nil {
		t.Fatalf("file sau khi sửa không parse được: %v", err)
	}
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cfg.Service("moi-be"); !ok {
		t.Errorf("service mới không đọc được")
	}
}

// ---------------------------------------------------------------------------
// SetAutoRestart (F3.1 — cờ tự khởi động lại)
// ---------------------------------------------------------------------------

func TestSetAutoRestart_BatCoSan_ChiDoiMotDong(t *testing.T) {
	content := strings.Replace(sampleYAML,
		"        start: make run\n",
		"        start: make run\n        auto_restart: false   # bật lại khi crash\n", 1)
	path := writeSample(t, content)
	before := readFile(t, path)

	if err := SetAutoRestart(path, "auth-be", true); err != nil {
		t.Fatalf("SetAutoRestart: %v", err)
	}
	after := readFile(t, path)

	if !strings.Contains(after, "        auto_restart: true   # bật lại khi crash\n") {
		t.Errorf("chưa đổi đúng dòng cờ (giữ comment đuôi dòng):\n%s", after)
	}
	// Chỉ 1 dòng đổi.
	assertUnchangedLines(t, before, after,
		"        auto_restart: false   # bật lại khi crash",
		"        auto_restart: true   # bật lại khi crash",
	)

	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	svc, _ := cfg.Service("auth-be")
	if !svc.AutoRestart {
		t.Errorf("auto_restart đọc lại phải là true")
	}
}

func TestSetAutoRestart_ChuaCoCo_ThemDong(t *testing.T) {
	path := writeSample(t, sampleYAML)
	before := readFile(t, path)

	if err := SetAutoRestart(path, "auth-be", true); err != nil {
		t.Fatalf("SetAutoRestart: %v", err)
	}
	after := readFile(t, path)

	if !strings.Contains(after, "        auto_restart: true\n") {
		t.Errorf("chưa thêm dòng cờ:\n%s", after)
	}
	// Dòng cờ phải nằm cuối khối service auth-be (trước service kế tiếp).
	if strings.Index(after, "auto_restart: true") > strings.Index(after, "- id: wm-be") {
		t.Errorf("dòng cờ phải nằm trong khối service auth-be")
	}
	assertUnchangedLines(t, before, after, "        auto_restart: true")

	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	svc, _ := cfg.Service("auth-be")
	if !svc.AutoRestart {
		t.Errorf("auto_restart đọc lại phải là true")
	}
}

func TestSetAutoRestart_Tat_ChiDoiMotDong(t *testing.T) {
	content := strings.Replace(sampleYAML,
		"        start: make run\n",
		"        start: make run\n        auto_restart: true\n", 1)
	path := writeSample(t, content)

	if err := SetAutoRestart(path, "auth-be", false); err != nil {
		t.Fatalf("SetAutoRestart: %v", err)
	}
	after := readFile(t, path)
	if !strings.Contains(after, "        auto_restart: false\n") {
		t.Errorf("chưa tắt được cờ:\n%s", after)
	}
}

func TestSetAutoRestart_ServiceKhongTonTai(t *testing.T) {
	path := writeSample(t, sampleYAML)
	before := readFile(t, path)

	err := SetAutoRestart(path, "khong-co", true)
	if err == nil {
		t.Fatalf("phải báo lỗi service không tồn tại")
	}
	if !strings.Contains(err.Error(), "không tìm thấy service") {
		t.Errorf("lỗi phải nói rõ không tìm thấy service, nhận: %v", err)
	}
	if after := readFile(t, path); after != before {
		t.Errorf("file đã bị ghi dù lỗi")
	}
}

// ---------------------------------------------------------------------------
// DeleteService
// ---------------------------------------------------------------------------

func TestDeleteService_XoaService_GiuPhanKhac(t *testing.T) {
	path := writeSample(t, sampleYAML)
	before := readFile(t, path)

	if err := DeleteService(path, "wm-be"); err != nil {
		t.Fatalf("DeleteService: %v", err)
	}
	after := readFile(t, path)

	if strings.Contains(after, "- id: wm-be") {
		t.Errorf("service chưa bị xoá:\n%s", after)
	}
	if !strings.Contains(after, "- id: auth-be") || !strings.Contains(after, "- id: auth-fe") {
		t.Errorf("xoá nhầm service khác:\n%s", after)
	}
	// Comment đầu file giữ nguyên.
	if headerOf(before) != headerOf(after) {
		t.Errorf("comment đầu file đã đổi")
	}
	if _, err := Parse([]byte(after)); err != nil {
		t.Fatalf("file sau khi sửa không parse được: %v", err)
	}
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cfg.Service("wm-be"); ok {
		t.Errorf("service đã xoá vẫn còn trong config")
	}
}

func TestDeleteService_ServiceKhongTonTai(t *testing.T) {
	path := writeSample(t, sampleYAML)
	before := readFile(t, path)

	if err := DeleteService(path, "khong-co"); err == nil {
		t.Fatalf("phải báo lỗi service không tồn tại")
	}
	if after := readFile(t, path); after != before {
		t.Errorf("file đã bị ghi dù lỗi")
	}
}

func TestXoaServiceCuoiTrongGroup_XoaLuonGroup(t *testing.T) {
	// Xoá service cuối của group → group phải bị xoá luôn (không để lại
	// group rỗng làm file rối).
	path := writeSample(t, sampleYAML)
	if err := DeleteService(path, "auth-fe"); err != nil {
		t.Fatalf("DeleteService: %v", err)
	}
	after := readFile(t, path)

	if strings.Contains(after, "- id: auth-fe") {
		t.Errorf("service chưa bị xoá:\n%s", after)
	}
	if strings.Contains(after, "- name: Frontend") {
		t.Errorf("group rỗng chưa bị xoá:\n%s", after)
	}
	if !strings.Contains(after, "- name: Backend Go") || !strings.Contains(after, "- name: Hạ tầng (docker)") {
		t.Errorf("xoá nhầm group khác:\n%s", after)
	}
	if _, err := Parse([]byte(after)); err != nil {
		t.Fatalf("file sau khi sửa không parse được: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Ghi file: atomic + lỗi
// ---------------------------------------------------------------------------

func TestGhiFile_AtomicKhongConFileTam(t *testing.T) {
	path := writeSample(t, sampleYAML)
	if err := SetAutoRestart(path, "auth-be", true); err != nil {
		t.Fatalf("SetAutoRestart: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("đọc thư mục: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "ports.yaml" {
			t.Errorf("còn sót file sau khi ghi: %q", e.Name())
		}
	}
}

func TestGhiFile_FileKhongTonTai(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "khong-co.yaml")

	if err := AddIgnorePort(missing, 6793); err == nil {
		t.Errorf("AddIgnorePort phải báo lỗi khi file không tồn tại")
	}
	if err := UpsertService(missing, ServiceSpec{ID: "a", Name: "A", Group: "G", Ports: []int{1}, Dir: "d", Start: "s"}); err == nil {
		t.Errorf("UpsertService phải báo lỗi khi file không tồn tại")
	}
	if err := SetAutoRestart(missing, "a", true); err == nil {
		t.Errorf("SetAutoRestart phải báo lỗi khi file không tồn tại")
	}
	if err := DeleteService(missing, "a"); err == nil {
		t.Errorf("DeleteService phải báo lỗi khi file không tồn tại")
	}
	// Không được tự tạo file mới.
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Errorf("không được tự tạo file config mới")
	}
}

func TestGhiFile_PathRong(t *testing.T) {
	if err := AddIgnorePort("", 6793); err == nil {
		t.Errorf("path rỗng phải báo lỗi")
	}
}

// ---------------------------------------------------------------------------
// Đọc lại config sau khi sửa (nền cho ReloadConfig)
// ---------------------------------------------------------------------------

func TestSauKhiSua_LoadLaiConfigDung(t *testing.T) {
	path := writeSample(t, sampleYAML)

	if err := UpsertService(path, ServiceSpec{
		ID: "auth-be", Name: "auth-service BE", Group: "Backend Go",
		Ports: []int{3000, 9090}, Dir: "auth-service/backend", Start: "make run",
		AutoRestart: true,
	}); err != nil {
		t.Fatalf("UpsertService: %v", err)
	}
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	svc, ok := cfg.Service("auth-be")
	if !ok {
		t.Fatalf("không thấy service")
	}
	if !svc.AutoRestart {
		t.Errorf("auto_restart phải là true")
	}
	if svc.Dir != "auth-service/backend" || svc.Start != "make run" {
		t.Errorf("service đọc lại sai: %+v", svc)
	}
	// Thứ tự group + service giữ nguyên.
	wantGroups := []string{"Backend Go", "Frontend", "Hạ tầng (docker)"}
	if len(cfg.Groups) != len(wantGroups) {
		t.Fatalf("số group đọc lại = %d, cần %d", len(cfg.Groups), len(wantGroups))
	}
	for i, g := range cfg.Groups {
		if g.Name != wantGroups[i] {
			t.Errorf("group[%d] = %q, cần %q", i, g.Name, wantGroups[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Gợi ý cổng trống
// ---------------------------------------------------------------------------

func TestSuggestFreePortFrom_LoaiPortDaDung(t *testing.T) {
	used := map[int]bool{3000: true, 3001: true}
	got := SuggestFreePortFrom([]int{3000, 3001, 3002}, used)
	if got != 3002 {
		t.Errorf("SuggestFreePortFrom = %d, cần 3002", got)
	}
}

func TestSuggestFreePortFrom_HetUngVien_Tra0(t *testing.T) {
	used := map[int]bool{3000: true, 3001: true}
	if got := SuggestFreePortFrom([]int{3000, 3001}, used); got != 0 {
		t.Errorf("SuggestFreePortFrom = %d, cần 0 khi hết ứng viên", got)
	}
}

func TestSuggestFreePort_LoaiPortDaKhaiTrongConfig(t *testing.T) {
	cfg := &Config{Groups: []Group{{
		Name: "Backend Go",
		Services: []Service{{
			ID: "auth-be", Name: "x", Ports: []int{3000, 3001, 3002},
			Dir: "d", Start: "s",
		}},
	}}}
	// 3000-3002 đã khai → port trống đầu tiên phải > 3002 (trừ khi đang
	// có process lắng nghe — chỉ cần chắc là KHÔNG nằm trong 3000-3002).
	got := SuggestFreePort(cfg, nil)
	if got >= 3000 && got <= 3002 {
		t.Errorf("SuggestFreePort = %d, không được trùng port đã khai", got)
	}
	if got == 0 {
		t.Errorf("SuggestFreePort phải tìm được port trống")
	}
}

// ---------------------------------------------------------------------------
// Parse các field mới
// ---------------------------------------------------------------------------

func TestParse_FieldMoi(t *testing.T) {
	content := `workspace_root: ".."
groups:
  - name: Hạ tầng
    tier: 0
    services:
      - id: infra-postgres
        name: PG
        ports: [5432]
        compose_dir: d
        compose_file: f
        compose_service: postgres
  - name: Backend Go
    tier: 1
    services:
      - id: auth-be
        name: BE
        ports: [3000]
        dir: d
        start: s
        auto_restart: true
        health_path: /healthz
        health_skip_ports: [9090]
`
	cfg, err := Parse([]byte(content))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Groups[0].Tier != 0 || cfg.Groups[1].Tier != 1 {
		t.Errorf("tier đọc sai: %+v", cfg.Groups)
	}
	svc, _ := cfg.Service("auth-be")
	if !svc.AutoRestart {
		t.Errorf("auto_restart đọc sai")
	}
	if svc.HealthPath != "/healthz" {
		t.Errorf("health_path đọc sai: %q", svc.HealthPath)
	}
	if len(svc.HealthSkipPorts) != 1 || svc.HealthSkipPorts[0] != 9090 {
		t.Errorf("health_skip_ports đọc sai: %+v", svc.HealthSkipPorts)
	}
}

func TestParse_HealthPathKhongBatDauBangSlash(t *testing.T) {
	content := `groups:
  - name: G
    services:
      - id: a
        name: A
        ports: [3000]
        dir: d
        start: s
        health_path: healthz
`
	if _, err := Parse([]byte(content)); err == nil {
		t.Fatalf("phải báo lỗi health_path không bắt đầu bằng /")
	}
}

func TestParse_FileCuKhongCoTier_VanDocDuoc(t *testing.T) {
	// File cũ không có tier → Parse KHÔNG lỗi, tier = -1.
	cfg, err := Parse([]byte(sampleYAML))
	if err != nil {
		t.Fatalf("Parse file không có tier phải OK: %v", err)
	}
	for _, g := range cfg.Groups {
		if g.Tier != -1 {
			t.Errorf("group %s tier = %d, cần -1", g.Name, g.Tier)
		}
	}
	// GroupsInStartupOrder giữ thứ tự file khi không có tier.
	order := cfg.GroupsInStartupOrder()
	for i, g := range order {
		if g.Name != cfg.Groups[i].Name {
			t.Errorf("GroupsInStartupOrder đổi thứ tự khi không có tier")
		}
	}
}

func TestGroupsInStartupOrder_SortTheoTier(t *testing.T) {
	cfg := &Config{Groups: []Group{
		{Name: "Frontend", Tier: 2},
		{Name: "Hạ tầng", Tier: 0},
		{Name: "Backend Go", Tier: 1},
		{Name: "Không tier", Tier: -1},
	}}
	order := cfg.GroupsInStartupOrder()
	want := []string{"Hạ tầng", "Backend Go", "Frontend", "Không tier"}
	for i, g := range order {
		if g.Name != want[i] {
			t.Errorf("thứ tự sai: %v (cần %v)", names(order), want)
		}
	}
}

func names(gs []Group) []string {
	out := make([]string, 0, len(gs))
	for _, g := range gs {
		out = append(out, g.Name)
	}
	return out
}

func TestUpsertService_GroupRong(t *testing.T) {
	// Group có `services:` nhưng không có service nào → thêm service vào
	// ngay sau dòng `services:`.
	content := `workspace_root: ".."
groups:
  - name: Rỗng
    services:
`
	path := writeSample(t, content)

	spec := ServiceSpec{
		ID: "a-be", Name: "A", Group: "Rỗng",
		Ports: []int{3100}, Dir: "d", Start: "s",
	}
	if err := UpsertService(path, spec); err != nil {
		t.Fatalf("UpsertService: %v", err)
	}
	after := readFile(t, path)

	if !strings.Contains(after, "      - id: a-be\n") {
		t.Errorf("chưa thêm service vào group rỗng:\n%s", after)
	}
	if _, err := Parse([]byte(after)); err != nil {
		t.Fatalf("file sau khi sửa không parse được: %v", err)
	}
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cfg.Service("a-be"); !ok {
		t.Errorf("service không đọc được")
	}
}

func TestUpsertService_SuaServiceKhongCoPorts(t *testing.T) {
	// Service cũ không có key `ports` → thêm key mới, indent đúng.
	content := `workspace_root: ".."
groups:
  - name: G
    services:
      - id: a-be
        name: A
        dir: d
        start: s
`
	path := writeSample(t, content)

	spec := ServiceSpec{
		ID: "a-be", Name: "A", Group: "G",
		Ports: []int{3100}, Dir: "d", Start: "s",
	}
	if err := UpsertService(path, spec); err != nil {
		t.Fatalf("UpsertService: %v", err)
	}
	after := readFile(t, path)

	if !strings.Contains(after, "        ports: [3100]\n") {
		t.Errorf("chưa thêm key ports:\n%s", after)
	}
	if _, err := Parse([]byte(after)); err != nil {
		t.Fatalf("file sau khi sửa không parse được: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Hợp đồng JSON của ServiceSpec (khớp binding SaveService)
// ---------------------------------------------------------------------------

func TestServiceSpec_JSONKhopHopDong(t *testing.T) {
	spec := ServiceSpec{
		ID: "auth-be", Name: "BE", Group: "Backend Go", Ports: []int{3000, 9090},
		Dir: "d", Start: "s", RepoDir: "rd",
		ComposeDir: "cd", ComposeFile: "cf", ComposeService: "cs",
		AutoRestart: true,
	}
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	// healthPath/healthSkipPorts cũng thuộc hợp đồng (F2.2): frontend gửi
	// rỗng → backend xoá key. 0-value vẫn phải xuất hiện trong JSON (không
	// omitempty) để frontend luôn gửi đủ field.
	want := `{"id":"auth-be","name":"BE","group":"Backend Go","ports":[3000,9090],` +
		`"dir":"d","start":"s","repoDir":"rd","composeDir":"cd","composeFile":"cf",` +
		`"composeService":"cs","autoRestart":true,"healthPath":"","healthSkipPorts":null}`
	if string(b) != want {
		t.Errorf("JSON ServiceSpec sai:\n có:  %s\n cần: %s", b, want)
	}
}

// ---------------------------------------------------------------------------
// Giá trị đã quote trong file
// ---------------------------------------------------------------------------

func TestUpsertService_GiaTriDaQuote(t *testing.T) {
	// File có name đã quote → sửa phải thay đúng giá trị, không để lại
	// dấu quote cũ lẫn với giá trị mới.
	content := `workspace_root: ".."
groups:
  - name: G
    services:
      - id: a-be
        name: "tên cũ: có colon"
        ports: [3100]
        dir: d
        start: s
`
	path := writeSample(t, content)

	spec := ServiceSpec{
		ID: "a-be", Name: "tên mới", Group: "G",
		Ports: []int{3100}, Dir: "d", Start: "s",
	}
	if err := UpsertService(path, spec); err != nil {
		t.Fatalf("UpsertService: %v", err)
	}
	after := readFile(t, path)

	if !strings.Contains(after, "        name: tên mới\n") {
		t.Errorf("chưa thay đúng giá trị đã quote:\n%s", after)
	}
	if strings.Contains(after, `"tên cũ`) {
		t.Errorf("còn sót giá trị cũ:\n%s", after)
	}
	if _, err := Parse([]byte(after)); err != nil {
		t.Fatalf("file sau khi sửa không parse được: %v", err)
	}
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	svc, _ := cfg.Service("a-be")
	if svc.Name != "tên mới" {
		t.Errorf("name đọc lại sai: %q", svc.Name)
	}
}
