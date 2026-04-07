package mesh

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config 代表 GopherMesh 全局配置（支持无头静默运行）
type Config struct {
	ConfigPath     string                 `json:"-"`
	DashboardHost  string                 `json:"dashboard_host"`
	DashboardPort  string                 `json:"dashboard_port"`
	TrustedOrigins []string               `json:"trusted_origins"`
	ServiceName    string                 `json:"service_name,omitempty"`
	InternalPort   string                 `json:"internal_port,omitempty"`
	Routes         map[string]RouteConfig `json:"routes,omitempty"`
}

// RouteConfig 定义单个对外暴露端口的路由规则。
type RouteConfig struct {
	Name        string          `json:"name"`
	Protocol    string          `json:"protocol,omitempty"`     // 默认为 http，可配置为 tcp / stdio
	StdioMode   string          `json:"stdio_mode,omitempty"`   // 仅 stdio 路由生效：stream / http / auto
	LoadBalance string          `json:"load_balance,omitempty"` // 支持 round_robin / least_conn / ip_hash
	Backends    []BackendConfig `json:"backends,omitempty"`
}

// BackendConfig 定义路由下单个实际承载请求的后端。
type BackendConfig struct {
	Name         string   `json:"name,omitempty"`
	Cmd          string   `json:"cmd"`                     // 执行的二进制目标，为空或"internal"表示内部路由
	Args         []string `json:"args,omitempty"`          // 启动参数
	InternalHost string   `json:"internal_host,omitempty"` // 目标主机IP/域名
	InternalPort string   `json:"internal_port,omitempty"` // 目标进程实际监听的本地端口；stdio 路由下留空
}

func (c Config) clone() Config {
	cloned := c

	if len(c.TrustedOrigins) > 0 {
		cloned.TrustedOrigins = append([]string(nil), c.TrustedOrigins...)
	}

	if len(c.Routes) > 0 {
		cloned.Routes = make(map[string]RouteConfig, len(c.Routes))
		for port, route := range c.Routes {
			cloned.Routes[port] = route.clone()
		}
	}

	return cloned
}

func (r RouteConfig) clone() RouteConfig {
	cloned := r
	if len(r.Backends) > 0 {
		cloned.Backends = make([]BackendConfig, len(r.Backends))
		for i, backend := range r.Backends {
			cloned.Backends[i] = backend.clone()
		}
	}
	return cloned
}

func (b BackendConfig) clone() BackendConfig {
	cloned := b
	if len(b.Args) > 0 {
		cloned.Args = append([]string(nil), b.Args...)
	}
	return cloned
}

const (
	defaultLocalHost     = "127.0.0.1"
	defaultDashboardPort = "9999"
	defaultLoadBalance   = "round_robin"
	loadBalanceLeastConn = "least_conn"
	loadBalanceIPHash    = "ip_hash"
	protocolHTTP         = "http"
	protocolTCP          = "tcp"
	protocolSTDIO        = "stdio"
	stdioModeStream      = "stream"
	stdioModeHTTP        = "http"
	stdioModeAuto        = "auto"
)

func isInternalCommand(cmd string) bool {
	return strings.EqualFold(strings.TrimSpace(cmd), "internal")
}

func isInternalRoute(route RouteConfig) bool {
	return len(route.Backends) == 1 && isInternalCommand(route.Backends[0].Cmd)
}

func routeUsesRawTCPListener(route RouteConfig) bool {
	return route.Protocol == protocolTCP || route.Protocol == protocolSTDIO
}

