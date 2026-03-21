package dashboard

import (
	"encoding/json"
	"net"
	"net/http"
	"testing"
)

type stubMeshState struct{}

func (stubMeshState) GetStatus() map[string]RouteStatus {
	return map[string]RouteStatus{
		"8081": {
			Name:        "HTTP Sample Route",
			Protocol:    "http",
			LoadBalance: "round_robin",
			Backends: []BackendStatus{
				{
					Name:         "sample-a",
					InternalPort: "9081",
					Status:       "Running",
					PID:          4321,
					Uptime:       "3s",
				},
				{
					Name:         "sample-b",
					InternalPort: "9082",
					Status:       "Dormant",
				},
			},
		},
	}
}

func (stubMeshState) GetLogs(port string) []string {
	return []string{"port=" + port, "log-line"}
}

func TestServeStatusAndLogsAPI(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer ln.Close()

	go func() {
		_ = Serve(ln, stubMeshState{})
	}()

	baseURL := "http://" + ln.Addr().String()

	statusResp, err := http.Get(baseURL + "/api/status")
	if err != nil {
		t.Fatalf("GET /api/status error = %v", err)
	}
	defer statusResp.Body.Close()

	if statusResp.StatusCode != http.StatusOK {
		t.Fatalf("/api/status status = %d, want %d", statusResp.StatusCode, http.StatusOK)
	}
	if got := statusResp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("/api/status Access-Control-Allow-Origin = %q, want %q", got, "*")
	}

	var statusPayload struct {
		Code int                    `json:"code"`
		Data map[string]RouteStatus `json:"data"`
	}
	if err := json.NewDecoder(statusResp.Body).Decode(&statusPayload); err != nil {
		t.Fatalf("decode status payload error = %v", err)
	}
	if statusPayload.Code != http.StatusOK {
		t.Fatalf("status payload code = %d, want %d", statusPayload.Code, http.StatusOK)
	}
	route := statusPayload.Data["8081"]
	if route.Name != "HTTP Sample Route" {
		t.Fatalf("status payload route name = %q, want %q", route.Name, "HTTP Sample Route")
	}
	if len(route.Backends) != 2 {
		t.Fatalf("status payload backend len = %d, want %d", len(route.Backends), 2)
	}

	logsResp, err := http.Get(baseURL + "/api/logs/9081")
	if err != nil {
		t.Fatalf("GET /api/logs/9081 error = %v", err)
	}
	defer logsResp.Body.Close()

	if logsResp.StatusCode != http.StatusOK {
		t.Fatalf("/api/logs/9081 status = %d, want %d", logsResp.StatusCode, http.StatusOK)
	}

	var logsPayload struct {
		Code int      `json:"code"`
		Port string   `json:"port"`
		Data []string `json:"data"`
	}
	if err := json.NewDecoder(logsResp.Body).Decode(&logsPayload); err != nil {
		t.Fatalf("decode logs payload error = %v", err)
	}
	if logsPayload.Port != "9081" {
		t.Fatalf("logs payload port = %q, want %q", logsPayload.Port, "9081")
	}
	if len(logsPayload.Data) != 2 {
		t.Fatalf("logs payload data len = %d, want %d", len(logsPayload.Data), 2)
	}
}

func TestServeRejectsInvalidMethodsAndMissingPort(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer ln.Close()

	go func() {
		_ = Serve(ln, stubMeshState{})
	}()

	baseURL := "http://" + ln.Addr().String()

	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/status", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/status error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /api/status status = %d, want %d", resp.StatusCode, http.StatusMethodNotAllowed)
	}

	logsResp, err := http.Get(baseURL + "/api/logs/")
	if err != nil {
		t.Fatalf("GET /api/logs/ error = %v", err)
	}
	defer logsResp.Body.Close()

	if logsResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("GET /api/logs/ status = %d, want %d", logsResp.StatusCode, http.StatusBadRequest)
	}
}
