package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/SUTFutureCoder/gophermesh/dashboard"
)

// Role 定义节点的运行角色
type Role string

const (
	RoleMaster Role = "master" // 主控节点：负责监听、劫持流量、按需静默拉起子进程
	RoleWorker Role = "worker" // 工作节点：被拉起的业务进程（当作为SDK引入时使用）
)

// MeshEngine 定义了GopherMesh核心引擎的生命周期契约
type MeshEngine interface {
	// Role 返回当前实例运行的身份
	Role() Role

	// Run 启动代理或注册工作节点，并阻塞直到ctx被取消
	Run(ctx context.Context) error

	// Shutdown 触发安全退出，释放端口并优雅中介所有托管的子进程
	Shutdown(ctx context.Context) error
}

type EngineOptions struct {
	NoDashboard bool
	Healthz     HealthzOptions
}

type ProcessInfo struct {
	Cmd       *exec.Cmd
	StartTime time.Time
}

type routeRuntime struct {
	routeServers []*http.Server
	routes       map[string]*routeState
	tcpListeners []net.Listener
}

// Engine 是 MeshEngine 接口的具体实现
type Engine struct {
	role              Role
	dashboardListener net.Listener
	options           EngineOptions
	healthzOptions    HealthzOptions

	cfgMu sync.RWMutex
	cfg   Config

	// 代理服务器与路由状态映射
	routeServers []*http.Server
	routes       map[string]*routeState
	tcpListeners []net.Listener

	// Warden 进程字典和并发锁
	procMu  sync.Mutex
	process map[string]*ProcessInfo

	// 独立于进程生命周期的日志缓存字典
	logBufs map[string]*LogBuffer

	// 热重载防并发锁
	reloadMu sync.Mutex
}

var dashboardBrowserOpener = openBrowser

// NewEngine 负责初始化并探测节点角色
func NewEngine(cfg Config) (MeshEngine, error) {
	return NewEngineWithOptions(cfg, EngineOptions{})
}

// NewEngineWithOptions 允许调用方覆盖 Dashboard 自动打开等运行行为。
func NewEngineWithOptions(cfg Config, opts EngineOptions) (MeshEngine, error) {
	normalized, err := cfg.Normalize()
	if err != nil {
		return nil, fmt.Errorf("normalize config: %w", err)
	}

	listener, role, err := detectRole(normalized.DashboardHost, normalized.DashboardPort)
	if err != nil {
		return nil, fmt.Errorf("error detecting dashboard role: %w", err)
	}
	return &Engine{
		cfg:               normalized,
		role:              role,
		dashboardListener: listener,
		options:           opts,
		healthzOptions:    normalizeHealthzOptions(opts.Healthz),
		process:           make(map[string]*ProcessInfo),
		logBufs:           make(map[string]*LogBuffer),
	}, nil
}

func (e *Engine) GetLogs(ref string) []string {
	lookupKeys := e.logLookupKeys(ref)

	e.procMu.Lock()
	defer e.procMu.Unlock()

	for _, key := range lookupKeys {
		logBuf, exists := e.logBufs[key]
		if exists {
			return logBuf.Lines()
		}
	}

	return []string{"[GopherMesh] No logs available or process not spawned yet."}
}

// Role 返回当前实例运行的身份
func (e *Engine) Role() Role {
	return e.role
}

// Run 启动引擎并阻塞，直到ctx被取消
func (e *Engine) Run(ctx context.Context) error {
	if e.role == RoleWorker {
		// 如果是Worker模式，后续将在这里实现向Master注册的逻辑
		<-ctx.Done()
		return nil
	}

	cfg := e.configSnapshot()

	// 主控节点：启动独立模块的 Dashboard API
	go func() {
		host := cfg.DashboardHost
		if host == "0.0.0.0" || host == "" {
			host = defaultLocalHost
		}
		dashboardURL := fmt.Sprintf("http://%s:%s", host, cfg.DashboardPort)

		log.Printf("[Dashboard] start API server on %s", dashboardURL)

		if !e.options.NoDashboard {
			go dashboardBrowserOpener(dashboardURL)
		}

		if err := dashboard.Serve(e.dashboardListener, e); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[Dashboard] failed to start dashboard server: %v", err)
		}
	}()

	// 主控节点：启动所有监听端口的反向代理
	if err := e.startRoutes(); err != nil {
		return err
	}

	// 阻塞当前 Goroutine，直到 main 函数中的 context 因为 Ctrl+C 被取消
	<-ctx.Done()
	return nil
}

// startRoutes 遍历配置中的路由并启动对应的监听器
func (e *Engine) startRoutes() error {
	runtimeState, err := e.startRouteRuntime(e.configSnapshot())
	if err != nil {
		return err
	}
	e.installRouteRuntime(runtimeState)
	return nil
}

