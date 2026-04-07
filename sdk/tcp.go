package mesh

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"os/exec"
	"sync"
	"time"
)

const stdioExitGracePeriod = 500 * time.Millisecond

// serveTCP 持续接收外部的裸 TCP 连接
func (e *Engine) serveTCP(ln net.Listener, route *routeState) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Temporary() {
				log.Printf("[L4 Proxy] temporary accept error on %s: %v", route.publicPort, err)
				continue
			}
			return
		}
		go route.handleTCPConnection(conn)
	}
}

// handleTCPConnection 处理单条 TCP 流量的拦截、拉起与透传
func (r *routeState) handleTCPConnection(clientConn net.Conn) {
	defer clientConn.Close()

	if r.cfg.Protocol == protocolSTDIO {
		r.handleSTDIOClient(clientConn)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultStartTimeout)
	defer cancel()

	targetConn, backend, err := r.connectBackend(ctx, clientConn.RemoteAddr().String())
	if err != nil {
		log.Printf("[L4 Proxy] backend selection failed [%s]: %v", r.cfg.Name, err)
		return
	}
	defer targetConn.Close()
	backend.acquire()
	defer backend.release()

	log.Printf("[L4 Proxy] %s -> backend %s (%s)", r.publicPort, backend.cfg.Name, backend.cfg.InternalPort)

	var wg sync.WaitGroup
	wg.Add(2)

	// 通道 A: 客户端 -> 目标进程
	go func() {
		defer wg.Done()
		_, _ = io.Copy(targetConn, clientConn)

		if tc, ok := targetConn.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}()

	// 通道 B：目标进程 -> 客户端
	go func() {
		defer wg.Done()
		_, _ = io.Copy(clientConn, targetConn)

		if tc, ok := clientConn.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}()

	wg.Wait()
}

func (r *routeState) handleSTDIOStreamConnection(clientConn net.Conn, clientReader io.Reader) {
	proc, backend, err := r.spawnSTDIOBackend(clientConn.RemoteAddr().String(), nil)
	if err != nil {
		log.Printf("[L4 STDIO] backend spawn failed [%s]: %v", r.cfg.Name, err)
		return
	}

	backend.acquire()
	defer backend.release()

	log.Printf("[L4 STDIO] %s -> backend %s (PID: %d)", r.publicPort, backend.cfg.Name, proc.cmd.Process.Pid)

	stdoutDone := make(chan struct{})
	copyErrs := make(chan error, 2)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()

		_, err := io.Copy(proc.stdin, clientReader)
		_ = proc.stdin.Close()

		if err != nil {
			select {
			case <-stdoutDone:
				err = nil
			default:
				_ = killManagedCmd(proc.cmd)
			}
		}

		copyErrs <- err
	}()

	go func() {
		defer wg.Done()

		stdoutReader := io.Reader(proc.stdout)
		if proc.logBuf != nil {
			stdoutReader = io.TeeReader(proc.stdout, proc.logBuf)
		}
		_, err := io.Copy(clientConn, stdoutReader)
		close(stdoutDone)
		_ = proc.stdout.Close()

		if tcpConn, ok := clientConn.(*net.TCPConn); ok {
			_ = tcpConn.CloseWrite()
		}
		_ = clientConn.SetReadDeadline(time.Now())

		copyErrs <- err
	}()

	wg.Wait()
	close(copyErrs)

	for copyErr := range copyErrs {
		if copyErr != nil && !errors.Is(copyErr, net.ErrClosed) {
			log.Printf("[L4 STDIO] stream copy warning [%s -> %s]: %v", r.publicPort, backend.cfg.Name, copyErr)
		}
	}

	if err := waitForSTDIOExit(proc.cmd); err != nil {
		log.Printf("[L4 STDIO] child process %s (PID: %d) exited with error: %v", backend.cfg.Name, proc.cmd.Process.Pid, err)
	}
}

func (r *routeState) connectBackend(ctx context.Context, remoteAddr string) (net.Conn, *backendState, error) {
	var errs []error
	for _, backend := range r.backendsInLoadBalanceOrder(remoteAddr) {
		if err := backend.ensureReady(ctx); err != nil {
			errs = append(errs, err)
			continue
		}

		conn, err := net.Dial("tcp", backend.targetAddress())
		if err != nil {
			errs = append(errs, err)
			continue
		}
		return conn, backend, nil
	}

	return nil, nil, errors.Join(errs...)
}

func (r *routeState) spawnSTDIOBackend(remoteAddr string, extraEnv []string) (*stdioProcess, *backendState, error) {
	var errs []error
	for _, backend := range r.backendsInLoadBalanceOrder(remoteAddr) {
		proc, err := r.engine.spawnForSTDIO(backend.ref(), backend.cfg, extraEnv)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		return proc, backend, nil
	}

	if len(errs) == 0 {
		errs = append(errs, errors.New("no backend configured"))
	}
	return nil, nil, errors.Join(errs...)
}

func waitForSTDIOExit(cmd *exec.Cmd) error {
	if cmd == nil {
		return nil
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	select {
	case err := <-waitCh:
		return err
	case <-time.After(stdioExitGracePeriod):
		_ = killManagedCmd(cmd)
		return <-waitCh
	}
}
