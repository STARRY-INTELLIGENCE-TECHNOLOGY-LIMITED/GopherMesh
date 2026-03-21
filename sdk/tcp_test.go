package mesh

import (
	"context"
	"io"
	"net"
	"strconv"
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
