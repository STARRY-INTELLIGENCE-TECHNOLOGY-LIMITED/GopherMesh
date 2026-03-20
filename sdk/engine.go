package mesh

import (
    "context"
    "errors"
    "fmt"
    "log"
    "net"
    "net/http"
    "os/exec"
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

type ProcessInfo struct {
    Cmd       *exec.Cmd
    StartTime time.Time
}

// Engine 是 MeshEngine 接口的具体实现
type Engine struct {
    cfg               Config
    role              Role
    dashboardListener net.Listener

    // 代理服务器与端点状态映射
    endpointServers []*http.Server
    endpoints       map[string]*endpointState
    tcpListener     []net.Listener // 存放 TCP 监听器

    // Warden 进程字典和并发锁
    procMu  sync.Mutex
    process map[string]*ProcessInfo

    // 独立于进程生命周期的日志缓存字典
    logBufs map[string]*LogBuffer
}

// NewEngine 负责初始化并探测节点角色
func NewEngine(cfg Config) (MeshEngine, error) {
    listener, role, err := detectRole(cfg.DashboardPort)
    if err != nil {
        return nil, fmt.Errorf("error detecting dashboard role: %w", err)
    }
    return &Engine{
        cfg:               cfg,
        role:              role,
        dashboardListener: listener,
        process:           make(map[string]*ProcessInfo),
        logBufs:           make(map[string]*LogBuffer),
    }, nil
}

func (e *Engine) GetLogs(port string) []string {
    e.procMu.Lock()
    logBuf, exists := e.logBufs[port]
    e.procMu.Unlock()

    if !exists {
        return []string{"[GopherMesh] No logs available or process not spawned yet."}
    }
    return logBuf.Lines()
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

    // 主控节点：启动独立模块的 Dashboard API
    go func() {
        log.Printf("[Dashboard] start API server on %s", e.dashboardListener.Addr().String())
        if err := dashboard.Serve(e.dashboardListener, e); err != nil && err != http.ErrServerClosed {
            log.Printf("[Dashboard] failed to start dashboard server: %v", err)
        }
    }()

    // 主控节点：启动所有监听端口的反向代理
    if err := e.startProxies(); err != nil {
        return err
    }

    // 阻塞当前Goroutine，直到main函数中的context因为Ctrl+C被取消
    <-ctx.Done()
    return nil
}

// startProxies 遍历配置中的断点，启动 HTTP 拦截服务器
func (e *Engine) startProxies() error {
    e.endpoints = make(map[string]*endpointState)

    for port, cfg := range e.cfg.Endpoints {
        state := newEndpointState(e, port, cfg)
        e.endpoints[port] = state

        addr := "127.0.0.1:" + port
        listener, err := net.Listen("tcp", addr)
        if err != nil {
            return fmt.Errorf("error listening on %s: %w", addr, err)
        }

        if cfg.Protocol == "tcp" {
            // L4 零拷贝 TCP 代理
            e.tcpListener = append(e.tcpListener, listener)
            go e.serveTCP(listener, state)
            log.Printf("[Engine] start L4 TCP listener on %s -> internal port %s", addr, cfg.InternalPort)
        }

        if cfg.Protocol != "tcp" {
            // L7 透明代理
            server := &http.Server{Handler: http.HandlerFunc(state.handleRequest)}
            e.endpointServers = append(e.endpointServers, server)

            go func(srv *http.Server, ln net.Listener) {
                _ = srv.Serve(ln)
            }(server, listener)
            log.Printf("[Engine] start L7 HTTP listener on %s -> internal port %s", addr, cfg.InternalPort)
        }
    }
    return nil
}

// GetStatus 实现 dashboard.MeshState 接口，收集底层进程快照
func (e *Engine) GetStatus() map[string]dashboard.EndpointStatus {
    e.procMu.Lock()
    defer e.procMu.Unlock()

    result := make(map[string]dashboard.EndpointStatus)
    for port, cfg := range e.cfg.Endpoints {
        if cfg.Cmd == "" || strings.ToLower(cfg.Cmd) == "internal" {
            continue // 过滤内部健康检查接口，不展示在业务状态里
        }

        info, exists := e.process[cfg.InternalPort]
        status := dashboard.EndpointStatus{
            Name:         cfg.Name,
            InternalPort: cfg.InternalPort,
            Status:       "Dormant", // 默认休眠状态
        }

        // 检查进程是否真实存活
        if exists && info.Cmd.Process != nil {
            status.Status = "Running"
            status.PID = info.Cmd.Process.Pid
            // 格式化运行时间（例如 "2m30s"）
            status.Uptime = time.Since(info.StartTime).Round(time.Second).String()
        }

        result[port] = status
    }
    return result
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

    // 2.1 优雅关闭所有的 HTTP 代理端口，拒绝新请求但等待进行中的请求处理完毕
    for _, srv := range e.endpointServers {
        if err := srv.Shutdown(ctx); err != nil {
            errs = append(errs, err)
        }
    }

    // 2.2 关闭所有 L4 TCP 监听器，中断新连接进入
    for _, ln := range e.tcpListener {
        if err := ln.Close(); err != nil {
            errs = append(errs, err)
        }
    }

    // 3. 安全终结所有由 GoperMesh 拉起的业务子进程
    e.procMu.Lock()
    for port, info := range e.process {
        if info.Cmd.Process != nil {
            log.Printf("[Shutdown] killing process PID: %d Port: %s", info.Cmd.Process.Pid, port)
        }
        if err := info.Cmd.Process.Kill(); err != nil {
            errs = append(errs, fmt.Errorf("force kill PID: %d failed: %v", info.Cmd.Process.Pid, err))
        }
    }
    e.procMu.Unlock()

    if len(errs) > 0 {
        return errors.Join(errs...)
    }
    return nil
}

func (e *Engine) handleHealthcheck(w http.ResponseWriter, r *http.Request) {
    w.WriteHeader(http.StatusOK)
    _, _ = w.Write([]byte(`{"status": "ok", "mesh": "running"}`))
}

// detectRole 尝试绑定 Dashboard 端口已决定当前进程的角色
func detectRole(port string) (net.Listener, Role, error) {
    addr := "127.0.0.1:" + port
    listener, err := net.Listen("tcp", addr)

    // 1. 绑定成功，我们是Master节点
    if err == nil {
        return listener, RoleMaster, nil
    }

    // 2. 绑定失败，检查是否是因为端口被占用（EADDRINUSE）
    if isAddrInUse(err) {
        return nil, RoleWorker, nil
    }

    // 3. 其他知名网络错误（如权限不足）
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