// DefaultConfig 生成包含防环路机制与默认放行白名单的配置
func DefaultConfig() Config {
	return Config{
		DashboardHost: defaultLocalHost,
		DashboardPort: defaultDashboardPort,
		// 默认允许所有的跨域请求以保证开发体验
		// 部署到暴露环境时，应修改为类似 ["https://your-host.com"]
		TrustedOrigins: []string{"*"},
		Routes: map[string]RouteConfig{
			"8081": {
				Name:     "Internal-Healthcheck",
				Protocol: "http",
				Backends: []BackendConfig{
					{
						Name:         "dashboard",
						Cmd:          "internal",
						InternalPort: defaultDashboardPort,
					},
				},
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
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
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
	return writeFileAtomically(path, data, 0644)
}

func writeFileAtomically(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}

	tmpPath := file.Name()
	cleanup := func() {
		_ = os.Remove(tmpPath)
	}

	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		cleanup()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		cleanup()
		return err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("replace config file: %w", err)
	}
	if err := os.Chmod(path, perm); err != nil {
		return fmt.Errorf("chmod config file: %w", err)
	}
	return nil
}

// Normalize 校验用户配置的合法性，并安全补全缺失的默认值
func (c Config) Normalize() (Config, error) {
	if strings.TrimSpace(c.DashboardHost) == "" {
		c.DashboardHost = defaultLocalHost
	}
	if strings.TrimSpace(c.DashboardPort) == "" {
		c.DashboardPort = defaultDashboardPort
	}
	if len(c.TrustedOrigins) == 0 {
		c.TrustedOrigins = []string{"*"}
	}

	routes := c.Routes
	if len(routes) == 0 {
		routes = DefaultConfig().Routes
	}

	normalized := make(map[string]RouteConfig, len(routes))
	seenBackendPorts := make(map[string]string)

	for port, route := range routes {
		publicPort := strings.TrimSpace(port)
		if publicPort == "" {
			return Config{}, fmt.Errorf("invalid blank port")
		}

		route.Name = strings.TrimSpace(route.Name)
		if route.Name == "" {
			route.Name = "Route-" + publicPort
		}

		route.Protocol = normalizeProtocol(route.Protocol)
		route.StdioMode = normalizeSTDIOBridgeMode(route.Protocol, route.StdioMode)
		route.LoadBalance = normalizeLoadBalance(route.LoadBalance)

		if len(route.Backends) == 0 {
			return Config{}, fmt.Errorf("route %q has no backends", publicPort)
		}

		normalizedBackends := make([]BackendConfig, 0, len(route.Backends))
		internalCount := 0

		for index, backend := range route.Backends {
			backend.Name = strings.TrimSpace(backend.Name)
			backend.Cmd = strings.TrimSpace(backend.Cmd)

			if route.Protocol == protocolSTDIO {
				if backend.Cmd == "" {
					return Config{}, fmt.Errorf("route %q backend %d requires cmd for stdio protocol", publicPort, index+1)
				}
				if isInternalCommand(backend.Cmd) {
					return Config{}, fmt.Errorf("route %q backend %d cannot use internal backend with stdio protocol", publicPort, index+1)
				}

				backend.InternalHost = ""
				backend.InternalPort = ""

				if backend.Name == "" {
					backend.Name = fmt.Sprintf("%s-%d", route.Name, index+1)
				}
				normalizedBackends = append(normalizedBackends, backend)
				continue
			}

			// 如果未配置 Host 默认兜底为本机
			if strings.TrimSpace(backend.InternalHost) == "" {
				backend.InternalHost = defaultLocalHost
			}

			if isInternalCommand(backend.Cmd) {
				internalCount++
				backend.Cmd = "internal"
				if strings.TrimSpace(backend.InternalPort) == "" {
					backend.InternalPort = c.DashboardPort
				}
			} else {
				if strings.TrimSpace(backend.InternalPort) == "" {
					return Config{}, fmt.Errorf("invalid internal port %q backend %d", publicPort, index+1)
				}
				if previousRoute, exists := seenBackendPorts[backend.InternalPort]; exists {
					return Config{}, fmt.Errorf("duplicate internal port %q used by routes %q and %q", backend.InternalPort, previousRoute, publicPort)
				}
				seenBackendPorts[backend.InternalPort] = publicPort
			}

			if backend.Name == "" {
				backend.Name = fmt.Sprintf("%s-%d", route.Name, index+1)
			}
			normalizedBackends = append(normalizedBackends, backend)
		}

		if internalCount > 0 {
			if internalCount != len(normalizedBackends) {
				return Config{}, fmt.Errorf("route %q mixes internal and external backends", publicPort)
			}
			if len(normalizedBackends) != 1 {
				return Config{}, fmt.Errorf("route %q can only have one internal backend", publicPort)
			}
		}

		route.Backends = normalizedBackends
		normalized[publicPort] = route
	}

	c.Routes = normalized
	return c, nil
}

func normalizeProtocol(protocol string) string {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	switch protocol {
	case protocolTCP:
		return protocolTCP
	case protocolSTDIO:
		return protocolSTDIO
	}
	return protocolHTTP
}

func normalizeSTDIOBridgeMode(protocol, mode string) string {
	if normalizeProtocol(protocol) != protocolSTDIO {
		return ""
	}

	switch strings.ToLower(strings.TrimSpace(mode)) {
	case stdioModeHTTP:
		return stdioModeHTTP
	case stdioModeAuto:
		return stdioModeAuto
	case "", stdioModeStream:
		return stdioModeStream
	}
	return stdioModeStream
}

func normalizeLoadBalance(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case defaultLoadBalance, loadBalanceLeastConn, loadBalanceIPHash:
		return mode
	case "":
		return defaultLoadBalance
	}
	return defaultLoadBalance
}
