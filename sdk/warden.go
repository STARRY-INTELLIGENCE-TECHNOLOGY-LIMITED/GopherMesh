package mesh

import (
    "context"
    "fmt"
    "io"
    "log"
    "net"
    "os"
    "os/exec"
    "time"
)

// isReady 执行纯粹的 L4 TCP 端口嗅探，判断下游进程是否已经绑定了端口
func (e *Engine) isReady(internalPort string) bool {
    target := "127.0.0.1:" + internalPort
    // 50ms 极速探测
    conn, err := net.DialTimeout("tcp", target, 50*time.Millisecond)
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

    // 日志劫持逻辑
    e.procMu.Lock()
    logBuf, exists := e.logBufs[cfg.InternalPort]
    if !exists {
        logBuf = NewLogBuffer(50) // 最多保留 50 行
        e.logBufs[cfg.InternalPort] = logBuf
    } else {
        // 如果冷启动复活，插入一条分割线
        logBuf.Write([]byte("====== GopherMesh: Process Cold Restarted ======\n"))
    }
    e.procMu.Unlock()

    // 将子进程的标准输出/错误重定向到主进程，方便崩溃排查
    cmd.Stdout = io.MultiWriter(os.Stdout, logBuf)
    cmd.Stderr = io.MultiWriter(os.Stderr, logBuf)

    // 2. 触发操作系统拉起进程
    if err := cmd.Start(); err != nil {
        return fmt.Errorf("start cmd failed [%s]: %w", cfg.Cmd, err)
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
    return e.waitForPort(ctx, cfg.InternalPort, cmd)
}

// waitForPort 每100ms探测一次端口，同时监控进程是否在启动期暴毙
func (e *Engine) waitForPort(ctx context.Context, port string, cmd *exec.Cmd) error {
    ticker := time.NewTicker(100 * time.Millisecond)
    defer ticker.Stop()

    target := "127.0.0.1:" + port

    for {
        select {
        case <-ctx.Done():
            // 触发冷启动断路器：超时未就绪，必须强杀刚才拉起的进程，防止产生僵尸进程
            log.Printf("[Warden] port %s ready timeout, kill zombie process PID: %d", port, cmd.Process.Pid)
            _ = cmd.Process.Kill()
            return fmt.Errorf("timeout waiting for port %s to become ready: %w", port, ctx.Err())

        case <-ticker.C:
            // 防御机制：检查进程是否在启动的瞬间就崩溃
            if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
                return fmt.Errorf("process sudden exited, check environment and logs")
            }

            // 尝试 TCP 握手
            conn, err := net.DialTimeout("tcp", target, 50*time.Millisecond)
            if err == nil {
                // 握手成功，释放连接，连接正式就绪
                _ = conn.Close()
                return nil
            }
        }
    }
}
