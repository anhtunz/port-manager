// Package mirror đối chiếu bảng quy hoạch port ở AGENTS.md (mục 10.1) với các
// port khai trong ports.yaml.
//
// Nguồn sự thật về port là AGENTS.md mục 10.1; ports.yaml chỉ là MIRROR
// (ports.yaml:4-8). Package này CHỈ ĐỌC — không sửa file nào.
//
// Bảng ở mục 10.1 là markdown lẫn văn xuôi, nên bộ đọc phải bỏ qua:
//   - phần văn xuôi (chỉ lấy port nằm trong backtick, vd dòng hạ tầng);
//   - dòng "Mẹo nhớ" (chỉ chứa mẫu số để nhớ, không phải port thật);
//   - cột không phải cổng của service (cột gRPC, cột preview, cột "Port đặt
//     ở đâu") — vì các port đó được so ở chỗ khác, đem vào đây sẽ sinh lệch giả.
//
// Mọi port đọc được đều lọc theo dải hợp lệ 1024-65535; cái gì không chắc
// chắn thì đánh dấu "cần kiểm" thay vì khẳng định sai.
package mirror

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Giới hạn port hợp lệ. Mọi số ngoài dải này bị bỏ (vd "phase 1", "9" trong
// "10.1", version "0.8.0") — bảng quy hoạch chỉ dùng port > 1024.
const (
	minPort = 1024
	maxPort = 65535
)

// Mismatch là một port lệch giữa AGENTS.md mục 10.1 và ports.yaml.
//
// Toàn bộ field đều marshal ra JSON đúng hợp đồng (C2 + C6): service/group
// nói rõ lệch nằm ở service nào — với lệch "có trong ports.yaml mà thiếu trong
// AGENTS.md", UI không chỉ hiện note chung mà còn biết service nào giữ cổng.
type Mismatch struct {
	Port        int    `json:"port"`
	InAgents    bool   `json:"inAgents"`
	InPortsYaml bool   `json:"inPortsYaml"`
	Note        string `json:"note,omitempty"`

	Service string `json:"service,omitempty"`
	Group   string `json:"group,omitempty"`
}

// MirrorReport là kết quả đối chiếu trả về cho frontend.
type MirrorReport struct {
	Mismatches []Mismatch `json:"mismatches"`
	CheckedAt  string     `json:"checkedAt"`

	// Note: ghi chú ngoài danh sách lệch — vd khi KHÔNG đọc được AGENTS.md,
	// danh sách rỗng kèm note để UI hiện rõ lý do thay vì "0 lệch" gây hiểu
	// nhầm là mọi thứ đều ổn.
	Note string `json:"note,omitempty"`
}

// Owner là service + group sở hữu một port trong ports.yaml.
type Owner struct {
	Service string
	Group   string
}

// Owners: port → service/group sở hữu nó (dùng để ghi chú cho dễ đọc).
type Owners map[int]Owner

// PortRange là dải cổng tìm thấy trong bảng (vd 5173–5179).
//
// Dải KHÔNG so sánh 1-1 với ports.yaml: đó là dải "dành riêng" ghi trong
// bảng, không phải cổng của một service cụ thể. Vẫn giữ lại để hiển thị.
type PortRange struct {
	From   int
	To     int
	Ports  []int
	Detail string
}

// Table là kết quả đọc mục 10.1 của AGENTS.md.
type Table struct {
	// Ports: các port thuộc phạm vi so sánh (cột HTTP / Dev server + hạ tầng).
	Ports map[int]bool
	// Ignored: port nằm ở cột bị loại (gRPC, preview) — loại ở CẢ hai phía
	// khi so, để không sinh lệch giả (vd gRPC 9090 khai ở ports.yaml).
	Ignored map[int]bool
	// Ranges: các dải cổng tìm thấy (không so 1-1).
	Ranges []PortRange
	// Detail: port → ngữ cảnh (ô bảng hoặc đoạn backtick) để viết ghi chú.
	Detail map[int]string
	// Secondary: port nằm trong ngoặc (vd 6790 "utoopack mở thêm 6790 nội bộ")
	// — thường là cổng phụ, cần user xác nhận.
	Secondary map[int]bool
	// Uncertain: port đọc từ cột không nhận diện chắc chắn → "cần kiểm".
	Uncertain map[int]bool
	// DevPorts / InfraPorts: ứng viên cho SuggestPort (dev server trước, hạ tầng sau).
	DevPorts   []int
	InfraPorts []int
}

func newTable() Table {
	return Table{
		Ports:     map[int]bool{},
		Ignored:   map[int]bool{},
		Detail:    map[int]string{},
		Secondary: map[int]bool{},
		Uncertain: map[int]bool{},
	}
}

// ParseFile đọc file AGENTS.md và trả về bảng port ở mục 10.1.
func ParseFile(path string) (Table, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return newTable(), fmt.Errorf("đọc AGENTS.md: %w", err)
	}
	return Parse(data)
}

