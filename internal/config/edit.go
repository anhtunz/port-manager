package config

// Sửa ports.yaml BẰNG CÁCH CHỈNH TEXT THEO DÒNG — không dùng yaml.Marshal.
//
// Vì sao không marshal: marshal xuống lại sẽ mất toàn bộ comment, mất flow
// style `ports: [3000, 9090]`, và sinh thêm key rỗng (`repo_dir: ""`…) cho mọi
// service không compose — file người dùng tự sửa sẽ bị phá.
//
// Cơ chế: đọc file thành list dòng, dùng yaml.Node (giữ comment) để ĐỊNH VỊ
// node (node.Line / node.Column), rồi sửa đúng dòng đó. Mọi dòng không đụng
// tới giữ nguyên nguyên byte (kể cả comment đầu file).
//
// Sau mọi lần sửa: dựng lại Config từ nội dung mới và gọi Parse — nếu Parse
// lỗi (port trùng, thiếu dir/start…) thì KHÔNG ghi file, trả lỗi.
// Ghi file: ghi file tạm rồi rename (atomic).

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ServiceSpec là đặc tả service cần ghi vào ports.yaml (thêm mới hoặc sửa).
// Tên field + thứ tự giống hợp đồng JSON của binding SaveService.
type ServiceSpec struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Group          string `json:"group"`
	Ports          []int  `json:"ports"`
	Dir            string `json:"dir"`
	Start          string `json:"start"`
	RepoDir        string `json:"repoDir"`
	ComposeDir     string `json:"composeDir"`
	ComposeFile    string `json:"composeFile"`
	ComposeService string `json:"composeService"`
	AutoRestart    bool   `json:"autoRestart"`
	// HealthPath / HealthSkipPorts: health_path + health_skip_ports (F2.2).
	// Tuỳ chọn — rỗng/rỗng danh sách → xoá key khỏi file (như repo_dir).
	HealthPath      string `json:"healthPath"`
	HealthSkipPorts []int  `json:"healthSkipPorts"`
}

// Validate kiểm tra spec trước khi ghi — lỗi trả về bằng tiếng Việt.
func (s ServiceSpec) Validate() error {
	if strings.TrimSpace(s.ID) == "" {
		return fmt.Errorf("thiếu id của service")
	}
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("service %s thiếu name", s.ID)
	}
	if len(s.Ports) == 0 {
		return fmt.Errorf("service %s thiếu ports", s.ID)
	}
	seen := map[int]bool{}
	for _, p := range s.Ports {
		if p <= 0 || p > 65535 {
			return fmt.Errorf("service %s có port %d không hợp lệ", s.ID, p)
		}
		if seen[p] {
			return fmt.Errorf("service %s khai port %d trùng ngay trong nó", s.ID, p)
		}
		seen[p] = true
	}
	if s.ComposeService != "" {
		if s.ComposeDir == "" || s.ComposeFile == "" {
			return fmt.Errorf("service compose %s thiếu compose_dir/compose_file", s.ID)
		}
	} else if s.Dir == "" || s.Start == "" {
		return fmt.Errorf("service %s thiếu dir/start", s.ID)
	}
	return nil
}

// AddIgnorePort thêm port vào danh sách unknown_ignore_ports của ports.yaml
// (dùng cho nút "ẩn cổng lạ" — F1.4).
//
// Idempotent: port đã có trong danh sách → không thêm, không lỗi.
// Giữ nguyên comment + định dạng file; nếu file chưa có key
// unknown_ignore_ports thì tạo key top-level (indent 0) + item indent 2.
func AddIgnorePort(path string, port int) error {
	if port <= 0 || port > 65535 {
		return fmt.Errorf("port %d không hợp lệ", port)
	}
	d, err := readDoc(path)
	if err != nil {
		return err
	}
	e := newEdits(d.lines)

	keyNode, seq, hasKey := mapGet(d.rootMap(), "unknown_ignore_ports")
	switch {
	case hasKey && seq.Kind == yaml.SequenceNode:
		// Đã có key + danh sách: port đã trong danh sách → idempotent.
		for _, item := range seq.Content {
			if item.Kind == yaml.ScalarNode && item.Value == strconv.Itoa(port) {
				return nil
			}
		}
		// Thêm một dòng cuối danh sách, giữ indent của các dòng bên cạnh.
		last := seq.Content[len(seq.Content)-1]
		indent := strings.Repeat(" ", maxInt(0, last.Column-3))
		e.insertAfter(last.Line-1, indent+"- "+strconv.Itoa(port))
		return d.save(e)

	case hasKey:
		// Có key nhưng rỗng (`unknown_ignore_ports:` không có item) → thêm
		// item ngay sau dòng key, KHÔNG tạo key trùng.
		e.insertAfter(keyNode.Line-1, "  - "+strconv.Itoa(port))
		return d.save(e)

	default:
		// Chưa có key → tạo mới, chèn ngay sau khối liên quan
		// (unknown_ignore_commands), không chèn lên đầu file.
		anchor, block := d.newIgnorePortsBlock(port)
		e.insertAfter(anchor, block...)
		return d.save(e)
	}
}

