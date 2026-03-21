package mesh

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"sync"
)

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

	ctx, cancel := context.WithTimeout(context.Background(), defaultStartTimeout)
	defer cancel()

	targetConn, backend, err := r.connectBackend(ctx)
	if err != nil {
		log.Printf("[L4 Proxy] backend selection failed [%s]: %v", r.cfg.Name, err)
		return
	}
	defer targetConn.Close()

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

func (r *routeState) connectBackend(ctx context.Context) (net.Conn, *backendState, error) {
	var errs []error
	for _, backend := range r.backendsInRoundRobinOrder() {
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
