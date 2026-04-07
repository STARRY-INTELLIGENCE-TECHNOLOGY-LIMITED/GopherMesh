package mesh

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

type stdioProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	logBuf *LogBuffer
}

// isReady 执行纯粹的 L4 TCP 端口嗅探，判断下游进程是否已经绑定了端口
func (e *Engine) isReady(targetAddr string) bool {
	// 50ms 极速探测
	conn, err := net.DialTimeout("tcp", targetAddr, 50*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// spawnAndWait 负责拉起 OS 进程，并阻塞等待其 TCP 端口就绪
func (e *Engine) spawnAndWait(ctx context.Context, cfg BackendConfig) error {
	// 1. 组装执行命令
	cmd := exec.Command(cfg.Cmd, cfg.Args...)
	configureManagedProcess(cmd)

	// 日志劫持逻辑
	logBuf := e.getOrCreateLogBuffer(cfg.InternalPort, "====== GopherMesh: Process Cold Restarted ======\n")

	// 将子进程的标准输出/错误重定向到主进程，方便崩溃排查
	cmd.Stdout = io.MultiWriter(os.Stdout, logBuf)
	cmd.Stderr = io.MultiWriter(os.Stderr, logBuf)

	// 2. 触发操作系统拉起进程
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start cmd failed [%s]: %w", cfg.Cmd, err)
	}
	if err := registerManagedProcess(cmd); err != nil {
		log.Printf("[Warden] warning: register PID %d to managed process group failed: %v", cmd.Process.Pid, err)
	}

	log.Printf("[Warden] success exec cmd: %s (PID: %d), wait port %s ready...", cfg.Cmd, cmd.Process.Pid, cfg.InternalPort)

	// 3. 将进程注册到 Engine 的存活字典中，一边优雅退出时进行清理
	e.procMu.Lock()
	e.process[cfg.InternalPort] = &ProcessInfo{
		Cmd:       cmd,
		StartTime: time.Now(),
	}
	e.procMu.Unlock()

	// 4. 开启独立的 Goroutine 监听进程自然推出或异常崩溃
	go func() {
		err := cmd.Wait()
		e.procMu.Lock()
		// 只有当前字典里存的 Cmd 确实是我自己时，才进行删除，防止误删并发复活的新进程
		if info, exists := e.process[cfg.InternalPort]; exists && info.Cmd == cmd {
			delete(e.process, cfg.InternalPort)
		}
		e.procMu.Unlock()

		if err != nil {
			log.Printf("[Warden] warning: child process %s (PID: %d) exited with error: %v", cfg.Cmd, cmd.Process.Pid, err)
		} else {
			log.Printf("[Warden] child process %s exit normally", cfg.Cmd)
		}
	}()

	// 5. 进入 TCP 探活轮询与死循环防御
	host := cfg.InternalHost
	if strings.TrimSpace(host) == "" {
		host = defaultLocalHost
	}
	targetAddr := net.JoinHostPort(host, cfg.InternalPort)
	return e.waitForPort(ctx, targetAddr, cmd)
}

// waitForPort 每100ms探测一次端口，同时监控进程是否在启动期暴毙
func (e *Engine) waitForPort(ctx context.Context, targetAddr string, cmd *exec.Cmd) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// 触发冷启动断路器：超时未就绪，必须强杀刚才拉起的进程，防止产生僵尸进程
			log.Printf("[Warden] target %s ready timeout, kill zombie process PID: %d", targetAddr, cmd.Process.Pid)
			_ = killManagedCmd(cmd)
			return fmt.Errorf("timeout waiting for target %s to become ready: %w", targetAddr, ctx.Err())

		case <-ticker.C:
			// 防御机制：检查进程是否在启动的瞬间就崩溃
			if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
				return fmt.Errorf("process sudden exited, check environment and logs")
			}

			// 尝试 TCP 握手
			conn, err := net.DialTimeout("tcp", targetAddr, 50*time.Millisecond)
			if err == nil {
				// 握手成功，释放连接，连接正式就绪
				_ = conn.Close()
				return nil
			}
		}
	}
}

// spawnForSTDIO 拉起单次请求生命周期的子进程，并直接暴露 stdin/stdout 数据面。
// 上层可在此基础上实现原始 L4 字节流透传，或将 HTTP 请求适配后再桥接到 stdio。
func (e *Engine) spawnForSTDIO(logKey string, cfg BackendConfig, extraEnv []string) (*stdioProcess, error) {
	cmd := exec.Command(cfg.Cmd, cfg.Args...)
	configureManagedProcess(cmd)

	var logBuf *LogBuffer
	if strings.TrimSpace(logKey) != "" {
		logBuf = e.getOrCreateLogBuffer(logKey, "====== GopherMesh: Request-Driven Child Spawned ======\n")
		cmd.Stderr = io.MultiWriter(os.Stderr, logBuf)
	} else {
		cmd.Stderr = os.Stderr
	}
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("create stdin pipe for %s failed: %w", cfg.Cmd, err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("create stdout pipe for %s failed: %w", cfg.Cmd, err)
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("start stdio cmd failed [%s]: %w", cfg.Cmd, err)
	}
	if err := registerManagedProcess(cmd); err != nil {
		log.Printf("[Warden] warning: register PID %d to managed process group failed: %v", cmd.Process.Pid, err)
	}

	log.Printf("[Warden] spawned stdio cmd: %s (PID: %d)", cfg.Cmd, cmd.Process.Pid)

	return &stdioProcess{
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdout,
		logBuf: logBuf,
	}, nil
}

func (e *Engine) getOrCreateLogBuffer(key, divider string) *LogBuffer {
	e.procMu.Lock()
	defer e.procMu.Unlock()

	logBuf, exists := e.logBufs[key]
	if !exists {
		logBuf = NewLogBuffer(50)
		e.logBufs[key] = logBuf
		return logBuf
	}

	if divider != "" {
		_, _ = logBuf.Write([]byte(divider))
	}
	return logBuf
}