// SetAutoRestart bật/tắt cờ auto_restart của một service (F3.1).
//
// Cờ có sẵn → chỉ đổi giá trị trên đúng dòng đó (giữ comment đuôi dòng).
// Chưa có cờ và bật → thêm một dòng `auto_restart: true` cuối khối service.
// Chưa có cờ và tắt → không ghi file (mặc định đã là tắt).
func SetAutoRestart(path, id string, on bool) error {
	d, err := readDoc(path)
	if err != nil {
		return err
	}
	_, svc, ok := d.findService(id)
	if !ok {
		return fmt.Errorf("không tìm thấy service %q", id)
	}
	e := newEdits(d.lines)
	keyNode, valNode, exists := mapGet(svc, "auto_restart")
	if exists {
		if valNode == nil || valNode.Value == boolText(on) {
			return nil // không đổi → không đụng file
		}
		e.setScalar(keyNode, valNode, boolText(on))
		return d.save(e)
	}
	if !on {
		return nil // mặc định đã tắt → không cần ghi file
	}
	indent := strings.Repeat(" ", maxInt(0, svc.Column-1))
	e.insertAfter(lastLine(svc)-1, indent+"auto_restart: "+boolText(on))
	return d.save(e)
}

// DeleteService xoá một service khỏi ports.yaml (giữ nguyên thứ tự group và
// service còn lại). Nếu group không còn service nào thì xoá luôn group đó.
func DeleteService(path, id string) error {
	d, err := readDoc(path)
	if err != nil {
		return err
	}
	group, svc, ok := d.findService(id)
	if !ok {
		return fmt.Errorf("không tìm thấy service %q", id)
	}
	e := newEdits(d.lines)
	e.removeRange(svc.Line, lastLine(svc))

	// Group rỗng sau khi xoá → xoá luôn khối group (kèm dòng trống ngay
	// sau, để không để lại hai dòng trống liên tiếp).
	if group != nil && group.services != nil && len(group.services.Content) == 1 {
		e.removeBlock(group.node.Line, lastLine(group.node))
	}
	return d.save(e)
}

// UpsertService thêm mới hoặc sửa một service trong ports.yaml.
//
// Thêm mới: chèn CUỐI danh sách services của group (indent 6 cho `- id:`,
// 8 cho key — khớp ports.yaml), không sửa group nào khác.
// Sửa: chỉ thay đúng dòng của field bị đổi, giữ nguyên comment đuôi dòng và
// flow style của `ports`.
//
// Group trong spec rỗng → giữ service ở group hiện tại. Group khác group hiện
// tại → service được chuyển sang group mới (xoá khỏi group cũ, thêm vào group
// mới). Group không tồn tại → tạo group mới ở cuối danh sách groups.
func UpsertService(path string, spec ServiceSpec) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	d, err := readDoc(path)
	if err != nil {
		return err
	}
	e := newEdits(d.lines)

	oldGroup, existing, found := d.findService(spec.ID)

	// Sửa service đã có (và ở đúng group) → sửa tại chỗ.
	if found && (spec.Group == "" || oldGroup.name == spec.Group) {
		d.applyServiceEdits(e, existing, spec)
		return d.save(e)
	}

	// Chuyển sang group khác, hoặc thêm mới.
	// Service đang tồn tại ở group khác → xoá khỏi group cũ rồi dựng lại ở
	// group mới, KHÔNG dùng renderService vẽ lại: nó sẽ đặt lại toàn bộ managed
	// keys (mất comment đuôi dòng của repo_dir/start…) và bỏ các key lạ user
	// tự sửa tay trong ports.yaml (vd "note:"). Thay vào đó:
	//  1) applyServiceEdits sửa managed keys IN-PLACE trên khối cũ — setScalar
	//     giữ comment đuôi dòng và flow style ports;
	//  2) mang NGUYÊN VĂN cả khối service (đã áp edits: dòng xoá bị bỏ, dòng
	//     đổi lấy nội dung mới, key mới thêm đi kèm đúng chỗ) sang vị trí mới,
	//     chỉ re-indent. Comment/key lạ giữ nguyên.
	var raw []string
	oldKeyIndent := 0
	if found {
		oldKeyIndent = existing.Column - 1
		d.applyServiceEdits(e, existing, spec)
		raw = e.carriedServiceLines(existing)
		e.removeRange(existing.Line, lastLine(existing))
	}

	target, ok := d.findGroup(spec.Group)
	if !ok {
		if spec.Group == "" {
			return fmt.Errorf("service %s thiếu group", spec.ID)
		}
		// Tạo group mới ở cuối danh sách groups (indent chuẩn 2/4/6/8).
		lines := renderGroupClip(spec.Group)
		lines = append(lines, d.renderService(spec, 6, 8)...)
		lines = append(lines, reindent(raw, oldKeyIndent, 8)...)
		e.insertAfter(d.groupInsertPos(), lines...)
		return d.save(e)
	}
	pos, dashIndent, keyIndent := d.newServicePos(target)
	var lines []string
	if len(raw) > 0 {
		// Khối đã áp edits xong → chỉ vẽ dòng dash `- id:` riêng (nó luôn là
		// dòng đầu khối và đã bị loại khỏi raw) rồi ghép phần còn lại nguyên
		// bản. Đừng gọi lại renderService — sẽ sinh managed keys hai lần.
		lines = append(lines, strings.Repeat(" ", dashIndent)+"- id: "+yamlScalar(spec.ID))
		lines = append(lines, reindent(raw, oldKeyIndent, keyIndent)...)
	} else {
		lines = d.renderService(spec, dashIndent, keyIndent)
	}
	e.insertAfter(pos, lines...)
	return d.save(e)
}

