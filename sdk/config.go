package mesh

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Config 代表 GopherMesh 全局配置（支持无头静默运行）
type Config struct {
	DashboardPort  string                    `json:"dashboard_port"`
	TrustedOrigins []string                  `json:"trusted_origins"`
	ServiceName    string                    `json:"service_name,omitempty"`
	InternalPort   string                    `json:"internal_port,omitempty"`
	Endpoints      map[string]EndpointConfig `json:"endpoints"`
}

// EndpointConfig 定义了单个本地计算断点的静默调度规则
type EndpointConfig struct {
	Name         string   `json:"name"`
	Cmd          string   `json:"cmd"`                // 执行的二进制目标，为空或"internal"表示内部路由
	Args         []string `json:"args,omitempty"`     // 启动参数
	InternalPort string   `json:"internal_port"`      // 目标进程实际监听的本地端口
	Protocol     string   `json:"protocol,omitempty"` // 流量类型：默认为空(http)，可配置为"tcp"开启L4零拷贝对拷
}

const (
	defaultDashboardPort = "9999"
)

// DefaultConfig 生成包含防环路机制与默认放行白名单的配置
func DefaultConfig() Config {
	return Config{
		DashboardPort: defaultDashboardPort,
		// 默认允许所有的跨域请求以保证开发体验
		// 部署到暴露环境时，应修改为类似 ["https://your-host.com"]
		TrustedOrigins: []string{"*"},
		Endpoints: map[string]EndpointConfig{
			"8081": {
				Name:         "Internal-Healthcheck",
				Cmd:          "internal",
				InternalPort: defaultDashboardPort,
			},
		},
	}
}

// LoadConfig 从指定路径加载配置，若文件不存在则初始化默认配置并落盘
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := DefaultConfig()
			if writeErr := SaveConfig(path, cfg); writeErr != nil {
				return cfg, fmt.Errorf("failed to save config: %v", writeErr)
			}
			return cfg, nil
		}
		return Config{}, fmt.Errorf("failed to read config: %v", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("failed to parse config: %v", err)
	}

	return cfg.Normalize()
}

// SaveConfig 将配置序列化并安全写入磁盘
func SaveConfig(path string, cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// Normalize 校验用户配置的合法性，并安全补全缺失的默认值
func (c Config) Normalize() (Config, error) {
	if strings.TrimSpace(c.DashboardPort) == "" {
		c.DashboardPort = defaultDashboardPort
	}
	if len(c.TrustedOrigins) == 0 {
		c.TrustedOrigins = []string{"*"}
	}
	if len(c.Endpoints) == 0 {
		c.Endpoints = DefaultConfig().Endpoints
	}

	normalized := make(map[string]EndpointConfig, len(c.Endpoints))
	for port, ep := range c.Endpoints {
		p := strings.TrimSpace(port)
		if p == "" {
			return Config{}, fmt.Errorf("invalid blank port")
		}

		ep.Cmd = strings.TrimSpace(ep.Cmd)
		isInternal := strings.ToLower(ep.Cmd) == "" || strings.ToLower(ep.Cmd) == "internal"

		if isInternal {
			if strings.TrimSpace(ep.InternalPort) == "" {
				ep.InternalPort = c.DashboardPort
			}
		} else {
			if strings.TrimSpace(ep.InternalPort) == "" {
				return Config{}, fmt.Errorf("invalid internal port %q", p)
			}
			ep.Protocol = strings.ToLower(strings.TrimSpace(ep.Protocol))
			if ep.Protocol != "tcp" {
				ep.Protocol = "http"
			}
		}
		normalized[p] = ep
	}
	c.Endpoints = normalized
	return c, nil
}
