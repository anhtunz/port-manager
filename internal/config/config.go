package config

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Service mô tả một service dev trong workspace: hoặc chạy bằng lệnh shell
// (service thường) hoặc điều khiển qua docker compose (hạ tầng).
type Service struct {
	ID    string `yaml:"id" json:"id"`
	Name  string `yaml:"name" json:"name"`
	Ports []int  `yaml:"ports" json:"ports"`

	// Service thường
	Dir   string `yaml:"dir"`   // tương đối workspace_root
	Start string `yaml:"start"` // lệnh shell chạy trong Dir

	// RepoDir: git repo của service — chỉ cần khi Dir KHÔNG phải repo/với repo
	// khác dir (vd wm-be dir=warehouse-management nhưng repo là backend/).
	// Rỗng → dùng Dir (service thường) hoặc bỏ qua (compose).
	RepoDir string `yaml:"repo_dir"`

	// Hạ tầng docker
	ComposeDir     string `yaml:"compose_dir"`
	ComposeFile    string `yaml:"compose_file"`
	ComposeService string `yaml:"compose_service"`

	// AutoRestart: service này được supervisor tự bật lại khi process chết
	// ngoài ý muốn (F3.1). Mặc định false — không tự khởi động lại.
	AutoRestart bool `yaml:"auto_restart" json:"autoRestart"`

	// HealthPath: path dùng để probe HTTP (F2.2). Rỗng → internal/health tự
	// hiểu là "/". Nếu có, phải bắt đầu bằng "/" (Parse kiểm tra).
	HealthPath string `yaml:"health_path" json:"healthPath,omitempty"`

	// HealthSkipPorts: port không probe (vd cổng gRPC) để không báo
	// http_error giả.
	HealthSkipPorts []int `yaml:"health_skip_ports" json:"healthSkipPorts,omitempty"`
}

func (s Service) IsCompose() bool { return s.ComposeService != "" }

type Group struct {
	Name string `yaml:"name" json:"name"`

	// Tier: thứ tự khởi động — 0 = hạ tầng, 1 = Backend Go, 2 = Frontend.
	// KHÔNG bắt buộc: group thiếu tier → tier = -1 (xếp sau các group có tier,
	// giữ thứ tự file trong nhóm đó). Xem GroupsInStartupOrder.
	Tier int `yaml:"tier" json:"tier,omitempty"`

	Services []Service `yaml:"services" json:"services"`
}

