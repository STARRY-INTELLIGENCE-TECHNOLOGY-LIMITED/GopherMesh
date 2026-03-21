package mesh

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const defaultStartTimeout = 30 * time.Second

// routeState 维护单个对外端口的路由规则、负载均衡器和后端冷启动状态。
type routeState struct {
	engine     *Engine
	publicPort string
	cfg        RouteConfig
	backends   []*backendState
	next       uint64
}

// backendState 维护单个后端进程的冷启动状态机。
type backendState struct {
	route *routeState
	cfg   BackendConfig

	startMu      sync.Mutex
	starting     bool
	startCh      chan struct{}
	lastStartErr error
}

func newRouteState(engine *Engine, publicPort string, cfg RouteConfig) *routeState {
	state := &routeState{
		engine:     engine,
		publicPort: publicPort,
		cfg:        cfg,
		backends:   make([]*backendState, 0, len(cfg.Backends)),
	}
	for _, backend := range cfg.Backends {
		state.backends = append(state.backends, &backendState{
			route: state,
			cfg:   backend,
		})
	}
	return state
}

// handleRequest 是所有外部流量的唯一 L7 入口
func (r *routeState) handleRequest(w http.ResponseWriter, req *http.Request) {
	// 1. 动态 CORS 与域名白名单校验
	reqOrigin := req.Header.Get("Origin")
	allowedOrigin := r.checkOrigin(reqOrigin)

	// 如果不在白名单中，且属于跨域请求，直接拒绝
	if reqOrigin != "" && allowedOrigin == "" {
		http.Error(w, "GopherMesh: Forbidden Origin", http.StatusForbidden)
		return
	}

	setCORSHeaders(w, allowedOrigin)
	if req.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// 2. 内部路由拦截（防环路保活）
	if isInternalRoute(r.cfg) {
		r.engine.handleHealthcheck(w, req)
		return
	}

	// 3. 选择后端并保留按请求触发的冷启动语义
	backend, err := r.selectBackend(req.Context())
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			status = http.StatusGatewayTimeout
		}
		http.Error(w, "GopherMesh Cold Start Failed: "+err.Error(), status)
		return
	}

	// 4. 后端就绪，无感透明透传（L7）
	newReverseProxy(backend.targetAddress()).ServeHTTP(w, req)
}

// checkOrigin 匹配配置中的 TrustedOrigins 白名单
func (r *routeState) checkOrigin(reqOrigin string) string {
	for _, origin := range r.engine.cfg.TrustedOrigins {
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

func (r *routeState) selectBackend(ctx context.Context) (*backendState, error) {
	if len(r.backends) == 0 {
		return nil, errors.New("no backend configured")
	}

	var errs []error
	for _, backend := range r.backendsInRoundRobinOrder() {
		if err := backend.ensureReady(ctx); err != nil {
			errs = append(errs, fmt.Errorf("%s[%s]: %w", backend.cfg.Name, backend.cfg.InternalPort, err))
			continue
		}
		return backend, nil
	}

	return nil, errors.Join(errs...)
}

func (r *routeState) backendsInRoundRobinOrder() []*backendState {
	if len(r.backends) == 0 {
		return nil
	}

	start := int(atomic.AddUint64(&r.next, 1)-1) % len(r.backends)
	ordered := make([]*backendState, 0, len(r.backends))
	for i := 0; i < len(r.backends); i++ {
		ordered = append(ordered, r.backends[(start+i)%len(r.backends)])
	}
	return ordered
}

func (b *backendState) targetAddress() string {
	return "127.0.0.1:" + b.cfg.InternalPort
}

func (b *backendState) ensureReady(ctx context.Context) error {
	if b.route.engine.isReady(b.cfg.InternalPort) {
		return nil
	}
	return b.startOnce(ctx)
}

// startOnce 保证海量并发请求下，同一个后端进程只会被 spawn 一次。
func (b *backendState) startOnce(ctx context.Context) error {
	b.startMu.Lock()

	// 场景A：已经有其他 Goroutine 正在拉起后端，当前请求挂起等待
	if b.starting {
		ch := b.startCh
		b.startMu.Unlock()
		select {
		case <-ch:
			b.startMu.Lock()
			err := b.lastStartErr
			b.startMu.Unlock()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// Double-check 获取锁后再次确认进程是否已就绪（防止等锁期间被别人拉起）
	if b.route.engine.isReady(b.cfg.InternalPort) {
		b.startMu.Unlock()
		return nil
	}

	// 场景B：第一个到达的请求，占据拉起权限
	b.starting = true
	b.startCh = make(chan struct{})
	b.startMu.Unlock()

	startCtx, cancel := context.WithTimeout(ctx, defaultStartTimeout)
	err := b.route.engine.spawnAndWait(startCtx, b.cfg)
	cancel()

	b.startMu.Lock()
	b.lastStartErr = err
	b.starting = false
	close(b.startCh)
	b.startMu.Unlock()

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
