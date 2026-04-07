package dashboard

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
)

type stubMeshState struct {
	mu             sync.Mutex
	configJSON     []byte
	reloadErr      error
	lastReloadBody []byte
}

func (s *stubMeshState) GetStatus() map[string]RouteStatus {
	return map[string]RouteStatus{
		"8081": {
			Name:        "HTTP Sample Route",
			Protocol:    "http",
			LoadBalance: "round_robin",
			Backends: []BackendStatus{
				{
					Ref:          "8081:0",
					Name:         "sample-a",
					InternalPort: "9081",
					Status:       "Running",
					PID:          4321,
					Uptime:       "3s",
				},
				{
					Ref:          "8081:1",
					Name:         "sample-b",
					InternalPort: "9082",
					Status:       "Dormant",
				},
			},
		},
	}
}

func (s *stubMeshState) GetLogs(port string) []string {
	return []string{"port=" + port, "log-line"}
}

func (s *stubMeshState) KillProcess(string) error {
	return nil
}

func (s *stubMeshState) GetConfigJSON() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]byte(nil), s.configJSON...)
}

func (s *stubMeshState) ReloadConfig(rawJSON []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.lastReloadBody = append([]byte(nil), rawJSON...)
	return s.reloadErr
}

func (s *stubMeshState) LastReloadBody() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]byte(nil), s.lastReloadBody...)
}

func startDashboardTestServer(t *testing.T, state MeshState) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	t.Cleanup(func() {
		_ = ln.Close()
	})

	go func() {
		_ = Serve(ln, state)
	}()

	return "http://" + ln.Addr().String()
}

func TestServeStatusAndLogsAPI(t *testing.T) {
	t.Parallel()

	baseURL := startDashboardTestServer(t, &stubMeshState{
		configJSON: []byte(`{"8081":{"name":"HTTP Sample Route"}}`),
	})

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

	logsResp, err := http.Get(baseURL + "/api/logs/8081:0")
	if err != nil {
		t.Fatalf("GET /api/logs/8081:0 error = %v", err)
	}
	defer logsResp.Body.Close()

	if logsResp.StatusCode != http.StatusOK {
		t.Fatalf("/api/logs/8081:0 status = %d, want %d", logsResp.StatusCode, http.StatusOK)
	}

	var logsPayload struct {
		Code int      `json:"code"`
		Ref  string   `json:"ref"`
		Data []string `json:"data"`
	}
	if err := json.NewDecoder(logsResp.Body).Decode(&logsPayload); err != nil {
		t.Fatalf("decode logs payload error = %v", err)
	}
	if logsPayload.Ref != "8081:0" {
		t.Fatalf("logs payload ref = %q, want %q", logsPayload.Ref, "8081:0")
	}
	if len(logsPayload.Data) != 2 {
		t.Fatalf("logs payload data len = %d, want %d", len(logsPayload.Data), 2)
	}
}

func TestServeRejectsInvalidMethodsAndMissingPort(t *testing.T) {
	t.Parallel()

	baseURL := startDashboardTestServer(t, &stubMeshState{})

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

func TestServeConfigAPI(t *testing.T) {
	t.Parallel()

	state := &stubMeshState{
		configJSON: []byte(`{
  "8082": {
    "name": "Reloaded Route",
    "backends": [
      {
        "name": "backend-1",
        "internal_port": "9082"
      }
    ]
  }
}`),
	}
	baseURL := startDashboardTestServer(t, state)

	getResp, err := http.Get(baseURL + "/api/config")
	if err != nil {
		t.Fatalf("GET /api/config error = %v", err)
	}
	defer getResp.Body.Close()

	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/config status = %d, want %d", getResp.StatusCode, http.StatusOK)
	}
	if got := getResp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("GET /api/config Access-Control-Allow-Origin = %q, want %q", got, "*")
	}

	var getPayload map[string]map[string]interface{}
	if err := json.NewDecoder(getResp.Body).Decode(&getPayload); err != nil {
		t.Fatalf("decode config payload error = %v", err)
	}
	if _, ok := getPayload["8082"]; !ok {
		t.Fatalf("GET /api/config payload missing route 8082: %#v", getPayload)
	}

	postBody := []byte(`{"8083":{"name":"new-route","backends":[{"name":"b1","internal_port":"9083"}]}}`)
	postResp, err := http.Post(baseURL+"/api/config", "application/json", bytes.NewReader(postBody))
	if err != nil {
		t.Fatalf("POST /api/config error = %v", err)
	}
	defer postResp.Body.Close()

	if postResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/config status = %d, want %d", postResp.StatusCode, http.StatusOK)
	}

	var postPayload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.NewDecoder(postResp.Body).Decode(&postPayload); err != nil {
		t.Fatalf("decode POST /api/config payload error = %v", err)
	}
	if postPayload.Code != http.StatusOK {
		t.Fatalf("POST /api/config payload code = %d, want %d", postPayload.Code, http.StatusOK)
	}
	if string(state.LastReloadBody()) != string(postBody) {
		t.Fatalf("ReloadConfig() body = %q, want %q", string(state.LastReloadBody()), string(postBody))
	}

	req, err := http.NewRequest(http.MethodOptions, baseURL+"/api/config", nil)
	if err != nil {
		t.Fatalf("NewRequest(OPTIONS /api/config) error = %v", err)
	}
	optionsResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("OPTIONS /api/config error = %v", err)
	}
	defer optionsResp.Body.Close()

	if optionsResp.StatusCode != http.StatusNoContent {
		t.Fatalf("OPTIONS /api/config status = %d, want %d", optionsResp.StatusCode, http.StatusNoContent)
	}
}