// PortsInFile đọc file AGENTS.md và trả về tập port tìm thấy ở mục 10.1.
//
// Hàm rút gọn cho chỗ chỉ cần tập port (vd đối chiếu nhanh, gợi ý cổng).
func PortsInFile(path string) (map[int]bool, error) {
	tbl, err := ParseFile(path)
	if err != nil {
		return map[int]bool{}, err
	}
	return tbl.PortSet(), nil
}

// Parse đọc nội dung markdown của AGENTS.md và trả về bảng port ở mục 10.1.
//
// Lỗi duy nhất hiện tại: không tìm thấy mục 10.1 trong file.
func Parse(md []byte) (Table, error) {
	t := newTable()
	lines := splitLines(string(md))
	section, err := section101(lines)
	if err != nil {
		return t, err
	}
	for i := 0; i < len(section); {
		if isTableRow(section[i]) {
			j := i
			for j < len(section) && isTableRow(section[j]) {
				j++
			}
			t.addTable(section[i:j])
			i = j
			continue
		}
		t.addProse(section[i])
		i++
	}
	t.sortPorts()
	// Port nằm trong tập so sánh thì không còn "bị loại" — một số có thể xuất
	// hiện ở cả cột port và cột bị loại (vd 3005 vừa ở cột HTTP của bảng
	// Backend vừa ở cột "Port đặt ở đâu" của bảng Frontend theo `→ :3005`).
	for p := range t.Ports {
		delete(t.Ignored, p)
	}
	return t, nil
}

// PortSet trả bản sao tập port thuộc phạm vi so sánh.
func (t Table) PortSet() map[int]bool {
	out := make(map[int]bool, len(t.Ports))
	for p := range t.Ports {
		out[p] = true
	}
	return out
}

