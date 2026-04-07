package mesh

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func startTCPBackend(t *testing.T, prefix string) (string, func()) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("backend Listen() error = %v", err)
	}

	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}

			go func(c net.Conn) {
				defer c.Close()

				payload, _ := io.ReadAll(c)
				_, _ = c.Write([]byte(prefix + string(payload)))
			}(conn)
		}
	}()

	return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port), func() {
		_ = ln.Close()
	}
}

func dialTCPProxy(t *testing.T, publicPort string, payload string) string {
	t.Helper()

	conn, err := net.Dial("tcp", "127.0.0.1:"+publicPort)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.CloseWrite()
	}

	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	return string(got)
}

func TestEngineStartRoutesTCPForwardsTraffic(t *testing.T) {
	internalPort, shutdownBackend := startTCPBackend(t, "echo:")
	defer shutdownBackend()

	publicPort := freePort(t)

	engine := newTestEngine(Config{
		Routes: map[string]RouteConfig{
			publicPort: {
				Name:     "TCP Echo",
				Protocol: "tcp",
				Backends: []BackendConfig{
					{
						Name:         "echo-1",
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

	if got := dialTCPProxy(t, publicPort, "mesh tcp payload"); got != "echo:mesh tcp payload" {
		t.Fatalf("proxy response = %q, want %q", got, "echo:mesh tcp payload")
	}
}

func TestEngineStartRoutesTCPRoundRobinAcrossBackends(t *testing.T) {
	portA, shutdownA := startTCPBackend(t, "A:")
	defer shutdownA()

	portB, shutdownB := startTCPBackend(t, "B:")
	defer shutdownB()

	publicPort := freePort(t)

	engine := newTestEngine(Config{
		Routes: map[string]RouteConfig{
			publicPort: {
				Name:        "TCP LB",
				Protocol:    "tcp",
				LoadBalance: defaultLoadBalance,
				Backends: []BackendConfig{
					{Name: "backend-a", Cmd: "worker", InternalPort: portA},
					{Name: "backend-b", Cmd: "worker", InternalPort: portB},
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

	if got := dialTCPProxy(t, publicPort, "first"); got != "A:first" {
		t.Fatalf("first response = %q, want %q", got, "A:first")
	}
	if got := dialTCPProxy(t, publicPort, "second"); got != "B:second" {
		t.Fatalf("second response = %q, want %q", got, "B:second")
	}
}

func TestRouteConnectBackendIPHashPinsClientIP(t *testing.T) {
	portA, shutdownA := startTCPBackend(t, "A:")
	defer shutdownA()

	portB, shutdownB := startTCPBackend(t, "B:")
	defer shutdownB()

	engine := newTestEngine(Config{})
	state := newRouteState(engine, "8081", RouteConfig{
		Name:        "TCP IP Hash",
		Protocol:    "tcp",
		LoadBalance: loadBalanceIPHash,
		Backends: []BackendConfig{
			{Name: "backend-a", Cmd: "worker", InternalPort: portA},
			{Name: "backend-b", Cmd: "worker", InternalPort: portB},
		},
	})

	conn1, backend1, err := state.connectBackend(context.Background(), "10.0.0.1:3000")
	if err != nil {
		t.Fatalf("connectBackend(client-1 first) error = %v", err)
	}
	_ = conn1.Close()

	conn2, backend2, err := state.connectBackend(context.Background(), "10.0.0.1:4000")
	if err != nil {
		t.Fatalf("connectBackend(client-1 second) error = %v", err)
	}
	_ = conn2.Close()

	conn3, backend3, err := state.connectBackend(context.Background(), "10.0.0.2:5000")
	if err != nil {
		t.Fatalf("connectBackend(client-2) error = %v", err)
	}
	_ = conn3.Close()

	if backend1.cfg.Name != backend2.cfg.Name {
		t.Fatalf("same client IP selected backends %q and %q, want stable pinning", backend1.cfg.Name, backend2.cfg.Name)
	}
	if backend1.cfg.Name == backend3.cfg.Name {
		t.Fatalf("different client IPs selected same backend %q, want different hash buckets", backend1.cfg.Name)
	}
}

func TestEngineStartRoutesSTDIOForwardsTrafficAndSpawnsPerRequest(t *testing.T) {
	markerPath := filepath.Join(t.TempDir(), "stdio-starts.log")
	publicPort := freePort(t)

	engine := newTestEngine(Config{
		Routes: map[string]RouteConfig{
			publicPort: {
				Name:      "STDIO Echo",
				Protocol:  protocolSTDIO,
				StdioMode: stdioModeStream,
				Backends: []BackendConfig{
					{
						Name: "stdio-echo",
						Cmd:  os.Args[0],
						Args: []string{
							"-test.run=^TestHelperProcess$",
							"-gophermesh-helper=stdio",
							"-gophermesh-marker=" + markerPath,
							"-gophermesh-stdio-prefix=echo:",
						},
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

	if got := dialTCPProxy(t, publicPort, "first"); got != "echo:first" {
		t.Fatalf("first response = %q, want %q", got, "echo:first")
	}
	if got := dialTCPProxy(t, publicPort, "second"); got != "echo:second" {
		t.Fatalf("second response = %q, want %q", got, "echo:second")
	}

	if len(engine.process) != 0 {
		t.Fatalf("stdio route should not register persistent processes, got %d tracked entries", len(engine.process))
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
	if startCount != 2 {
		t.Fatalf("stdio helper started %d times, want %d", startCount, 2)
	}
}

func TestEngineStartRoutesSTDIORoundRobinAcrossBackends(t *testing.T) {
	publicPort := freePort(t)

	engine := newTestEngine(Config{
		Routes: map[string]RouteConfig{
			publicPort: {
				Name:        "STDIO Round Robin",
				Protocol:    protocolSTDIO,
				StdioMode:   stdioModeStream,
				LoadBalance: defaultLoadBalance,
				Backends: []BackendConfig{
					{
						Name: "stdio-a",
						Cmd:  os.Args[0],
						Args: []string{
							"-test.run=^TestHelperProcess$",
							"-gophermesh-helper=stdio",
							"-gophermesh-stdio-prefix=A:",
						},
					},
					{
						Name: "stdio-b",
						Cmd:  os.Args[0],
						Args: []string{
							"-test.run=^TestHelperProcess$",
							"-gophermesh-helper=stdio",
							"-gophermesh-stdio-prefix=B:",
						},
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

	if got := dialTCPProxy(t, publicPort, "first"); got != "A:first" {
		t.Fatalf("first response = %q, want %q", got, "A:first")
	}
	if got := dialTCPProxy(t, publicPort, "second"); got != "B:second" {
		t.Fatalf("second response = %q, want %q", got, "B:second")
	}
}

func TestEngineSTDIOStatusAndLogsUseBackendRef(t *testing.T) {
	publicPort := freePort(t)

	engine := newTestEngine(Config{
		Routes: map[string]RouteConfig{
			publicPort: {
				Name:      "STDIO Logs",
				Protocol:  protocolSTDIO,
				StdioMode: stdioModeStream,
				Backends: []BackendConfig{
					{
						Name: "stdio-logs",
						Cmd:  os.Args[0],
						Args: []string{
							"-test.run=^TestHelperProcess$",
							"-gophermesh-helper=stdio",
							"-gophermesh-stdio-prefix=ok:",
							"-gophermesh-stdio-stderr=stderr-line",
						},
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

	if got := dialTCPProxy(t, publicPort, "payload"); got != "ok:payload" {
		t.Fatalf("response = %q, want %q", got, "ok:payload")
	}

	status := engine.GetStatus()
	backend := status[publicPort].Backends[0]
	if backend.Ref != publicPort+":0" {
		t.Fatalf("backend ref = %q, want %q", backend.Ref, publicPort+":0")
	}
	if backend.Status != "Request-Driven" {
		t.Fatalf("backend status = %q, want %q", backend.Status, "Request-Driven")
	}

	logs := engine.GetLogs(backend.Ref)
	if len(logs) == 0 {
		t.Fatalf("GetLogs(%q) returned no lines", backend.Ref)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "stderr-line") {
		t.Fatalf("GetLogs(%q) = %#v, want stderr-line", backend.Ref, logs)
	}
}

func TestEngineStartRoutesSTDIOHTTPForwardsBrowserRequest(t *testing.T) {
	publicPort := freePort(t)

	engine := newTestEngine(Config{
		Routes: map[string]RouteConfig{
			publicPort: {
				Name:      "STDIO HTTP Echo",
				Protocol:  protocolSTDIO,
				StdioMode: stdioModeHTTP,
				Backends: []BackendConfig{
					{
						Name: "stdio-http-echo",
						Cmd:  os.Args[0],
						Args: []string{
							"-test.run=^TestHelperProcess$",
							"-gophermesh-helper=stdio",
							"-gophermesh-stdio-prefix=echo:",
						},
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

	resp, err := http.Get("http://127.0.0.1:" + publicPort + "/hello?x=1")
	if err != nil {
		t.Fatalf("GET stdio http route error = %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.StatusCode, http.StatusOK, string(body))
	}
	if got := resp.Header.Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want %q", got, "text/plain; charset=utf-8")
	}
	if !strings.Contains(string(body), "GET /hello?x=1 HTTP/1.1") {
		t.Fatalf("body = %q, want serialized HTTP request line", string(body))
	}
}

func TestEngineStartRoutesSTDIOHTTPStreamsStdoutChunks(t *testing.T) {
	publicPort := freePort(t)

	engine := newTestEngine(Config{
		Routes: map[string]RouteConfig{
			publicPort: {
				Name:      "STDIO HTTP Stream",
				Protocol:  protocolSTDIO,
				StdioMode: stdioModeHTTP,
				Backends: []BackendConfig{
					{
						Name: "stdio-http-stream",
						Cmd:  os.Args[0],
						Args: []string{
							"-test.run=^TestHelperProcess$",
							"-gophermesh-helper=stdio",
							"-gophermesh-stdio-chunks=chunk-1\\n|chunk-2\\n",
							"-gophermesh-stdio-delay-ms=300",
						},
					},
				},
			},
		},
	})
	if err := engine.startRoutes(); err != nil {
		t.Fatalf("startRoutes() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = engine.Shutdown(ctx)
	})

	resp, err := http.Get("http://127.0.0.1:" + publicPort + "/stream")
	if err != nil {
		t.Fatalf("GET stdio streaming route error = %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	start := time.Now()
	firstLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("ReadString(first chunk) error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("first stdout chunk arrived after %v, want streaming before second chunk delay", elapsed)
	}
	if firstLine != "chunk-1\n" {
		t.Fatalf("first chunk = %q, want %q", firstLine, "chunk-1\n")
	}

	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll(remaining chunks) error = %v", err)
	}
	if got := string(rest); got != "chunk-2\n" {
		t.Fatalf("remaining chunks = %q, want %q", got, "chunk-2\n")
	}
}

func TestEngineStartRoutesSTDIOStreamModeKeepsRawPayloadsThatLookLikeHTTP(t *testing.T) {
	publicPort := freePort(t)

	engine := newTestEngine(Config{
		Routes: map[string]RouteConfig{
			publicPort: {
				Name:      "STDIO Raw Stream",
				Protocol:  protocolSTDIO,
				StdioMode: stdioModeStream,
				Backends: []BackendConfig{
					{
						Name: "stdio-raw",
						Cmd:  os.Args[0],
						Args: []string{
							"-test.run=^TestHelperProcess$",
							"-gophermesh-helper=stdio",
							"-gophermesh-stdio-prefix=raw:",
						},
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

	payload := "GET /not-http HTTP/1.1\r\nHost: raw-client\r\n\r\n"
	if got := dialTCPProxy(t, publicPort, payload); got != "raw:"+payload {
		t.Fatalf("response = %q, want %q", got, "raw:"+payload)
	}
}

func TestEngineStartRoutesSTDIOHTTPStreamsWhileRequestBodyIsStillUploading(t *testing.T) {
	publicPort := freePort(t)

	engine := newTestEngine(Config{
		Routes: map[string]RouteConfig{
			publicPort: {
				Name:      "STDIO HTTP Duplex",
				Protocol:  protocolSTDIO,
				StdioMode: stdioModeHTTP,
				Backends: []BackendConfig{
					{
						Name: "stdio-http-duplex",
						Cmd:  os.Args[0],
						Args: []string{
							"-test.run=^TestHelperProcess$",
							"-gophermesh-helper=stdio",
							"-gophermesh-stdio-read-prefix-bytes=1",
							"-gophermesh-stdio-early-chunk-bytes=262144",
							"-gophermesh-stdio-final-chunk=done\\n",
						},
					},
				},
			},
		},
	})
	if err := engine.startRoutes(); err != nil {
		t.Fatalf("startRoutes() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = engine.Shutdown(ctx)
	})

	conn, err := net.Dial("tcp", "127.0.0.1:"+publicPort)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	responseCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		data, err := io.ReadAll(conn)
		if err != nil {
			errCh <- err
			return
		}
		responseCh <- string(data)
	}()

	body := strings.Repeat("x", 512*1024)
	request := "POST /duplex HTTP/1.1\r\n" +
		"Host: 127.0.0.1\r\n" +
		"Content-Type: text/plain\r\n" +
		"Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n"

	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatalf("Write(request headers) error = %v", err)
	}
	if _, err := conn.Write([]byte(body)); err != nil {
		t.Fatalf("Write(request body) error = %v", err)
	}
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.CloseWrite()
	}

	select {
	case err := <-errCh:
		t.Fatalf("ReadAll(response) error = %v", err)
	case resp := <-responseCh:
		if !strings.Contains(resp, "HTTP/1.1 200 OK") {
			t.Fatalf("response missing 200 OK status line: %q", resp)
		}
		if !strings.Contains(resp, "Transfer-Encoding: chunked") {
			t.Fatalf("response missing chunked header: %q", resp)
		}
		if !strings.Contains(resp, "done\n") {
			t.Fatalf("response body missing final chunk marker: %q", resp)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for duplex stdio response")
	}
}

func TestEngineStartRoutesSTDIOHTTPReturns502WhenBackendExitsBeforeOutput(t *testing.T) {
	publicPort := freePort(t)

	engine := newTestEngine(Config{
		Routes: map[string]RouteConfig{
			publicPort: {
				Name:      "STDIO HTTP Failure",
				Protocol:  protocolSTDIO,
				StdioMode: stdioModeHTTP,
				Backends: []BackendConfig{
					{
						Name: "stdio-http-fail",
						Cmd:  os.Args[0],
						Args: []string{
							"-test.run=^TestHelperProcess$",
							"-gophermesh-helper=unknown-mode",
						},
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

	resp, err := http.Get("http://127.0.0.1:" + publicPort + "/fail")
	if err != nil {
		t.Fatalf("GET stdio failure route error = %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d body=%s", resp.StatusCode, http.StatusBadGateway, string(body))
	}
}