// carriedServiceLines gom NGUYÊN VĂN các dòng của khối service SAU khi áp
// edits (caller gọi applyServiceEdits trước):
//   - dòng bị removeKey/setScalar-xoá-list bị bỏ;
//   - dòng đã setScalar lấy nội dung mới (giữ comment đuôi dòng);
//   - key mới thêm (repo_dir/health_*/auto_restart… — insertAfter cuối khối)
//     đi kèm đúng vị trí, kể cả khi dòng neo bị xoá;
//   - key lạ ngoài hợp đồng ServiceSpec giữ nguyên dòng gốc kèm comment.
//
// Dòng dash mở khối (`- id: …`, line == svc.Line) KHÔNG mang — caller tự vẽ
// vì id luôn là dòng đầu của khối service ở vị trí mới.
func (e *edits) carriedServiceLines(svc *yaml.Node) []string {
	out := []string{}
	if svc.Line == lastLine(svc) {
		// Khối 1 dòng duy nhất: mọi key applyServiceEdits thêm vào nằm ngay
		// sau dòng dash.
		return append(out, e.insert[svc.Line-1]...)
	}
	for ln := svc.Line + 1; ln <= lastLine(svc); ln++ {
		line, replaced := e.replace[ln-1]
		if !replaced {
			line = e.lines[ln-1]
		}
		if !e.remove[ln-1] {
			out = append(out, line)
		}
		out = append(out, e.insert[ln-1]...)
	}
	return out
}

// reindent đặt lại indent của các dòng raw: dòng key đặt tại cột base+1, các
// dòng sâu hơn (item list block…) giữ nguyên chênh lệch so với indent key cũ.
func reindent(lines []string, oldKeyIndent, base int) []string {
	if len(lines) == 0 {
		return nil
	}
	out := make([]string, len(lines))
	for i, ln := range lines {
		trimmed := strings.TrimLeft(ln, " ")
		rel := len(ln) - len(trimmed) - oldKeyIndent
		if rel < 0 {
			rel = 0
		}
		out[i] = strings.Repeat(" ", base+rel) + trimmed
	}
	return out
}

// rawRange trả các dòng (1-based, inclusive) nguyên văn của file.
func rawRange(lines []string, from, to int) []string {
	if from < 1 {
		from = 1
	}
	if to > len(lines) {
		to = len(lines)
	}
	if from > to {
		return nil
	}
	out := make([]string, to-from+1)
	copy(out, lines[from-1:to])
	return out
}

// ---------------------------------------------------------------------------
// Cấu trúc đọc file
// ---------------------------------------------------------------------------

// yamlDoc là ports.yaml đã đọc: nội dung thô + cây node để định vị.
type yamlDoc struct {
	path  string
	lines []string
	root  *yaml.Node
	mode  os.FileMode
}

