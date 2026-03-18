package mesh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"syscall"
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

// Engine 是 MeshEngine 接口的具体实现
type Engine struct {
	cfg               Config
	role              Role
	dashboardListener net.Listener

	// 代理服务器与端点状态映射
	endpointServers []*http.Server
	endpoints       map[string]*endpointState
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
	}, nil
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

		// 启动 HTTP L7 代理
		if cfg.Protocol != "tcp" {
			addr := "127.0.0.1:" + port
			server := &http.Server{Handler: http.HandlerFunc(state.handleRequest)}
			e.endpointServers = append(e.endpointServers, server)

			// 启动异步监听
			listener, err := net.Listen("tcp", addr)
			if err != nil {
				return fmt.Errorf("error listening on %s: %w", addr, err)
			}

			go func(srv *http.Server, ln net.Listener, p string) {
				_ = srv.Serve(ln)
			}(server, listener, cfg.Protocol)
		}
	}
	return nil
}

// Shutdown 触发安全退出，释放端口
func (e *Engine) Shutdown(ctx context.Context) error {
	if e.role != RoleMaster {
		return nil
	}

	var errs []error
	if e.dashboardListener != nil {
		_ = e.dashboardListener.Close()
	}

	// 优雅关闭所有的 HTTP 代理端口，拒绝新请求但等待进行中的请求处理完毕
	for _, srv := range e.endpointServers {
		if err := srv.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func (e *Engine) isReady(internalPort string) bool {
	return false
}

func (e *Engine) spawnAndWait(ctx context.Context, cfg EndpointConfig) error {
	return fmt.Errorf("未实现：进程拉起逻辑")
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
