package dashboard

import (
	_ "embed"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
)

//go:embed dashboard.html
var uiTemplate []byte

// BackendStatus 表示单个后端实例的运行时状态
type BackendStatus struct {
	Ref          string `json:"ref,omitempty"`
	Name         string `json:"name"`
	InternalHost string `json:"internalHost"`
	InternalPort string `json:"internalPort"`
	Status       string `json:"status"` // Dormant 休眠 / Running 运行 / Request-Driven 按请求拉起
	PID          int    `json:"pid,omitempty"`
	Uptime       string `json:"uptime,omitempty"`
}

// RouteStatus 表示单个路由及其下游后端的状态
type RouteStatus struct {
	Name        string          `json:"name"`
	Protocol    string          `json:"protocol"`
	StdioMode   string          `json:"stdioMode,omitempty"`
	LoadBalance string          `json:"loadBalance"`
	Backends    []BackendStatus `json:"backends"`
}

// MeshState 控制台向主引擎索取数据的契约
type MeshState interface {
	GetStatus() map[string]RouteStatus
	GetLogs(ref string) []string // ref 既可为 internalPort，也可为 backendRef
	KillProcess(ref string) error
	GetConfigJSON() []byte
	ReloadConfig(rawJSON []byte) error
}

// Serve 启动无头控制台的 API 服务
func Serve(ln net.Listener, state MeshState) error {
	mux := http.NewServeMux()

	// 挂载内嵌UI面板
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(uiTemplate)
	})

	// 中间件逻辑：设置通用的 JSON 和 CORS
	setHeaders := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
	}

	// 透视 API
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		setHeaders(w)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 200,
			"data": state.GetStatus(),
		})
	})

	// 黑盒日志
	mux.HandleFunc("/api/logs/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		// 极简路由解析，提取形如 /api/logs/9081 或 /api/logs/17083:0 中的 backend ref
		ref := strings.TrimPrefix(r.URL.Path, "/api/logs/")
		if ref == "" {
			http.Error(w, "Missing backend ref parameter", http.StatusBadRequest)
			return
		}

		setHeaders(w)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 200,
			"ref":  ref,
			"data": state.GetLogs(ref),
		})
	})

	mux.HandleFunc("/api/process/", func(w http.ResponseWriter, r *http.Request) {
		// 放行浏览器的预检请求 (Preflight)
		if r.Method == http.MethodOptions {
			setHeaders(w)
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if r.Method != http.MethodDelete {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		ref := strings.TrimPrefix(r.URL.Path, "/api/process/")
		if ref == "" {
			http.Error(w, "Missing backend ref parameter", http.StatusBadRequest)
			return
		}

		setHeaders(w)
		if err := state.KillProcess(ref); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"code":  500,
				"error": err.Error(),
			})
			return
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 200,
			"msg":  "Process killed successfully",
		})
	})

	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			setHeaders(w)
			w.WriteHeader(http.StatusNoContent)
			return
		}

		setHeaders(w)

		// GET: 获取当前路由 JSON
		if r.Method == http.MethodGet {
			w.Write(state.GetConfigJSON())
			return
		}

		// POST: 提交新 JSON 并触发重载
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			if err := state.ReloadConfig(body); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"code":  400,
					"error": err.Error(),
				})
				return
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"code": 200,
				"msg":  "Config reloaded successfully",
			})
			return
		}

		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	})

	httpServer := &http.Server{Handler: mux}
	return httpServer.Serve(ln)
}