// groupRef trỏ tới một group trong file.
type groupRef struct {
	name     string
	node     *yaml.Node
	services *yaml.Node
}

func readDoc(path string) (*yamlDoc, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("không tìm thấy ports.yaml để ghi")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("không tìm thấy ports.yaml để ghi: %s", path)
		}
		return nil, fmt.Errorf("đọc %s: %w", path, err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("yaml %s: %w", path, err)
	}
	if len(root.Content) == 0 {
		return nil, fmt.Errorf("%s không có nội dung", path)
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1] // bỏ phần tử rỗng cuối do newline cuối
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	return &yamlDoc{path: path, lines: lines, root: &root, mode: mode}, nil
}

func (d *yamlDoc) rootMap() *yaml.Node {
	if d.root.Kind == yaml.DocumentNode && len(d.root.Content) > 0 {
		return d.root.Content[0]
	}
	return d.root
}

// findGroup tìm group theo tên.
func (d *yamlDoc) findGroup(name string) (*groupRef, bool) {
	if name == "" {
		return nil, false
	}
	_, gseq, ok := mapGet(d.rootMap(), "groups")
	if !ok || gseq.Kind != yaml.SequenceNode {
		return nil, false
	}
	for _, g := range gseq.Content {
		_, n, ok := mapGet(g, "name")
		if !ok || n.Value != name {
			continue
		}
		_, sv, _ := mapGet(g, "services")
		return &groupRef{name: name, node: g, services: sv}, true
	}
	return nil, false
}

// findService tìm service theo id ở bất kỳ group nào.
func (d *yamlDoc) findService(id string) (*groupRef, *yaml.Node, bool) {
	_, gseq, ok := mapGet(d.rootMap(), "groups")
	if !ok || gseq.Kind != yaml.SequenceNode {
		return nil, nil, false
	}
	for _, g := range gseq.Content {
		_, n, _ := mapGet(g, "name")
		name := ""
		if n != nil {
			name = n.Value
		}
		_, sv, _ := mapGet(g, "services")
		gr := &groupRef{name: name, node: g, services: sv}
		if svc, ok := gr.findService(id); ok {
			return gr, svc, true
		}
	}
	return nil, nil, false
}

// findService tìm service theo id trong group.
func (g *groupRef) findService(id string) (*yaml.Node, bool) {
	if g == nil || g.services == nil || g.services.Kind != yaml.SequenceNode {
		return nil, false
	}
	for _, s := range g.services.Content {
		_, iv, ok := mapGet(s, "id")
		if ok && iv.Value == id {
			return s, true
		}
	}
	return nil, false
}

// newIgnorePortsBlock trả (chỉ số dòng 0-based để chèn SAU, các dòng cần
// chèn) cho trường hợp file CHƯA có key unknown_ignore_ports.
//
// Khối được chèn ngay sau unknown_ignore_commands (khối liên quan), không
// chèn lên đầu file. Nếu không có unknown_ignore_commands → chèn trước
// dòng `groups:`.
func (d *yamlDoc) newIgnorePortsBlock(port int) (int, []string) {
	block := []string{"unknown_ignore_ports:", "  - " + strconv.Itoa(port)}
	if _, seq, ok := mapGet(d.rootMap(), "unknown_ignore_commands"); ok &&
		seq.Kind == yaml.SequenceNode && len(seq.Content) > 0 {
		anchor := lastLine(seq) - 1
		// Tách khối bằng một dòng trống nếu dòng trước đó chưa trống.
		if anchor >= 0 && strings.TrimSpace(d.lines[anchor]) != "" {
			block = append([]string{""}, block...)
		}
		return anchor, block
	}
	if keyNode, _, ok := mapGet(d.rootMap(), "groups"); ok {
		anchor := keyNode.Line - 2
		if anchor < 0 {
			anchor = 0
		}
		if strings.TrimSpace(d.lines[anchor]) != "" {
			block = append([]string{""}, block...)
		}
		return anchor, block
	}
	return len(d.lines) - 1, append([]string{""}, block...)
}

// groupInsertPos trả chỉ số dòng (0-based) để chèn group mới: cuối danh sách
// groups hiện có.
func (d *yamlDoc) groupInsertPos() int {
	keyNode, seq, ok := mapGet(d.rootMap(), "groups")
	if !ok || seq.Kind != yaml.SequenceNode || len(seq.Content) == 0 {
		if ok {
			return keyNode.Line - 1
		}
		return len(d.lines) - 1
	}
	return lastLine(seq.Content[len(seq.Content)-1]) - 1
}