// UnmarshalYAML đọc group nhưng cho Tier mặc định -1 khi file không khai báo
// `tier` — file ports.yaml cũ (chưa có tier) vẫn phải đọc được, không được
// chặn app khởi động.
func (g *Group) UnmarshalYAML(value *yaml.Node) error {
	// Dùng con trỏ cho tier để phân biệt "thiếu key" với "tier: -1".
	var raw struct {
		Name     string    `yaml:"name"`
		Tier     *int      `yaml:"tier"`
		Services []Service `yaml:"services"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	g.Name = raw.Name
	g.Services = raw.Services
	g.Tier = -1
	if raw.Tier != nil {
		g.Tier = *raw.Tier
	}
	return nil
}

type Config struct {
	// WorkspaceRoot: gốc workspace smatec — đường dẫn service/compose tính từ đây.
	// Tương đối theo thư mục chứa file config.
	WorkspaceRoot string `yaml:"workspace_root"`

	// Lọc noise ở mục "Cổng lạ": theo tên process (khớp đầu, không phân biệt hoa thường)
	// và theo port (vd 6790 — utoopack mở nội bộ cạnh WM FE 6789).
	UnknownIgnoreCommands []string `yaml:"unknown_ignore_commands"`
	UnknownIgnorePorts    []int    `yaml:"unknown_ignore_ports"`

	Groups []Group `yaml:"groups" json:"groups"`
}

// Load đọc file config; trả thêm thư mục chứa file để resolve đường dẫn tương đối.
func Load(path string) (*Config, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, "", err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, "", err
	}
	return cfg, filepath.Dir(absPath), nil
}

// Parse parse yaml + validate những lỗi config phải bắt ngay từ đầu
// (thiếu trường, trùng port) thay vì để app chạy ngầm lệch.
func Parse(data []byte) (*Config, error) {
	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	if len(cfg.Groups) == 0 {
		return nil, fmt.Errorf("config trống — không có group nào")
	}
	seen := map[int]string{}
	for gi := range cfg.Groups {
		for si := range cfg.Groups[gi].Services {
			s := &cfg.Groups[gi].Services[si]
			if s.ID == "" {
				return nil, fmt.Errorf("service thiếu id (group %s)", cfg.Groups[gi].Name)
			}
			if len(s.Ports) == 0 {
				return nil, fmt.Errorf("service %s thiếu ports", s.ID)
			}
			if s.HealthPath != "" && !strings.HasPrefix(s.HealthPath, "/") {
				return nil, fmt.Errorf("service %s có health_path %q — phải bắt đầu bằng \"/\"", s.ID, s.HealthPath)
			}
			if s.IsCompose() {
				if s.ComposeDir == "" || s.ComposeFile == "" {
					return nil, fmt.Errorf("service compose %s thiếu compose_dir/compose_file", s.ID)
				}
			} else if s.Dir == "" || s.Start == "" {
				return nil, fmt.Errorf("service %s thiếu dir/start", s.ID)
			}
			for _, p := range s.Ports {
				if prev, dup := seen[p]; dup {
					return nil, fmt.Errorf("port %d khai báo trùng: %s và %s", p, prev, s.ID)
				}
				seen[p] = s.ID
			}
		}
	}
	return cfg, nil
}

// GroupsInStartupOrder trả group theo thứ tự khởi động: sort theo tier,
// tie → thứ tự group trong file. Group không khai báo tier có tier = -1 →
// xếp sau các group có tier, nhưng giữ thứ tự file trong nhóm đó.
//
// File cũ không có tier → mọi group tier -1 → thứ tự giữ nguyên như file.
func (c *Config) GroupsInStartupOrder() []Group {
	out := make([]Group, len(c.Groups))
	copy(out, c.Groups)
	sort.SliceStable(out, func(i, j int) bool {
		return tierRank(out[i].Tier) < tierRank(out[j].Tier)
	})
	return out
}

// tierRank: tier -1 (không khai báo) → xếp CUỐI, sau mọi tier >= 0.
func tierRank(tier int) int {
	if tier < 0 {
		return math.MaxInt
	}
	return tier
}

// ResolveRoot trả workspace_root tuyệt đối. Nếu đường dẫn mặc định không hợp lệ
// (app .app mở từ chỗ khác, cwd="/") thì thử fallback $HOME/Documents/smatec —
// vị trí chuẩn của workspace trên máy dev.
func (c *Config) ResolveRoot(cfgDir string) string {
	root := c.WorkspaceRoot
	if root == "" {
		root = "."
	}
	if !filepath.IsAbs(root) {
		root = filepath.Join(cfgDir, root)
	}
	if fileExists(filepath.Join(root, "AGENTS.md")) {
		return root
	}
	if home, err := os.UserHomeDir(); err == nil {
		alt := filepath.Join(home, "Documents", "smatec")
		if fileExists(filepath.Join(alt, "AGENTS.md")) {
			return alt
		}
	}
	return root
}

// ResolveDir gắn đường dẫn tương đối của service vào workspace_root.
func (c *Config) ResolveDir(root, rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(root, rel)
}

func (c *Config) Service(id string) (Service, bool) {
	for _, g := range c.Groups {
		for _, s := range g.Services {
			if s.ID == id {
				return s, true
			}
		}
	}
	return Service{}, false
}

func (c *Config) ConfiguredPorts() map[int]bool {
	m := map[int]bool{}
	for _, g := range c.Groups {
		for _, s := range g.Services {
			for _, p := range s.Ports {
				m[p] = true
			}
		}
	}
	return m
}

func (c *Config) IsIgnoredUnknown(command string, port int) bool {
	for _, p := range c.UnknownIgnorePorts {
		if p == port {
			return true
		}
	}
	cmd := strings.ToLower(command)
	for _, ig := range c.UnknownIgnoreCommands {
		if strings.HasPrefix(cmd, strings.ToLower(ig)) {
			return true
		}
	}
	return false
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