// SortedPorts trả tập port thuộc phạm vi so sánh, sắp xếp tăng dần.
func (t Table) SortedPorts() []int {
	out := make([]int, 0, len(t.Ports))
	for p := range t.Ports {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

// Compare đối chiếu tập port ở AGENTS.md mục 10.1 với tập port khai trong
// ports.yaml và trả về danh sách lệch.
//
// yamlPorts: tập port đang khai (config.ConfiguredPorts()).
func Compare(t Table, yamlPorts map[int]bool) MirrorReport {
	return CompareWithOwners(t, yamlPorts, nil)
}

// CompareWithOwners như Compare, thêm thông tin service/group sở hữu port
// trong ports.yaml để ghi chú dễ đọc hơn.
func CompareWithOwners(t Table, yamlPorts map[int]bool, owners Owners) MirrorReport {
	rep := MirrorReport{
		Mismatches: []Mismatch{},
		CheckedAt:  time.Now().Format(time.RFC3339),
	}

	// Port nằm ở cột bị loại (gRPC/preview) không thuộc phạm vi so sánh →
	// loại ở cả hai phía, nếu không sẽ sinh lệch giả.
	inAgents := map[int]bool{}
	for p := range t.Ports {
		inAgents[p] = true
	}
	inYaml := map[int]bool{}
	for p := range yamlPorts {
		// Port bị loại khỏi phạm vi so sánh (gRPC/preview) chỉ bị loại ở phía
		// ports.yaml khi nó KHÔNG nằm trong tập so sánh của AGENTS.md.
		if t.Ignored[p] && !inAgents[p] {
			continue
		}
		inYaml[p] = true
	}

	all := make([]int, 0, len(inAgents)+len(inYaml))
	seen := map[int]bool{}
	for p := range inAgents {
		if !seen[p] {
			seen[p] = true
			all = append(all, p)
		}
	}
	for p := range inYaml {
		if !seen[p] {
			seen[p] = true
			all = append(all, p)
		}
	}
	sort.Ints(all)

	for _, p := range all {
		a, y := inAgents[p], inYaml[p]
		if a == y {
			continue
		}
		m := Mismatch{Port: p, InAgents: a, InPortsYaml: y}
		if o, ok := owners[p]; ok {
			m.Service = o.Service
			m.Group = o.Group
		}
		m.Note = noteFor(t, m, owners)
		rep.Mismatches = append(rep.Mismatches, m)
	}
	return rep
}

// SuggestPort gợi ý một cổng đang trống, lấy từ bảng quy hoạch ở AGENTS.md.
//
// used: tập port đã dùng (config.ConfiguredPorts() ∪ port đang listen).
// Ưu tiên cổng dev server (HTTP / Dev server), sau đó mới tới cổng hạ tầng.
// Không còn ứng viên → trả 0.
func SuggestPort(t Table, used map[int]bool) int {
	for _, list := range [][]int{t.DevPorts, t.InfraPorts} {
		for _, p := range list {
			if used[p] {
				continue
			}
			// Cổng ghi trong ngoặc (vd 6790 nội bộ utoopack) không phải cổng
			// service thật → không gợi ý.
			if t.Secondary[p] {
				continue
			}
			return p
		}
	}
	return 0
}

// noteFor viết ghi chú cho một lệch. Khi không chắc chắn thì ghi "cần kiểm"
// thay vì khẳng định sai.
func noteFor(t Table, m Mismatch, owners Owners) string {
	if m.InAgents && !m.InPortsYaml {
		parts := []string{"chỉ có trong AGENTS.md mục 10.1"}
		if d := strings.TrimSpace(t.Detail[m.Port]); d != "" {
			parts = append(parts, fmt.Sprintf("ô bảng: %q", d))
		}
		if t.Secondary[m.Port] {
			parts = append(parts, "cổng ghi trong ngoặc (có thể là cổng nội bộ) — cần kiểm")
		}
		if t.Uncertain[m.Port] {
			parts = append(parts, "cột port không nhận diện chắc chắn — cần kiểm")
		}
		parts = append(parts, "chưa khai trong ports.yaml")
		return strings.Join(parts, " — ")
	}

	// Chỉ có trong ports.yaml.
	parts := []string{"chỉ có trong ports.yaml"}
	o, hasOwner := owners[m.Port]
	switch {
	case hasOwner && o.Service != "":
		if o.Group != "" {
			parts = append(parts, fmt.Sprintf("service %s · group %s", o.Service, o.Group))
		} else {
			parts = append(parts, fmt.Sprintf("service %s", o.Service))
		}
	default:
		parts = append(parts, "cần kiểm: không rõ service nào khai port này")
	}
	parts = append(parts, "chưa có trong AGENTS.md mục 10.1")
	return strings.Join(parts, " — ")
}

// ---------------------------------------------------------------------------
// Đọc markdown
// ---------------------------------------------------------------------------

var headingRe = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)

// section101 cắt phần nội dung của mục 10.1 (từ sau tiêu đề "10.1" tới
// tiêu đề cùng cấp trở xuống, vd "### 10.2").
func section101(lines []string) ([]string, error) {
	start := -1
	level := 0
	for i, ln := range lines {
		m := headingRe.FindStringSubmatch(strings.TrimRight(ln, " \t\r"))
		if m == nil {
			continue
		}
		if isSection101Title(strings.TrimSpace(m[2])) {
			start = i + 1
			level = len(m[1])
			break
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("không tìm thấy mục 10.1 trong AGENTS.md")
	}
	out := make([]string, 0, len(lines)-start)
	for _, ln := range lines[start:] {
		m := headingRe.FindStringSubmatch(strings.TrimRight(ln, " \t\r"))
		if m != nil && len(m[1]) <= level {
			break
		}
		out = append(out, ln)
	}
	return out, nil
}

// isSection101Title kiểm tra tiêu đề có phải "10.1 ..." hay không ("10.10" thì không).
func isSection101Title(title string) bool {
	t := strings.TrimSpace(title)
	if !strings.HasPrefix(t, "10.1") {
		return false
	}
	rest := t[len("10.1"):]
	if rest == "" {
		return true
	}
	switch rest[0] {
	case '.', ' ', '\t', ':', '-':
		return true
	}
	// "10.1–..." (dấu gạch dài) — kiểm tra theo byte đầu tiên của ký tự.
	if strings.HasPrefix(rest, "–") {
		return true
	}
	return false
}

// isTableRow: dòng bảng markdown (bắt đầu và kết thúc bằng "|").
func isTableRow(line string) bool {
	s := strings.TrimSpace(line)
	return len(s) >= 2 && strings.HasPrefix(s, "|") && strings.HasSuffix(s, "|")
}

// isSeparatorRow: dòng phân cách header của bảng markdown ("| --- | --- |").
func isSeparatorRow(line string) bool {
	cells := splitCells(line)
	if len(cells) == 0 {
		return false
	}
	for _, c := range cells {
		c = strings.TrimSpace(c)
		if c == "" {
			return false
		}
		if strings.Trim(c, "-:") != "" {
			return false
		}
		if !strings.Contains(c, "-") {
			return false
		}
	}
	return true
}

// splitCells tách một dòng bảng thành các ô (bỏ 2 dấu "|" ngoài cùng).
func splitCells(line string) []string {
	s := strings.TrimSpace(line)
	s = strings.TrimPrefix(s, "|")
	s = strings.TrimSuffix(s, "|")
	parts := strings.Split(s, "|")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

// addTable nạp một khối bảng markdown vào bảng port.
func (t *Table) addTable(block []string) {
	if len(block) == 0 {
		return
	}
	var header []string
	var rows [][]string
	if len(block) >= 2 && isSeparatorRow(block[1]) {
		header = splitCells(block[0])
		for _, l := range block[2:] {
			rows = append(rows, splitCells(l))
		}
	} else {
		// Không có dòng phân cách → không nhận diện được cột nào là cột port.
		for _, l := range block {
			rows = append(rows, splitCells(l))
		}
	}

	portCols, ignoredCols, uncertain := classifyColumns(header, rows)

	for _, ci := range ignoredCols {
		for _, row := range rows {
			if ci >= len(row) {
				continue
			}
			ports, _, _ := extractPorts(row[ci])
			for _, p := range ports {
				t.Ignored[p] = true
			}
		}
	}

	for _, row := range rows {
		for _, ci := range portCols {
			if ci >= len(row) {
				continue
			}
			cell := row[ci]
			ports, ranges, secondary := extractPorts(cell)
			for _, r := range ranges {
				r.Detail = strings.TrimSpace(cell)
				t.Ranges = append(t.Ranges, r)
			}
			for _, p := range ports {
				// Port đọc được ở cột port thì KHÔNG còn bị loại, kể cả khi
				// cùng số đó xuất hiện ở một cột bị loại (vd "3003" vừa ở cột
				// Dev server vừa ở cột "Port đặt ở đâu" theo `--port 3003`).
				delete(t.Ignored, p)
				if t.Ports[p] {
					continue
				}
				t.Ports[p] = true
				t.Detail[p] = strings.TrimSpace(cell)
				t.Secondary[p] = secondary[p]
				if uncertain {
					t.Uncertain[p] = true
				}
				t.DevPorts = append(t.DevPorts, p)
			}
		}
	}
}

// classifyColumns chọn cột nào là cột port, cột nào bị loại.
//
//   - Cột có tiêu đề gRPC / Preview / "Port đặt ở đâu" → bị loại (không thuộc
//     phạm vi so sánh; port của chúng được loại ở cả phía ports.yaml).
//   - Cột có tiêu đề HTTP / Dev server / Cổng / Port → cột port.
//   - Không nhận diện được tiêu đề nào → chọn cột nhiều số-port nhất và đánh dấu
//     uncertain (mọi port đọc từ đó sẽ ghi "cần kiểm").
func classifyColumns(header []string, rows [][]string) (portCols, ignoredCols []int, uncertain bool) {
	if len(header) == 0 {
		// Bảng không có header: không loại được cột nào → đọc tất cả, đánh dấu
		// không chắc chắn.
		for i := range rows[0] {
			portCols = append(portCols, i)
		}
		return portCols, nil, true
	}
	for i, h := range header {
		switch {
		case excludedColumn(h):
			ignoredCols = append(ignoredCols, i)
		case portColumn(h):
			portCols = append(portCols, i)
		}
	}
	if len(portCols) > 0 {
		return portCols, ignoredCols, false
	}

	// Không có cột nào khớp → chọn cột nhiều port nhất trong các cột chưa bị loại.
	candidates := make([]int, 0, len(header))
	for i := range header {
		if excludedColumn(header[i]) {
			continue
		}
		candidates = append(candidates, i)
	}
	if len(candidates) == 0 {
		return nil, ignoredCols, true
	}
	best, bestCount := candidates[0], -1
	for _, ci := range candidates {
		count := 0
		for _, row := range rows {
			if ci >= len(row) {
				continue
			}
			ports, _, _ := extractPorts(row[ci])
			count += len(ports)
		}
		if count > bestCount {
			best, bestCount = ci, count
		}
	}
	return []int{best}, ignoredCols, true
}

// excludedColumn: cột không thuộc phạm vi so sánh.
func excludedColumn(header string) bool {
	h := strings.ToLower(strings.TrimSpace(header))
	switch {
	case strings.Contains(h, "grpc"):
		return true
	case strings.Contains(h, "preview"):
		return true
	case strings.Contains(h, "đặt ở đâu"), strings.Contains(h, "dat o dau"):
		return true
	case strings.Contains(h, "ghi chú"), strings.Contains(h, "note"):
		return true
	}
	return false
}

// portColumn: cột chứa cổng của service.
func portColumn(header string) bool {
	h := strings.ToLower(strings.TrimSpace(header))
	switch h {
	case "http", "http port", "port", "ports", "cong", "cổng", "dev", "dev port":
		return true
	}
	if strings.Contains(h, "http") {
		return true
	}
	if strings.Contains(h, "dev server") {
		return true
	}
	if strings.Contains(h, "cổng") || strings.Contains(h, "cong ") {
		return true
	}
	return false
}

// addProse xử lý một dòng văn xuôi trong mục 10.1.
//
// Chỉ lấy port nằm trong backtick (vd dòng hạ tầng: postgres `5432` · …) —
// phần văn xuôi khác và dòng "Mẹo nhớ" không sinh port nào.
func (t *Table) addProse(line string) {
	s := strings.TrimSpace(line)
	if s == "" {
		return
	}
	if isMemoLine(s) {
		return
	}
	for _, span := range backtickSpans(s) {
		inner := strings.TrimSpace(span)
		if !isNumericSpan(inner) {
			continue
		}
		ports, ranges, secondary := extractPorts(inner)
		for _, r := range ranges {
			r.Detail = inner
			t.Ranges = append(t.Ranges, r)
		}
		for _, p := range ports {
			if t.Ports[p] {
				continue
			}
			t.Ports[p] = true
			t.Detail[p] = inner
			t.Secondary[p] = secondary[p]
			t.InfraPorts = append(t.InfraPorts, p)
		}
	}
}

// isMemoLine: dòng "Mẹo nhớ" chỉ chứa mẫu số để nhớ (3xxx, 68xx…) — không
// phải port thật nên bỏ hẳn.
func isMemoLine(s string) bool {
	l := strings.ToLower(strings.TrimSpace(s))
	l = strings.TrimLeft(l, "*_#>-–— \t")
	return strings.HasPrefix(l, "mẹo nhớ") || strings.HasPrefix(l, "meo nho")
}

// backtickSpans trả nội dung các đoạn nằm trong backtick.
func backtickSpans(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		j := strings.IndexByte(s[i:], '`')
		if j < 0 {
			break
		}
		open := i + j
		k := strings.IndexByte(s[open+1:], '`')
		if k < 0 {
			break
		}
		out = append(out, s[open+1:open+1+k])
		i = open + 1 + k + 1
	}
	return out
}

// isNumericSpan: đoạn backtick chỉ chứa port (hoặc dải port) → lấy được.
func isNumericSpan(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == '-' || r == '–' || r == '—' || r == ' ' || r == '\t'
	})
	if len(parts) == 0 || len(parts) > 2 {
		return false
	}
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < minPort || n > maxPort {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Bóc số trong ô bảng
// ---------------------------------------------------------------------------

type numToken struct {
	start, end int
	val        int
}

// extractPorts lấy các port trong một ô bảng (hoặc một đoạn backtick).
//
// Nhận diện được: số đơn ("3000"), số kèm ngoặc ("3004 (REST healthz/version)"),
// dải cổng ("5173–5179" với EN DASH U+2013), ô rỗng "—" (không có port).
// Trả về: port đơn, các dải tìm thấy, và cờ "port nằm trong ngoặc".
func extractPorts(text string) (ports []int, ranges []PortRange, secondary map[int]bool) {
	secondary = map[int]bool{}
	tokens := scanNumbers(text)
	consumed := make([]bool, len(tokens))

	for i := 0; i+1 < len(tokens); i++ {
		if consumed[i] || consumed[i+1] {
			continue
		}
		a, b := tokens[i], tokens[i+1]
		if !isRangeSeparator(text[a.end:b.start]) {
			continue
		}
		if a.val < minPort || a.val > maxPort || b.val < minPort || b.val > maxPort {
			continue
		}
		if b.val < a.val || b.val-a.val > 64 {
			continue
		}
		consumed[i], consumed[i+1] = true, true
		r := PortRange{From: a.val, To: b.val, Detail: strings.TrimSpace(text)}
		for p := a.val; p <= b.val; p++ {
			r.Ports = append(r.Ports, p)
		}
		ranges = append(ranges, r)
	}

	parens := parenSpans(text)
	for i, tok := range tokens {
		if consumed[i] {
			continue
		}
		if tok.val < minPort || tok.val > maxPort {
			continue
		}
		ports = append(ports, tok.val)
		if inAnySpan(parens, tok.start, tok.end) {
			secondary[tok.val] = true
		}
	}
	return ports, ranges, secondary
}

// scanNumbers quét các số trong chuỗi, bỏ qua số nằm trong từ (vd "v1.2.3",
// "0.8.0", "phase1") — chỉ nhận số đứng riêng.
func scanNumbers(s string) []numToken {
	var out []numToken
	for i := 0; i < len(s); {
		if !isDigit(s[i]) {
			i++
			continue
		}
		j := i
		for j < len(s) && isDigit(s[j]) {
			j++
		}
		if (i == 0 || !isWordByte(s[i-1])) && (j == len(s) || !isWordByte(s[j])) {
			if n, err := strconv.Atoi(s[i:j]); err == nil {
				out = append(out, numToken{start: i, end: j, val: n})
			}
		}
		i = j
	}
	return out
}

// isWordByte: ký tự làm số "dính" vào nhau (chữ, số, dấu chấm) → số đó không
// đứng riêng nên bị bỏ.
func isWordByte(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '.' || c == '_'
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// isRangeSeparator: giữa 2 số có đúng một dấu gạch (hoặc "..") → dải cổng.
func isRangeSeparator(s string) bool {
	t := strings.TrimSpace(s)
	switch t {
	case "-", "–", "..":
		return true
	}
	return false
}

// parenSpans trả các khoảng (byte) bên trong ngoặc tròn.
func parenSpans(s string) [][2]int {
	var out [][2]int
	depth := 0
	start := -1
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			if depth == 0 {
				start = i
			}
			depth++
		case ')':
			if depth > 0 {
				depth--
				if depth == 0 && start >= 0 {
					out = append(out, [2]int{start, i})
				}
			}
		}
	}
	return out
}

func inAnySpan(spans [][2]int, start, end int) bool {
	for _, sp := range spans {
		if start > sp[0] && end <= sp[1] {
			return true
		}
	}
	return false
}

// sortPorts sắp xếp các danh sách port để SuggestPort cho kết quả ổn định.
func (t *Table) sortPorts() {
	sort.Ints(t.DevPorts)
	sort.Ints(t.InfraPorts)
}

// splitLines tách file thành dòng (xoá \r).
func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.Split(s, "\n")
}