// newServicePos trả (vị trí chèn 0-based, indent của `- id:`, indent của key)
// cho service mới trong group.
func (d *yamlDoc) newServicePos(g *groupRef) (int, int, int) {
	if g != nil {
		if g.services != nil && g.services.Kind == yaml.SequenceNode && len(g.services.Content) > 0 {
			first := g.services.Content[0]
			dashIndent := maxInt(0, first.Column-3)
			keyIndent := maxInt(0, first.Column-1)
			last := g.services.Content[len(g.services.Content)-1]
			return lastLine(last) - 1, dashIndent, keyIndent
		}
		// Group có key services nhưng rỗng → chèn ngay sau dòng `services:`.
		if k, _, ok := mapGet(g.node, "services"); ok {
			return k.Line - 1, 6, 8
		}
	}
	return d.groupInsertPos(), 6, 8
}

// ---------------------------------------------------------------------------
// Sinh nội dung service
// ---------------------------------------------------------------------------

// renderService sinh các dòng của một service mới theo đúng indent của file.
func (d *yamlDoc) renderService(spec ServiceSpec, dashIndent, keyIndent int) []string {
	dash := strings.Repeat(" ", dashIndent)
	key := strings.Repeat(" ", keyIndent)

	var out []string
	out = append(out, dash+"- id: "+yamlScalar(spec.ID))
	out = append(out, key+"name: "+yamlScalar(spec.Name))
	out = append(out, key+"ports: "+flowPorts(spec.Ports))
	if spec.ComposeService != "" {
		out = append(out, key+"compose_dir: "+yamlScalar(spec.ComposeDir))
		out = append(out, key+"compose_file: "+yamlScalar(spec.ComposeFile))
		out = append(out, key+"compose_service: "+yamlScalar(spec.ComposeService))
		if spec.RepoDir != "" {
			out = append(out, key+"repo_dir: "+yamlScalar(spec.RepoDir))
		}
	} else {
		out = append(out, key+"dir: "+yamlScalar(spec.Dir))
		if spec.RepoDir != "" {
			out = append(out, key+"repo_dir: "+yamlScalar(spec.RepoDir))
		}
		out = append(out, key+"start: "+yamlScalar(spec.Start))
	}
	if spec.AutoRestart {
		out = append(out, key+"auto_restart: true")
	}
	if spec.HealthPath != "" {
		out = append(out, key+"health_path: "+yamlScalar(spec.HealthPath))
	}
	if len(spec.HealthSkipPorts) > 0 {
		out = append(out, key+"health_skip_ports: "+flowPorts(spec.HealthSkipPorts))
	}
	return out
}

// renderGroup sinh khối group mới (kèm service đầu tiên) — dùng khi group
// trong spec chưa tồn tại trong file.
func (d *yamlDoc) renderGroup(spec ServiceSpec) []string {
	dash := strings.Repeat(" ", 2)
	key := strings.Repeat(" ", 4)
	out := []string{
		dash + "- name: " + yamlScalar(spec.Group),
		key + "services:",
	}
	return append(out, d.renderService(spec, 6, 8)...)
}

// renderGroupClip sinh 2 dòng đầu của group mới (name + services:) — dùng khi
// chuyển service sang group chưa có: phần service được mang nguyên bản (đã
// applyServiceEdits + carriedServiceLines), không vẽ lại qua renderGroup.
func renderGroupClip(group string) []string {
	return []string{
		strings.Repeat(" ", 2) + "- name: " + yamlScalar(group),
		strings.Repeat(" ", 4) + "services:",
	}
}

// applyServiceEdits áp dụng spec lên một service đã có trong file: chỉ sửa
// dòng của field thực sự đổi, giữ nguyên phần còn lại.
//
// spec là trạng thái MONG MUỐN của service: field rỗng trong spec → xoá key
// đó khỏi file (vd đổi service compose sang thường thì bỏ compose_*).
func (d *yamlDoc) applyServiceEdits(e *edits, svc *yaml.Node, spec ServiceSpec) {
	isCompose := spec.ComposeService != ""

	e.setString(svc, "name", spec.Name)
	e.setPorts(svc, spec.Ports)
	e.setString(svc, "repo_dir", spec.RepoDir)
	e.setString(svc, "health_path", spec.HealthPath)
	e.setIntList(svc, "health_skip_ports", spec.HealthSkipPorts)

	if isCompose {
		e.setString(svc, "compose_dir", spec.ComposeDir)
		e.setString(svc, "compose_file", spec.ComposeFile)
		e.setString(svc, "compose_service", spec.ComposeService)
		// Service compose không dùng dir/start.
		e.removeKey(svc, "dir")
		e.removeKey(svc, "start")
	} else {
		e.setString(svc, "dir", spec.Dir)
		e.setString(svc, "start", spec.Start)
		// Service thường không dùng compose_*.
		e.removeKey(svc, "compose_dir")
		e.removeKey(svc, "compose_file")
		e.removeKey(svc, "compose_service")
	}

	// auto_restart: có sẵn → sửa dòng; chưa có và bật → thêm dòng cuối khối.
	keyNode, valNode, exists := mapGet(svc, "auto_restart")
	switch {
	case exists:
		if valNode == nil || valNode.Value != boolText(spec.AutoRestart) {
			e.setScalar(keyNode, valNode, boolText(spec.AutoRestart))
		}
	case spec.AutoRestart:
		indent := strings.Repeat(" ", maxInt(0, svc.Column-1))
		e.insertAfter(lastLine(svc)-1, indent+"auto_restart: true")
	}
}