func (e *Engine) startRouteRuntime(cfg Config) (*routeRuntime, error) {
	runtimeState := &routeRuntime{
		routes: make(map[string]*routeState),
	}

	for port, routeCfg := range cfg.Routes {
		state := newRouteState(e, port, routeCfg)
		runtimeState.routes[port] = state

		addr := net.JoinHostPort(defaultLocalHost, port)
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = runtimeState.shutdown(rollbackCtx)
			return nil, fmt.Errorf("error listening on %s: %w", addr, err)
		}

		if routeUsesRawTCPListener(routeCfg) {
			runtimeState.tcpListeners = append(runtimeState.tcpListeners, listener)
			go e.serveTCP(listener, state)
			if routeCfg.Protocol == protocolSTDIO {
				log.Printf("[Engine] start L4 STDIO listener on %s with %d backend(s)", addr, len(routeCfg.Backends))
			} else {
				log.Printf("[Engine] start L4 TCP listener on %s with %d backend(s)", addr, len(routeCfg.Backends))
			}
			continue
		}

		server := &http.Server{Handler: http.HandlerFunc(state.handleRequest)}
		runtimeState.routeServers = append(runtimeState.routeServers, server)

		go func(srv *http.Server, ln net.Listener) {
			_ = srv.Serve(ln)
		}(server, listener)
		log.Printf("[Engine] start L7 HTTP listener on %s with %d backend(s)", addr, len(routeCfg.Backends))
	}
	return runtimeState, nil
}

func (e *Engine) replaceRouteRuntime(currentCfg, nextCfg Config) error {
	currentRuntime := e.snapshotRouteRuntime()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := currentRuntime.shutdown(shutdownCtx); err != nil {
		log.Printf("[Reload] warning: shutdown existing listeners returned: %v", err)
	}
	e.installRouteRuntime(nil)

	nextRuntime, err := e.startRouteRuntime(nextCfg)
	if err != nil {
		restoreRuntime, restoreErr := e.startRouteRuntime(currentCfg)
		if restoreErr != nil {
			e.setConfig(currentCfg)
			return errors.Join(
				fmt.Errorf("activate config failed: %w", err),
				fmt.Errorf("restore previous listeners failed: %w", restoreErr),
			)
		}
		e.installRouteRuntime(restoreRuntime)
		e.setConfig(currentCfg)
		return fmt.Errorf("activate config failed: %w", err)
	}

	e.installRouteRuntime(nextRuntime)
	e.setConfig(nextCfg)
	return nil
}

func (e *Engine) snapshotRouteRuntime() routeRuntime {
	return routeRuntime{
		routeServers: append([]*http.Server(nil), e.routeServers...),
		routes:       e.routes,
		tcpListeners: append([]net.Listener(nil), e.tcpListeners...),
	}
}

func (e *Engine) installRouteRuntime(runtimeState *routeRuntime) {
	if runtimeState == nil {
		e.routeServers = nil
		e.routes = nil
		e.tcpListeners = nil
		return
	}
	e.routeServers = runtimeState.routeServers
	e.routes = runtimeState.routes
	e.tcpListeners = runtimeState.tcpListeners
}