func TestServeConfigAPIPropagatesReloadErrors(t *testing.T) {
	t.Parallel()

	state := &stubMeshState{
		reloadErr: errors.New("reload failed"),
	}
	baseURL := startDashboardTestServer(t, state)

	resp, err := http.Post(baseURL+"/api/config", "application/json", bytes.NewReader([]byte(`{"8082":{}}`)))
	if err != nil {
		t.Fatalf("POST /api/config error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /api/config status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}

	var payload struct {
		Code  int    `json:"code"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode POST /api/config error payload error = %v", err)
	}
	if payload.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/config payload code = %d, want %d", payload.Code, http.StatusBadRequest)
	}
	if payload.Error != "reload failed" {
		t.Fatalf("POST /api/config payload error = %q, want %q", payload.Error, "reload failed")
	}
}

func TestServeRootContainsLoadBalanceSelectorAndRefreshHook(t *testing.T) {
	t.Parallel()

	baseURL := startDashboardTestServer(t, &stubMeshState{})

	resp, err := http.Get(baseURL + "/")
	if err != nil {
		t.Fatalf("GET / error = %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll(/) error = %v", err)
	}

	html := string(body)
	if !strings.Contains(html, `id="f_loadBalance"`) {
		t.Fatalf("dashboard html missing load balance selector")
	}
	if !strings.Contains(html, `<option value="stdio">STDIO</option>`) {
		t.Fatalf("dashboard html missing stdio protocol option")
	}
	if !strings.Contains(html, `id="f_stdioMode"`) {
		t.Fatalf("dashboard html missing stdio mode selector")
	}
	if !strings.Contains(html, `<option value="least_conn">least_conn</option>`) {
		t.Fatalf("dashboard html missing least_conn option")
	}
	if !strings.Contains(html, `<option value="ip_hash">ip_hash</option>`) {
		t.Fatalf("dashboard html missing ip_hash option")
	}
	if !strings.Contains(html, `await refreshDashboardState();`) {
		t.Fatalf("dashboard html missing refreshDashboardState success hook")
	}
	if !strings.Contains(html, `latestConfig = await loadGlobalConfig();`) {
		t.Fatalf("dashboard html missing latest config reload before save")
	}
	if !strings.Contains(html, `配置已热重载成功，但界面刷新失败`) {
		t.Fatalf("dashboard html missing refresh failure warning after successful save")
	}
	if !strings.Contains(html, `if (json.code === 200) {`) || !strings.Contains(html, `await fetchStatus();`) {
		t.Fatalf("dashboard html missing awaited status refresh after process kill")
	}
	if !strings.Contains(html, `id="deleteBackendBtn"`) || !strings.Contains(html, `删除 (Delete)`) {
		t.Fatalf("dashboard html missing delete backend button")
	}
	if !strings.Contains(html, `function removeBackendFromConfig(config, publicPort, backendIndex)`) || !strings.Contains(html, `delete config[publicPort];`) {
		t.Fatalf("dashboard html missing backend removal helper")
	}
	if !strings.Contains(html, `const canKill = isRunning && Number(ep.pid) > 0;`) {
		t.Fatalf("dashboard html should only show kill button for managed running backends")
	}
	if !strings.Contains(html, `function updateProtocolFormState()`) || !strings.Contains(html, `protocol === 'stdio'`) {
		t.Fatalf("dashboard html missing stdio-aware protocol form toggle")
	}
	if !strings.Contains(html, `route.stdioMode || 'stream'`) || !strings.Contains(html, `route.stdio_mode || 'stream'`) {
		t.Fatalf("dashboard html missing stdio mode defaults for status and config")
	}
	if !strings.Contains(html, `document.getElementById('f_stdioMode').addEventListener('change', updateProtocolFormState)`) {
		t.Fatalf("dashboard html missing stdio mode change hook")
	}
	if !strings.Contains(html, `openLogModal(${JSON.stringify(backendRef)}, ${JSON.stringify(ep.name)})`) {
		t.Fatalf("dashboard html missing backend-ref based log action")
	}
	if !strings.Contains(html, `openFormModal(${JSON.stringify(publicPort)}, ${backendIndex})`) {
		t.Fatalf("dashboard html missing public-port plus index config action")
	}
	if !strings.Contains(html, `encodeURIComponent(currentLogRef)`) || !strings.Contains(html, `encodeURIComponent(ref)`) {
		t.Fatalf("dashboard html missing encoded backend ref API calls")
	}
}