// ---------------------------------------------------------------------------
// Ghi file
// ---------------------------------------------------------------------------

// edits là tập thay đổi trên các dòng của file (chỉ số 0-based).
type edits struct {
	lines   []string
	replace map[int]string   // dòng → nội dung mới
	insert  map[int][]string // chèn SAU dòng này
	remove  map[int]bool     // xoá dòng này
}

func newEdits(lines []string) *edits {
	return &edits{
		lines:   lines,
		replace: map[int]string{},
		insert:  map[int][]string{},
		remove:  map[int]bool{},
	}
}

func (e *edits) insertAfter(idx int, lines ...string) {
	e.insert[idx] = append(e.insert[idx], lines...)
}

func (e *edits) removeRange(from, to int) { // from/to: 1-based, inclusive
	for ln := from; ln <= to; ln++ {
		e.remove[ln-1] = true
	}
}

// removeBlock xoá một khối dòng (1-based, inclusive) KÈM các dòng trống
// ngay sau nó — để xoá group không để lại hai dòng trống liên tiếp.
func (e *edits) removeBlock(from, to int) {
	for ln := from; ln <= to; ln++ {
		e.remove[ln-1] = true
	}
	for ln := to + 1; ln <= len(e.lines); ln++ {
		if strings.TrimSpace(e.lines[ln-1]) != "" {
			break
		}
		e.remove[ln-1] = true
	}
}

// setScalar thay giá trị của một field (node key + node value) bằng text mới,
// giữ nguyên phần comment đuôi dòng và khoảng trắng align trước nó.
func (e *edits) setScalar(keyNode, valNode *yaml.Node, text string) {
	if valNode == nil || keyNode == nil {
		return
	}
	if valNode.Line == keyNode.Line {
		// Giá trị nằm cùng dòng với key → thay đúng đoạn sau `key:`.
		line := e.lineText(valNode.Line)
		start := valNode.Column - 1
		end := valueEnd(line, start)
		e.replace[valNode.Line-1] = line[:start] + text + line[end:]
		return
	}
	// Giá trị nằm dòng khác (ví dụ list block) → thay cả khối dòng.
	first := e.lineText(keyNode.Line)
	idx := strings.Index(first[keyNode.Column-1:], ":")
	if idx < 0 {
		return
	}
	colon := idx + keyNode.Column - 1
	e.replace[keyNode.Line-1] = first[:colon+1] + " " + text + commentSuffix(first[colon+1:])
	for ln := valNode.Line; ln <= lastLine(valNode); ln++ {
		e.remove[ln-1] = true
	}
}

// setString đặt giá trị chuỗi cho một key của service (thêm nếu chưa có).
func (e *edits) setString(svc *yaml.Node, key, value string) {
	keyNode, valNode, exists := mapGet(svc, key)
	if value == "" {
		if exists {
			e.removeKey(svc, key)
		}
		return
	}
	if exists {
		if valNode != nil && valNode.Kind == yaml.ScalarNode && valNode.Value == value {
			return // không đổi → không đụng dòng này
		}
		e.setScalar(keyNode, valNode, yamlScalar(value))
		return
	}
	indent := strings.Repeat(" ", maxInt(0, svc.Column-1))
	e.insertAfter(lastLine(svc)-1, indent+key+": "+yamlScalar(value))
}

// setPorts đặt danh sách port, giữ flow style `ports: [3000, 9090]`.
func (e *edits) setPorts(svc *yaml.Node, ports []int) {
	e.setIntList(svc, "ports", ports)
}

