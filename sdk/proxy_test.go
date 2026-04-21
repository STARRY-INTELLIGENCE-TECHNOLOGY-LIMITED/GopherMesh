package mesh

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func freePort(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer ln.Close()

	return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
}

func newTestEngine(cfg Config) *Engine {
	return &Engine{
		cfg:     cfg,
		role:    RoleMaster,
		process: make(map[string]*ProcessInfo),
		logBufs: make(map[string]*LogBuffer),
	}
}

func startHTTPBackend(t *testing.T, label string) (string, func()) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("backend Listen() error = %v", err)
	}

	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprintf(w, "%s %s", label, r.URL.Path)
		}),
	}
	go func() {
		_ = server.Serve(ln)
	}()

	shutdown := func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}

	return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port), shutdown
}

func startBlockingHTTPBackend(t *testing.T, label string, entered chan<- struct{}, release <-chan struct{}) (string, func()) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("backend Listen() error = %v", err)
	}

	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
			_, _ = fmt.Fprintf(w, "%s %s", label, r.URL.Path)
		}),
	}
	go func() {
		_ = server.Serve(ln)
	}()

	shutdown := func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}

	return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port), shutdown
}

func TestRouteHandleRequestInternalRouteIsCaseInsensitive(t *testing.T) {
	t.Parallel()

	engine := newTestEngine(Config{
		TrustedOrigins: []string{"https://console.example.com/"},
	})
	state := newRouteState(engine, "8081", RouteConfig{
		Name:     "Internal Route",
		Protocol: "http",
		Backends: []BackendConfig{
			{
				Cmd:          "Internal",
				InternalPort: defaultDashboardPort,
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "http://mesh.local/health", nil)
	req.Header.Set("Origin", "https://console.example.com")
	rec := httptest.NewRecorder()

	state.handleRequest(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://console.example.com" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, "https://console.example.com")
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v body=%s", err, rec.Body.String())
	}
	if got := payload["mesh"]; got != "running" {
		t.Fatalf("payload mesh = %#v, want %q", got, "running")
	}
	if got := payload["version"]; got != DefaultHealthzVersion {
		t.Fatalf("payload version = %#v, want %q", got, DefaultHealthzVersion)
	}
}

func TestRouteHandleRequestRejectsForbiddenOrigin(t *testing.T) {
	t.Parallel()

	engine := newTestEngine(Config{
		TrustedOrigins: []string{"https://allowed.example.com"},
	})
	state := newRouteState(engine, "8081", RouteConfig{
		Name:     "Internal Route",
		Protocol: "http",
		Backends: []BackendConfig{
			{
				Cmd:          "internal",
				InternalPort: defaultDashboardPort,
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "http://mesh.local/health", nil)
	req.Header.Set("Origin", "https://blocked.example.com")
	rec := httptest.NewRecorder()

	state.handleRequest(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

func TestRouteHandleRequestOptionsPreflight(t *testing.T) {
	t.Parallel()

	engine := newTestEngine(Config{
		TrustedOrigins: []string{"https://allowed.example.com"},
	})
	state := newRouteState(engine, "8081", RouteConfig{
		Name:     "Internal Route",
		Protocol: "http",
		Backends: []BackendConfig{
			{
				Cmd:          "internal",
				InternalPort: defaultDashboardPort,
			},
		},
	})

	req := httptest.NewRequest(http.MethodOptions, "http://mesh.local/health", nil)
	req.Header.Set("Origin", "https://allowed.example.com")
	rec := httptest.NewRecorder()

	state.handleRequest(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://allowed.example.com" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, "https://allowed.example.com")
	}
}

func TestEngineStartRoutesHTTPForwardsTrafficToReadyBackend(t *testing.T) {
	internalPort, shutdownBackend := startHTTPBackend(t, "ready-backend")
	defer shutdownBackend()

	publicPort := freePort(t)

	engine := newTestEngine(Config{
		TrustedOrigins: []string{"*"},
		Routes: map[string]RouteConfig{
			publicPort: {
				Name:     "Ready HTTP Backend",
				Protocol: "http",
				Backends: []BackendConfig{
					{
						Name:         "backend-1",
						Cmd:          "worker",
						InternalPort: internalPort,
					},
				},
			},
		},
	})
	if err := engine.startRoutes(); err != nil {
		t.Fatalf("startRoutes() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = engine.Shutdown(ctx)
	})

	resp, err := http.Get("http://127.0.0.1:" + publicPort + "/ping")
	if err != nil {
		t.Fatalf("GET public proxy error = %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.StatusCode, http.StatusOK, string(body))
	}
	if got := string(body); got != "ready-backend /ping" {
		t.Fatalf("body = %q, want %q", got, "ready-backend /ping")
	}
}

func TestRouteHandleRequestRoundRobinAcrossReadyBackends(t *testing.T) {
	portA, shutdownA := startHTTPBackend(t, "backend-a")
	defer shutdownA()

	portB, shutdownB := startHTTPBackend(t, "backend-b")
	defer shutdownB()

	engine := newTestEngine(Config{
		TrustedOrigins: []string{"*"},
	})
	state := newRouteState(engine, "8081", RouteConfig{
		Name:        "lb-http",
		Protocol:    "http",
		LoadBalance: defaultLoadBalance,
		Backends: []BackendConfig{
			{Name: "A", Cmd: "worker", InternalPort: portA},
			{Name: "B", Cmd: "worker", InternalPort: portB},
		},
	})

	expected := []string{
		"backend-a /task/0",
		"backend-b /task/1",
		"backend-a /task/2",
		"backend-b /task/3",
	}

	for i, want := range expected {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("http://mesh.local/task/%d", i), nil)
		rec := httptest.NewRecorder()

		state.handleRequest(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status = %d, want %d", i, rec.Code, http.StatusOK)
		}
		if got := rec.Body.String(); got != want {
			t.Fatalf("request %d body = %q, want %q", i, got, want)
		}
	}
}

func TestRouteHandleRequestLeastConnPrefersBackendWithFewestInflightRequests(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})

	portA, shutdownA := startBlockingHTTPBackend(t, "backend-a", entered, release)
	defer shutdownA()

	portB, shutdownB := startHTTPBackend(t, "backend-b")
	defer shutdownB()

	engine := newTestEngine(Config{
		TrustedOrigins: []string{"*"},
	})
	state := newRouteState(engine, "8081", RouteConfig{
		Name:        "least-conn-http",
		Protocol:    "http",
		LoadBalance: loadBalanceLeastConn,
		Backends: []BackendConfig{
			{Name: "A", Cmd: "worker", InternalPort: portA},
			{Name: "B", Cmd: "worker", InternalPort: portB},
		},
	})

	reqSlow := httptest.NewRequest(http.MethodGet, "http://mesh.local/slow", nil)
	recSlow := httptest.NewRecorder()
	doneSlow := make(chan struct{})
	go func() {
		state.handleRequest(recSlow, reqSlow)
		close(doneSlow)
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for first backend request to block")
	}

	reqFast := httptest.NewRequest(http.MethodGet, "http://mesh.local/fast", nil)
	recFast := httptest.NewRecorder()
	state.handleRequest(recFast, reqFast)

	if recFast.Code != http.StatusOK {
		t.Fatalf("fast request status = %d, want %d body=%s", recFast.Code, http.StatusOK, recFast.Body.String())
	}
	if got := recFast.Body.String(); got != "backend-b /fast" {
		t.Fatalf("fast request body = %q, want %q", got, "backend-b /fast")
	}

	close(release)
	select {
	case <-doneSlow:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for slow request to finish")
	}

	if recSlow.Code != http.StatusOK {
		t.Fatalf("slow request status = %d, want %d body=%s", recSlow.Code, http.StatusOK, recSlow.Body.String())
	}
	if got := recSlow.Body.String(); got != "backend-a /slow" {
		t.Fatalf("slow request body = %q, want %q", got, "backend-a /slow")
	}
}

func TestBackendTargetAddressDefaultsBlankHostToLocalhost(t *testing.T) {
	backend := &backendState{
		cfg: BackendConfig{
			InternalPort: "19090",
		},
	}

	if got := backend.targetAddress(); got != "127.0.0.1:19090" {
		t.Fatalf("targetAddress() = %q, want %q", got, "127.0.0.1:19090")
	}
}

func TestBackendStartOnceReturnsNilWhenTargetBecomesReadyBeforeSpawn(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer ln.Close()

	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	backend := &backendState{
		route: &routeState{
			engine: newTestEngine(Config{}),
		},
		cfg: BackendConfig{
			Cmd:          "",
			InternalHost: "127.0.0.1",
			InternalPort: port,
		},
	}

	if err := backend.startOnce(context.Background()); err != nil {
		t.Fatalf("startOnce() error = %v, want nil when target is already ready", err)
	}
}

func TestRouteHandleRequestColdStartConcurrentRequestsSpawnOnlyOnce(t *testing.T) {
	internalPort := freePort(t)
	markerPath := filepath.Join(t.TempDir(), "starts.log")

	engine := newTestEngine(Config{
		TrustedOrigins: []string{"*"},
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = engine.Shutdown(ctx)
	})

	state := newRouteState(engine, "8081", RouteConfig{
		Name:     "Cold Start Helper",
		Protocol: "http",
		Backends: []BackendConfig{
			{
				Name: "helper",
				Cmd:  os.Args[0],
				Args: []string{
					"-test.run=^TestHelperProcess$",
					"-gophermesh-helper=http",
					"-gophermesh-port=" + internalPort,
					"-gophermesh-marker=" + markerPath,
				},
				InternalPort: internalPort,
			},
		},
	})

	const workers = 6
	start := make(chan struct{})
	errCh := make(chan error, workers)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start

			req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("http://mesh.local/task/%d", index), nil)
			rec := httptest.NewRecorder()
			state.handleRequest(rec, req)

			if rec.Code != http.StatusOK {
				errCh <- fmt.Errorf("request %d status = %d body=%s", index, rec.Code, rec.Body.String())
				return
			}

			expectedBody := fmt.Sprintf("helper GET /task/%d", index)
			if body := rec.Body.String(); body != expectedBody {
				errCh <- fmt.Errorf("request %d body = %q, want %q", index, body, expectedBody)
			}
		}(i)
	}

	close(start)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Error(err)
	}

	data, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", markerPath, err)
	}

	startCount := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line != "" {
			startCount++
		}
	}
	if startCount != 1 {
		t.Fatalf("helper process started %d times, want 1", startCount)
	}
}
