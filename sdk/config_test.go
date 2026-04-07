package mesh

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigCreatesDefaultWhenMissing(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.json")

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	if cfg.DashboardPort != defaultDashboardPort {
		t.Fatalf("DashboardPort = %q, want %q", cfg.DashboardPort, defaultDashboardPort)
	}

	internal, ok := cfg.Routes["8081"]
	if !ok {
		t.Fatalf("default internal route missing")
	}
	if !isInternalRoute(internal) {
		t.Fatalf("default route should be internal: %#v", internal)
	}
	if internal.Backends[0].Cmd != "internal" {
		t.Fatalf("internal backend cmd = %q, want %q", internal.Backends[0].Cmd, "internal")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("saved config file is empty")
	}
}

func TestLoadConfigRejectsDeprecatedEndpointsSchema(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{
  "dashboard_port": "9999",
  "endpoints": {
    "8081": {
      "name": "legacy",
      "cmd": "internal",
      "internal_port": "9999"
    }
  }
}`), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := LoadConfig(path); err == nil {
		t.Fatalf("LoadConfig() error = nil, want deprecated endpoints schema to be rejected")
	}
}

func TestSaveConfigReplacesExistingFileWithoutLeavingTempFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	initial := Config{
		DashboardPort: "9999",
		Routes: map[string]RouteConfig{
			"8081": {
				Name: "old-route",
				Backends: []BackendConfig{
					{Cmd: "worker", InternalPort: "9081"},
				},
			},
		},
	}
	reloaded := Config{
		DashboardPort: "9999",
		Routes: map[string]RouteConfig{
			"8082": {
				Name: "new-route",
				Backends: []BackendConfig{
					{Cmd: "worker", InternalPort: "9082"},
				},
			},
		},
	}

	if err := SaveConfig(path, initial); err != nil {
		t.Fatalf("SaveConfig(initial) error = %v", err)
	}
	if err := SaveConfig(path, reloaded); err != nil {
		t.Fatalf("SaveConfig(reloaded) error = %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig(%q) error = %v", path, err)
	}
	if _, ok := cfg.Routes["8082"]; !ok {
		t.Fatalf("LoadConfig(%q) missing replaced route: %#v", path, cfg.Routes)
	}
	if _, ok := cfg.Routes["8081"]; ok {
		t.Fatalf("LoadConfig(%q) still contains stale route: %#v", path, cfg.Routes)
	}

	matches, err := filepath.Glob(filepath.Join(dir, "config.json.tmp-*"))
	if err != nil {
		t.Fatalf("Glob(temp files) error = %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("SaveConfig() left temp files behind: %#v", matches)
	}
}

func TestConfigNormalizeCanonicalizesRoutesAndBackends(t *testing.T) {
	t.Parallel()

	cfg, err := (Config{
		TrustedOrigins: nil,
		Routes: map[string]RouteConfig{
			" 8088 ": {
				Name: "Internal Route",
				Backends: []BackendConfig{
					{Cmd: " Internal "},
				},
			},
			"8089": {
				Name:        "TCP Worker",
				Protocol:    " TCP ",
				LoadBalance: "unsupported",
				Backends: []BackendConfig{
					{
						Cmd:          " go ",
						InternalPort: "9089",
					},
				},
			},
			"8090": {
				Name: "Fallback HTTP Worker",
				Backends: []BackendConfig{
					{
						Cmd:          "python",
						InternalPort: "9090",
					},
				},
			},
		},
	}).Normalize()
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}

	if cfg.DashboardPort != defaultDashboardPort {
		t.Fatalf("DashboardPort = %q, want %q", cfg.DashboardPort, defaultDashboardPort)
	}
	if len(cfg.TrustedOrigins) != 1 || cfg.TrustedOrigins[0] != "*" {
		t.Fatalf("TrustedOrigins = %#v, want [*]", cfg.TrustedOrigins)
	}

	internal := cfg.Routes["8088"]
	if !isInternalRoute(internal) {
		t.Fatalf("route 8088 should be internal: %#v", internal)
	}
	if internal.Backends[0].InternalPort != defaultDashboardPort {
		t.Fatalf("internal backend InternalPort = %q, want %q", internal.Backends[0].InternalPort, defaultDashboardPort)
	}

	tcp := cfg.Routes["8089"]
	if tcp.Protocol != "tcp" {
		t.Fatalf("tcp protocol = %q, want %q", tcp.Protocol, "tcp")
	}
	if tcp.LoadBalance != defaultLoadBalance {
		t.Fatalf("tcp load balance = %q, want %q", tcp.LoadBalance, defaultLoadBalance)
	}
	if tcp.Backends[0].Cmd != "go" {
		t.Fatalf("tcp backend cmd = %q, want %q", tcp.Backends[0].Cmd, "go")
	}
	if tcp.Backends[0].Name != "TCP Worker-1" {
		t.Fatalf("tcp backend name = %q, want %q", tcp.Backends[0].Name, "TCP Worker-1")
	}

	http := cfg.Routes["8090"]
	if http.Protocol != "http" {
		t.Fatalf("fallback protocol = %q, want %q", http.Protocol, "http")
	}
	if http.LoadBalance != defaultLoadBalance {
		t.Fatalf("fallback load balance = %q, want %q", http.LoadBalance, defaultLoadBalance)
	}
}

func TestConfigNormalizeKeepsSupportedLoadBalanceStrategies(t *testing.T) {
	t.Parallel()

	cfg, err := (Config{
		Routes: map[string]RouteConfig{
			"8081": {
				Name:        "Least Conn",
				LoadBalance: " least_conn ",
				Backends: []BackendConfig{
					{Cmd: "worker", InternalPort: "9081"},
				},
			},
			"8082": {
				Name:        "IP Hash",
				LoadBalance: " IP_HASH ",
				Backends: []BackendConfig{
					{Cmd: "worker", InternalPort: "9082"},
				},
			},
		},
	}).Normalize()
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}

	if got := cfg.Routes["8081"].LoadBalance; got != loadBalanceLeastConn {
		t.Fatalf("route 8081 load balance = %q, want %q", got, loadBalanceLeastConn)
	}
	if got := cfg.Routes["8082"].LoadBalance; got != loadBalanceIPHash {
		t.Fatalf("route 8082 load balance = %q, want %q", got, loadBalanceIPHash)
	}
}

func TestConfigNormalizeAllowsSTDIOBackendsWithoutInternalAddress(t *testing.T) {
	t.Parallel()

	cfg, err := (Config{
		Routes: map[string]RouteConfig{
			"17083": {
				Name:     "STDIO Echo",
				Protocol: " STDIO ",
				Backends: []BackendConfig{
					{
						Cmd:  " go ",
						Args: []string{"run", "./sample/stdio/echo"},
					},
				},
			},
		},
	}).Normalize()
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}

	route := cfg.Routes["17083"]
	if route.Protocol != protocolSTDIO {
		t.Fatalf("route protocol = %q, want %q", route.Protocol, protocolSTDIO)
	}
	if route.StdioMode != stdioModeStream {
		t.Fatalf("route stdio mode = %q, want %q", route.StdioMode, stdioModeStream)
	}
	if got := route.Backends[0].InternalHost; got != "" {
		t.Fatalf("stdio backend InternalHost = %q, want blank", got)
	}
	if got := route.Backends[0].InternalPort; got != "" {
		t.Fatalf("stdio backend InternalPort = %q, want blank", got)
	}
}

func TestConfigNormalizeRejectsSTDIOBackendWithoutCommand(t *testing.T) {
	t.Parallel()

	_, err := (Config{
		Routes: map[string]RouteConfig{
			"17083": {
				Name:     "Broken STDIO",
				Protocol: protocolSTDIO,
				Backends: []BackendConfig{
					{},
				},
			},
		},
	}).Normalize()
	if err == nil {
		t.Fatalf("Normalize() error = nil, want missing cmd error for stdio backend")
	}
}

func TestConfigNormalizeKeepsExplicitSTDIOHTTPMode(t *testing.T) {
	t.Parallel()

	cfg, err := (Config{
		Routes: map[string]RouteConfig{
			"17084": {
				Name:      "STDIO HTTP",
				Protocol:  protocolSTDIO,
				StdioMode: " HTTP ",
				Backends: []BackendConfig{
					{
						Cmd: "go",
					},
				},
			},
		},
	}).Normalize()
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}

	if got := cfg.Routes["17084"].StdioMode; got != stdioModeHTTP {
		t.Fatalf("route stdio mode = %q, want %q", got, stdioModeHTTP)
	}
}

func TestConfigNormalizeRejectsBlankPort(t *testing.T) {
	t.Parallel()

	_, err := (Config{
		Routes: map[string]RouteConfig{
			"   ": {
				Name: "broken",
				Backends: []BackendConfig{
					{
						Cmd: "internal",
					},
				},
			},
		},
	}).Normalize()
	if err == nil {
		t.Fatalf("Normalize() error = nil, want invalid blank port error")
	}
}

func TestConfigNormalizeRejectsRouteWithoutBackends(t *testing.T) {
	t.Parallel()

	_, err := (Config{
		Routes: map[string]RouteConfig{
			"8081": {
				Name: "missing-backends",
			},
		},
	}).Normalize()
	if err == nil {
		t.Fatalf("Normalize() error = nil, want missing backends error")
	}
}

func TestConfigNormalizeRejectsMixedInternalAndExternalBackends(t *testing.T) {
	t.Parallel()

	_, err := (Config{
		Routes: map[string]RouteConfig{
			"8081": {
				Name: "invalid-mixed",
				Backends: []BackendConfig{
					{Cmd: "internal"},
					{Cmd: "go", InternalPort: "19081"},
				},
			},
		},
	}).Normalize()
	if err == nil {
		t.Fatalf("Normalize() error = nil, want mixed internal/external backend error")
	}
}

func TestSampleConfigParsesAsRoutesAndBackends(t *testing.T) {
	t.Parallel()

	cfg, err := LoadConfig(filepath.Join("..", "sample", "sample_config.json"))
	if err != nil {
		t.Fatalf("LoadConfig(sample_config.json) error = %v", err)
	}

	if len(cfg.Routes) != 7 {
		t.Fatalf("sample routes len = %d, want %d", len(cfg.Routes), 7)
	}
	if got := len(cfg.Routes["18081"].Backends); got != 2 {
		t.Fatalf("sample route 18081 backends len = %d, want %d", got, 2)
	}
	if got := cfg.Routes["17081"].Protocol; got != "tcp" {
		t.Fatalf("sample route 17081 protocol = %q, want %q", got, "tcp")
	}
	if got := cfg.Routes["17083"].Protocol; got != protocolSTDIO {
		t.Fatalf("sample route 17083 protocol = %q, want %q", got, protocolSTDIO)
	}
	if got := cfg.Routes["17083"].StdioMode; got != stdioModeStream {
		t.Fatalf("sample route 17083 stdio mode = %q, want %q", got, stdioModeStream)
	}
	if got := cfg.Routes["17083"].Backends[0].InternalPort; got != "" {
		t.Fatalf("sample route 17083 internal port = %q, want blank", got)
	}
	if got := cfg.Routes["17084"].StdioMode; got != stdioModeHTTP {
		t.Fatalf("sample route 17084 stdio mode = %q, want %q", got, stdioModeHTTP)
	}
}