// setIntList đặt danh sách số cho một key bất kỳ của service, giữ flow style
// `[3000, 9090]`. Danh sách rỗng → xoá key (giống setString với chuỗi rỗng).
func (e *edits) setIntList(svc *yaml.Node, key string, vals []int) {
	keyNode, valNode, exists := mapGet(svc, key)
	if len(vals) == 0 {
		if exists {
			e.removeKey(svc, key)
		}
		return
	}
	text := flowPorts(vals)
	if !exists {
		indent := strings.Repeat(" ", maxInt(0, svc.Column-1))
		e.insertAfter(lastLine(svc)-1, indent+key+": "+text)
		return
	}
	if valNode != nil && valNode.Kind == yaml.SequenceNode && sameInts(valNode, vals) {
		return // không đổi
	}
	e.setScalar(keyNode, valNode, text)
}

// removeKey xoá hẳn một key (và dòng của nó) khỏi service.
func (e *edits) removeKey(svc *yaml.Node, key string) {
	keyNode, valNode, exists := mapGet(svc, key)
	if !exists {
		return
	}
	if valNode != nil && valNode.Line != keyNode.Line {
		for ln := valNode.Line; ln <= lastLine(valNode); ln++ {
			e.remove[ln-1] = true
		}
	}
	e.remove[keyNode.Line-1] = true
}

// lineText trả nội dung dòng (1-based).
func (e *edits) lineText(line int) string {
	if line < 1 || line > len(e.lines) {
		return ""
	}
	return e.lines[line-1]
}

// save áp dụng thay đổi, kiểm tra lại bằng Parse rồi ghi file atomically.
func (d *yamlDoc) save(e *edits) error {
	out := make([]string, 0, len(d.lines))
	for i, ln := range d.lines {
		if !e.remove[i] {
			if r, ok := e.replace[i]; ok {
				ln = r
			}
			out = append(out, ln)
		}
		if ins, ok := e.insert[i]; ok {
			out = append(out, ins...)
		}
	}
	// Chèn vượt cuối file (phòng thủ — mọi vị trí chèn hợp lệ đều ≤ len-1).
	if ins, ok := e.insert[len(d.lines)]; ok {
		out = append(out, ins...)
	}
	content := strings.Join(out, "\n") + "\n"

	// Kiểm tra lại trước khi ghi: Parse lỗi (port trùng, thiếu dir/start…) thì
	// KHÔNG ghi file.
	if _, err := Parse([]byte(content)); err != nil {
		return fmt.Errorf("ports.yaml sau khi sửa không hợp lệ: %w", err)
	}
	return writeFileAtomic(d.path, []byte(content), d.mode)
}

// writeFileAtomic ghi file tạm rồi rename — không để lại file hỏng giữa chừng.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	tmp, err := os.CreateTemp(dir, "."+base+".tmp*")
	if err != nil {
		return fmt.Errorf("tạo file tạm trong %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op nếu rename đã thành công
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("ghi file tạm: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("đóng file tạm: %w", err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return fmt.Errorf("đặt quyền file tạm: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("ghi %s: %w", path, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Tiện ích đọc dòng
// ---------------------------------------------------------------------------

// mapGet lấy node key + node value của một key trong mapping.
func mapGet(m *yaml.Node, key string) (*yaml.Node, *yaml.Node, bool) {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil, nil, false
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i], m.Content[i+1], true
		}
	}
	return nil, nil, false
}

// lastLine trả dòng cuối cùng của một node (đệ quy).
func lastLine(n *yaml.Node) int {
	if n == nil {
		return 0
	}
	maxLn := n.Line
	for _, c := range n.Content {
		if l := lastLine(c); l > maxLn {
			maxLn = l
		}
	}
	return maxLn
}

// valueEnd trả vị trí (0-based, exclusive) kết thúc của giá trị yaml bắt đầu
// tại `start` trên dòng — dùng để thay đúng đoạn giá trị, giữ comment đuôi.
func valueEnd(line string, start int) int {
	if start >= len(line) {
		return len(line)
	}
	switch line[start] {
	case '"':
		for i := start + 1; i < len(line); i++ {
			if line[i] == '\\' {
				i++
				continue
			}
			if line[i] == '"' {
				return i + 1
			}
		}
		return len(line)
	case '\'':
		for i := start + 1; i < len(line); i++ {
			if line[i] == '\'' {
				return i + 1
			}
		}
		return len(line)
	case '[', '{':
		depth := 0
		for i := start; i < len(line); i++ {
			switch line[i] {
			case '"':
				for j := i + 1; j < len(line); j++ {
					if line[j] == '\\' {
						j++
						continue
					}
					if line[j] == '"' {
						i = j
						break
					}
				}
			case '[', '{':
				depth++
			case ']', '}':
				depth--
				if depth == 0 {
					return i + 1
				}
			}
		}
		return len(line)
	}
	// Scalar thường: chạy tới comment đuôi dòng (hoặc hết dòng).
	for i := start; i < len(line); i++ {
		if line[i] == '\r' {
			return trimRight(line[:i], start)
		}
		if line[i] == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
			return trimRight(line[:i], start)
		}
	}
	return trimRight(line, start)
}

