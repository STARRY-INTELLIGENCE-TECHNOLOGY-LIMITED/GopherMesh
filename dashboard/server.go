package dashboard

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
)

// EndpointStatus 表示单个计算端点的运行时状态
type EndpointStatus struct {
	Name         string `json:"name"`
	InternalPort string `json:"internalPort"`
	Status       string `json:"status"` // Dormant 休眠 或 Running 运行
	PID          int    `json:"pid,omitempty"`
	Uptime       string `json:"uptime,omitempty"`
}

// MeshState 控制台向主引擎索取数据的契约
type MeshState interface {
	GetStatus() map[string]EndpointStatus
	GetLogs(port string) []string // 新增获取日志接口
}

// Serve 启动无头控制台的 API 服务
func Serve(ln net.Listener, state MeshState) error {
	mux := http.NewServeMux()

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

		// 极简路由解析，提取形如 /api/logs/9081 中的 port
		port := strings.TrimPrefix(r.URL.Path, "/api/logs/")
		if port == "" {
			http.Error(w, "Missing port parameter", http.StatusBadRequest)
			return
		}

		setHeaders(w)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 200,
			"port": port,
			"data": state.GetLogs(port),
		})
	})

	httpServer := &http.Server{Handler: mux}
	return httpServer.Serve(ln)
}
