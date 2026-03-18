package mesh

import (
	"context"
	"errors"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"
)

const defaultStartTimeout = 30 * time.Second

// endpointState 维护单个端口的反向代理与冷启动状态机
type endpointState struct {
	engine     *Engine
	publicPort string
	cfg        EndpointConfig
	proxy      *httputil.ReverseProxy

	// startMu 用于控制并发冷启动，防止“惊群效应”打满CPU
	startMu      sync.Mutex
	starting     bool
	startCh      chan struct{} // 广播 Channel：关闭时瞬间唤醒所有阻塞请求
	lastStartErr error
}

func newEndpointState(engine *Engine, publicPort string, cfg EndpointConfig) *endpointState {
	target := "127.0.0.1:" + cfg.InternalPort
	return &endpointState{
		engine:     engine,
		publicPort: publicPort,
		cfg:        cfg,
		proxy:      newReverseProxy(target),
	}
}

// handleRequest 是所有外部流量的唯一 L7 入口
func (e *endpointState) handleRequest(w http.ResponseWriter, r *http.Request) {
	// 1. 动态 CORS 与 域名白名单校验
	reqOrigin := r.Header.Get("Origin")
	allowedOrigin := e.checkOrigin(reqOrigin)

	// 如果不在白名单中，且属于跨域请求，直接拒绝
	if reqOrigin != "" && allowedOrigin == "" {
		http.Error(w, "GopherMesh: Forbidden Origin", http.StatusForbidden)
		return
	}

	setCORSHeaders(w, allowedOrigin)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// 2. 内部路由拦截（防环路保活）
	if e.cfg.Cmd == "" || e.cfg.Cmd == "internal" {
		e.engine.handleHealthcheck(w, r)
		return
	}

	// 3. 静默冷启动状态机
	if !e.engine.isReady(e.cfg.InternalPort) {
		if err := e.startOnce(r.Context()); e != nil {
			status := http.StatusServiceUnavailable
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				status = http.StatusGatewayTimeout
			}
			http.Error(w, "GopherMesh Cold Start Failed: "+err.Error(), status)
			return
		}
	}

	// 4. 进程就绪，无感透明透传（L7）
	e.proxy.ServeHTTP(w, r)
}

// checkOrigin 匹配配置中的 TrustedOrigins 白名单
func (e *endpointState) checkOrigin(reqOrigin string) string {
	for _, origin := range e.engine.cfg.TrustedOrigins {
		if origin == "*" {
			return "*"
		}
		// 忽略大小写和末尾多余的斜杠
		if strings.EqualFold(strings.TrimRight(origin, "/"), strings.TrimRight(reqOrigin, "/")) {
			return reqOrigin
		}
	}
	return ""
}

// startOnce 保证海量并发请求下，底层高算力进程只被 spawn 一次
func (e *endpointState) startOnce(ctx context.Context) error {
	e.startMu.Lock()

	// 场景A：已经有其他Goroutine正在拉起进程，当前请求挂起等待
	if e.starting {
		ch := e.startCh
		e.startMu.Unlock()
		select {
		case <-ch: // 等待拉起者关闭 channel 广播
			return e.lastStartErr
		case <-ctx.Done(): // 客户端主动断开或超时
			return ctx.Err()
		}
	}

	// Double-check 获取锁后再次确认进程是否已就绪（防止等锁期间被别人拉起）
	if e.engine.isReady(e.cfg.InternalPort) {
		e.startMu.Unlock()
		return nil
	}

	// 场景B：第一个到达的请求，占据拉起权限
	e.starting = true
	e.startCh = make(chan struct{})
	e.startMu.Unlock()

	// 触发 OS 进程拉起与 TCP 探活等待
	startCtx, cancel := context.WithTimeout(ctx, defaultStartTimeout)
	err := e.engine.spawnAndWait(startCtx, e.cfg)
	cancel()

	// 拉起完成，释放状态，广播环境所有正在阻塞的并发请求
	e.startMu.Lock()
	e.lastStartErr = err
	e.starting = false
	close(e.startCh) // 关闭 channel 瞬间环境所有挂起在 case <-ch 的 Goroutine
	e.startMu.Unlock()

	return err
}

func newReverseProxy(target string) *httputil.ReverseProxy {
	director := func(req *http.Request) {
		req.URL.Scheme = "http"
		req.URL.Host = target
		req.Host = target
	}
	return &httputil.ReverseProxy{
		Director: director,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "GopherMesh Proxy Error: "+err.Error(), http.StatusBadGateway)
		},
	}
}

func setCORSHeaders(w http.ResponseWriter, allowedOrigin string) {
	h := w.Header()
	if allowedOrigin != "" {
		h.Set("Access-Control-Allow-Origin", allowedOrigin)
	}
	h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "*")
	h.Set("Access-Control-Max-Age", "86400")
}