// trimRight cắt khoảng trắng cuối (giữ nguyên phần từ `start` trở đi).
func trimRight(s string, start int) int {
	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return end
}

// commentSuffix lấy phần comment đuôi dòng (từ `#` trở đi) của một đoạn.
func commentSuffix(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '#' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t') {
			return s[i:]
		}
	}
	return ""
}

// yamlScalar render giá trị chuỗi: chỉ quote khi cần (chứa ':' hoặc bắt đầu
// bằng ký tự đặc biệt YAML) — giữ file đọc được như người dùng tự viết.
func yamlScalar(s string) string {
	if s == "" {
		return `""`
	}
	if needsQuote(s) {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
	}
	return s
}

func needsQuote(s string) bool {
	if s != strings.TrimSpace(s) {
		return true
	}
	if strings.Contains(s, ":") || strings.Contains(s, "#") {
		return true
	}
	switch s[0] {
	case '-', '?', ':', ',', '[', ']', '{', '}', '#', '&', '*', '!', '|', '>', '\'', '"', '%', '@', '`':
		return true
	}
	return false
}

// flowPorts render danh sách port dạng flow: `[3000, 9090]` — giữ style của
// ports.yaml (không bung list dọc).
func flowPorts(ports []int) string {
	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		parts = append(parts, strconv.Itoa(p))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// sameInts so sánh node sequence với danh sách port.
func sameInts(seq *yaml.Node, ports []int) bool {
	if seq == nil || seq.Kind != yaml.SequenceNode || len(seq.Content) != len(ports) {
		return false
	}
	for i, item := range seq.Content {
		if item.Value != strconv.Itoa(ports[i]) {
			return false
		}
	}
	return true
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------
// Gợi ý cổng trống
// ---------------------------------------------------------------------------

// SuggestFreePortFrom gợi ý một cổng đang trống từ DANH SÁCH ỨNG VIÊN cho
// trước.
//
// LƯU Ý: nguồn chuẩn của ứng viên là bảng quy hoạch port ở AGENTS.md mục 10.1 —
// internal/mirror.SuggestPort đọc bảng đó và NÊN được dùng cho binding
// `SuggestPort()` của app, vì AGENTS.md mục 10.2 cấm chạy server ở port ngoài
// bảng quy hoạch. Hàm này là phương án dự phòng cho trường hợp không có bảng
// quy hoạch: chọn ứng viên đầu tiên còn trống.
//
// used: các cổng đã bị chiếm (đã khai trong ports.yaml hoặc đang lắng nghe).
// Trả 0 nếu không còn ứng viên nào trống.
func SuggestFreePortFrom(candidates []int, used map[int]bool) int {
	for _, p := range candidates {
		if p <= 0 || p > 65535 {
			continue
		}
		if used[p] {
			continue
		}
		if portInUse(p) {
			continue
		}
		return p
	}
	return 0
}

// SuggestFreePort gợi ý cổng đang trống khi không có danh sách ứng viên từ
// bảng quy hoạch: quét từ cổng dev thấp lên, loại các cổng đã khai trong cfg
// và các cổng đang có process lắng nghe.
//
// extraUsed: cổng đang chiếm ngoài ports.yaml (vd port đang listen).
//
// LƯU Ý: port trả về KHÔNG chắc nằm trong bảng quy hoạch AGENTS.md mục 10.1 —
// dùng cho binding `SuggestPort()` thì nên gọi internal/mirror.SuggestPort để
// không vi phạm AGENTS.md mục 10.2.
func SuggestFreePort(cfg *Config, extraUsed map[int]bool) int {
	used := map[int]bool{}
	if cfg != nil {
		for p := range cfg.ConfiguredPorts() {
			used[p] = true
		}
	}
	for p := range extraUsed {
		used[p] = true
	}
	for p := 3000; p <= 65535; p++ {
		if used[p] || portInUse(p) {
			continue
		}
		return p
	}
	return 0
}

// portInUse kiểm tra cổng đang có gì lắng nghe trên localhost.
func portInUse(port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 150*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
