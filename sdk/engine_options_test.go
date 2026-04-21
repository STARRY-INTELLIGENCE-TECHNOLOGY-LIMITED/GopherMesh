package mesh

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestEngineRunOpensBrowserByDefault(t *testing.T) {
	publicPort := freePort(t)
	dashboardPort := freePort(t)

	engine := newRunTestEngine(t, publicPort, dashboardPort, EngineOptions{})

	called := make(chan string, 1)
	restore := stubDashboardBrowserOpener(func(url string) {
		called <- url
	})
	defer restore()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- engine.Run(ctx)
	}()

	select {
	case url := <-called:
		if want := "http://127.0.0.1:" + dashboardPort; url != want {
			t.Fatalf("dashboard URL = %q, want %q", url, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for dashboard browser opener")
	}

	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	shutdownRunTestEngine(t, engine)
}

func TestEngineRunSkipsBrowserOpenWhenNoDashboard(t *testing.T) {
	publicPort := freePort(t)
	dashboardPort := freePort(t)

	engine := newRunTestEngine(t, publicPort, dashboardPort, EngineOptions{
		NoDashboard: true,
	})

	called := make(chan string, 1)
	restore := stubDashboardBrowserOpener(func(url string) {
		called <- url
	})
	defer restore()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- engine.Run(ctx)
	}()

	waitForHTTPHealthz(t, "http://127.0.0.1:"+publicPort+"/healthz", DefaultHealthzVersion)

	select {
	case url := <-called:
		t.Fatalf("dashboard opener called unexpectedly with %q", url)
	case <-time.After(200 * time.Millisecond):
	}

	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	shutdownRunTestEngine(t, engine)
}

func newRunTestEngine(t *testing.T, publicPort, dashboardPort string, opts EngineOptions) *Engine {
	t.Helper()

	engineIface, err := NewEngineWithOptions(Config{
		DashboardHost:  "127.0.0.1",
		DashboardPort:  dashboardPort,
		TrustedOrigins: []string{"*"},
		Routes: map[string]RouteConfig{
			publicPort: {
				Name:     "internal-health",
				Protocol: "http",
				Backends: []BackendConfig{
					{
						Name:         "dashboard",
						Cmd:          "internal",
						InternalPort: dashboardPort,
					},
				},
			},
		},
	}, opts)
	if err != nil {
		t.Fatalf("NewEngineWithOptions() error = %v", err)
	}

	engine, ok := engineIface.(*Engine)
	if !ok {
		t.Fatalf("engine type = %T, want *Engine", engineIface)
	}
	return engine
}

func shutdownRunTestEngine(t *testing.T, engine *Engine) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := engine.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func stubDashboardBrowserOpener(fn func(string)) func() {
	previous := dashboardBrowserOpener
	dashboardBrowserOpener = fn
	return func() {
		dashboardBrowserOpener = previous
	}
}

func waitForHTTPHealthz(t *testing.T, url string, wantVersion string) {
	t.Helper()

	client := &http.Client{Timeout: 300 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error

	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			var payload map[string]any
			decodeErr := json.NewDecoder(resp.Body).Decode(&payload)
			_ = resp.Body.Close()
			if decodeErr == nil &&
				resp.StatusCode == http.StatusOK &&
				payload["status"] == "ok" &&
				payload["mesh"] == "running" &&
				payload["version"] == wantVersion {
				return
			}
			lastErr = decodeErr
		} else {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("GET %s did not return expected healthz payload before timeout: %v", url, lastErr)
}