func (r routeRuntime) shutdown(ctx context.Context) error {
	var errs []error

	for _, srv := range r.routeServers {
		if err := srv.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	for _, ln := range r.tcpListeners {
		if err := ln.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func (e *Engine) configSnapshot() Config {
	e.cfgMu.RLock()
	defer e.cfgMu.RUnlock()
	return e.cfg.clone()
}

func (e *Engine) trustedOriginsSnapshot() []string {
	e.cfgMu.RLock()
	defer e.cfgMu.RUnlock()
	return append([]string(nil), e.cfg.TrustedOrigins...)
}

func (e *Engine) setConfig(cfg Config) {
	e.cfgMu.Lock()
	e.cfg = cfg
	e.cfgMu.Unlock()
}

// GetStatus 实现 dashboard.MeshState 接口，收集底层进程快照
func (e *Engine) GetStatus() map[string]dashboard.RouteStatus {
	cfg := e.configSnapshot()

	e.procMu.Lock()
	processes := make(map[string]*ProcessInfo, len(e.process))
	for port, info := range e.process {
		processes[port] = info
	}
	e.procMu.Unlock()

	result := make(map[string]dashboard.RouteStatus)
	for port, route := range cfg.Routes {
		if isInternalRoute(route) {
			continue
		}

		status := dashboard.RouteStatus{
			Name:        route.Name,
			Protocol:    route.Protocol,
			StdioMode:   route.StdioMode,
			LoadBalance: route.LoadBalance,
			Backends:    make([]dashboard.BackendStatus, 0, len(route.Backends)),
		}

		for index, backend := range route.Backends {
			backendStatus := dashboard.BackendStatus{
				Ref:          makeBackendRef(port, index),
				Name:         backend.Name,
				InternalHost: backend.InternalHost,
				InternalPort: backend.InternalPort,
				Status:       "Dormant",
			}

			if route.Protocol == protocolSTDIO {
				backendStatus.Status = "Request-Driven"
			} else {
				targetAddr := net.JoinHostPort(backend.InternalHost, backend.InternalPort)

				if info, exists := processes[backend.InternalPort]; exists && info.Cmd.Process != nil {
					backendStatus.Status = "Running"
					backendStatus.PID = info.Cmd.Process.Pid
					backendStatus.Uptime = time.Since(info.StartTime).Round(time.Second).String()
				} else if e.isReady(targetAddr) {
					backendStatus.Status = "Running"
				}
			}

			status.Backends = append(status.Backends, backendStatus)
		}

		result[port] = status
	}
	return result
}

// KillProcess 实现 dashboard.MeshState 接口，支持手动kill底层进程
func (e *Engine) KillProcess(ref string) error {
	processKey := strings.TrimSpace(ref)
	if _, _, backend, ok := e.resolveBackendRef(ref); ok {
		if strings.TrimSpace(backend.InternalPort) == "" {
			return fmt.Errorf("backend %s is request-driven and has no persistent process to kill", ref)
		}
		processKey = backend.InternalPort
	}

	e.procMu.Lock()
	info, exists := e.process[processKey]
	if !exists {
		e.procMu.Unlock()
		return fmt.Errorf("target port %s not found or suspended", processKey)
	}

	if info.Cmd.Process == nil {
		e.procMu.Unlock()
		return fmt.Errorf("target port %s process is not initialized", processKey)
	}

	cmd := info.Cmd
	pid := cmd.Process.Pid
	e.procMu.Unlock()

	log.Printf("[Dashboard] force kill process PID: %d Port: %s", pid, processKey)

	if err := killManagedCmd(cmd); err != nil {
		return fmt.Errorf("force kill process failed: %w", err)
	}

	// 立即清理快照，避免 Dashboard 在 Wait goroutine 回收前继续显示旧 PID/Uptime。
	e.procMu.Lock()
	if current, exists := e.process[processKey]; exists && current.Cmd == cmd {
		delete(e.process, processKey)
	}
	e.procMu.Unlock()

	return nil
}

// Shutdown 触发安全退出，释放端口
func (e *Engine) Shutdown(ctx context.Context) error {
	if e.role != RoleMaster {
		return nil
	}

	var errs []error

	// 1. 释放控制端口
	if e.dashboardListener != nil {
		_ = e.dashboardListener.Close()
	}

	if err := e.snapshotRouteRuntime().shutdown(ctx); err != nil {
		errs = append(errs, err)
	}

	// 3. 安全终结所有由 GopherMesh 拉起的业务子进程
	e.procMu.Lock()
	processes := make(map[string]*ProcessInfo, len(e.process))
	for port, info := range e.process {
		processes[port] = info
	}
	e.procMu.Unlock()

	for port, info := range processes {
		if info.Cmd.Process == nil {
			continue
		}
		log.Printf("[Shutdown] killing process PID: %d Port: %s", info.Cmd.Process.Pid, port)

		if err := killManagedCmd(info.Cmd); err != nil {
			errs = append(errs, fmt.Errorf("force kill PID: %d failed: %v", info.Cmd.Process.Pid, err))
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func (e *Engine) handleHealthcheck(w http.ResponseWriter, r *http.Request) {
	WriteHealthz(w, e.healthzOptions)
}

// detectRole 尝试绑定 Dashboard 端口以决定当前进程的角色
func detectRole(host, port string) (net.Listener, Role, error) {
	addr := net.JoinHostPort(host, port)
	listener, err := net.Listen("tcp", addr)

	// 1. 绑定成功，我们是 Master 节点
	if err == nil {
		return listener, RoleMaster, nil
	}

	// 2. 绑定失败，检查是否是因为端口被占用（EADDRINUSE）
	if isAddrInUse(err) {
		return nil, RoleWorker, nil
	}

	// 3. 其他网络错误（如权限不足）
	return nil, "", err
}

// isAddrInUse 跨平台判断端口是否已被占用
func isAddrInUse(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if errors.Is(opErr.Err, syscall.EADDRINUSE) {
			return true
		}
	}
	return strings.Contains(strings.ToLower(err.Error()), "address already in use") ||
		strings.Contains(strings.ToLower(err.Error()), "bind")
}

// GetConfigJSON 返回当前 Routes 的 JSON 字符串
func (e *Engine) GetConfigJSON() []byte {
	// 只暴露 Routes 给前端编辑，保护核心的 DashboardHost 等配置
	data, _ := json.MarshalIndent(e.configSnapshot().Routes, "", "  ")
	return data
}

// ReloadConfig 接收前端传来的新 JSON，验证、落盘、并执行平滑重载
func (e *Engine) ReloadConfig(rawJSON []byte) error {
	e.reloadMu.Lock()
	defer e.reloadMu.Unlock()

	// 1. 解析前端传来的纯 Routes JSON
	var newRoutes map[string]RouteConfig
	if err := json.Unmarshal(rawJSON, &newRoutes); err != nil {
		return fmt.Errorf("parse JSON failed: %w", err)
	}

	// 2. 组装新配置并验证合法性
	oldCfg := e.configSnapshot()
	newCfg := oldCfg
	newCfg.Routes = newRoutes
	normalized, err := newCfg.Normalize()
	if err != nil {
		return fmt.Errorf("failed to check new config: %w", err)
	}

	log.Printf("[Dashboard] hot reloading...")

	// 3. 先切换 runtime；若新配置无法接管监听器，会自动恢复旧配置
	if err := e.replaceRouteRuntime(oldCfg, normalized); err != nil {
		return fmt.Errorf("bind new port failed: %w", err)
	}

	// 4. 切换成功后再原子落盘；若落盘失败，回滚到旧配置，避免内存与磁盘状态分叉
	if normalized.ConfigPath != "" {
		if err := SaveConfig(normalized.ConfigPath, normalized); err != nil {
			if rollbackErr := e.replaceRouteRuntime(normalized, oldCfg); rollbackErr != nil {
				return errors.Join(
					fmt.Errorf("save config failed: %w", err),
					fmt.Errorf("rollback runtime failed: %w", rollbackErr),
				)
			}
			return fmt.Errorf("save config failed: %w", err)
		}
	}

	e.killRemovedProcesses(normalized)

	log.Printf("[Dashboard] hot reload success")
	return nil
}

func (e *Engine) killRemovedProcesses(newCfg Config) {
	activePorts := make(map[string]struct{})
	for _, route := range newCfg.Routes {
		for _, backend := range route.Backends {
			if strings.TrimSpace(backend.InternalPort) != "" {
				activePorts[backend.InternalPort] = struct{}{}
			}
		}
	}

	e.procMu.Lock()
	processes := make(map[string]*ProcessInfo)
	for port, info := range e.process {
		if _, keep := activePorts[port]; !keep {
			processes[port] = info
		}
	}
	e.procMu.Unlock()

	for port, info := range processes {
		log.Printf("[Reload] kill abandoned zombie Port: %s", port)
		if err := killManagedCmd(info.Cmd); err != nil {
			log.Printf("[Reload] warning: kill abandoned Port %s failed: %v", port, err)
		}
	}
}

func (e *Engine) logLookupKeys(ref string) []string {
	keys := make([]string, 0, 2)
	ref = strings.TrimSpace(ref)
	if ref != "" {
		keys = append(keys, ref)
	}

	if _, _, backend, ok := e.resolveBackendRef(ref); ok {
		if internalPort := strings.TrimSpace(backend.InternalPort); internalPort != "" && internalPort != ref {
			keys = append(keys, internalPort)
		}
	}

	return keys
}

func (e *Engine) resolveBackendRef(ref string) (string, int, BackendConfig, bool) {
	publicPort, index, ok := parseBackendRef(ref)
	if !ok {
		return "", 0, BackendConfig{}, false
	}

	cfg := e.configSnapshot()
	route, exists := cfg.Routes[publicPort]
	if !exists || index < 0 || index >= len(route.Backends) {
		return "", 0, BackendConfig{}, false
	}

	return publicPort, index, route.Backends[index], true
}

func openBrowser(url string) {
	var err error
	switch runtime.GOOS {
	case "linux":
		err = exec.Command("xdg-open", url).Start()
	case "windows":
		err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
	}
	// 核心诉求：无头环境弹不出来也无所谓，直接屏蔽错误，绝不影响主线程
	_ = err
}

func killManagedCmd(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	if runtime.GOOS == "windows" {
		pid := strconv.Itoa(cmd.Process.Pid)
		output, err := exec.Command("taskkill", "/PID", pid, "/T", "/F").CombinedOutput()
		if err == nil {
			return nil
		}

		lower := strings.ToLower(err.Error() + " " + string(output))
		if strings.Contains(lower, "not found") || strings.Contains(lower, "no running instance") {
			return nil
		}
		return fmt.Errorf("taskkill PID %s failed: %w: %s", pid, err, strings.TrimSpace(string(output)))
	}

	err := cmd.Process.Kill()
	if err != nil && !errors.Is(err, os.ErrProcessDone) && !strings.Contains(strings.ToLower(err.Error()), "invalid argument") {
		return err
	}
	return nil
}
