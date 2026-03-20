package mesh

import (
	"context"
	"io"
	"log"
	"net"
	"sync"
)

// serveTCP 持续接收外部的裸 TCP 连接
func (e *Engine) serveTCP(ln net.Listener, state *endpointState) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			// 监听器被关闭，安全退出循环
			return
		}
		// 每个连接开启一个独立的 Goroutine 处理
		go state.handleTCPConnection(conn)
	}
}

// handleTCPConnection 处理单条 TCP 流量的拦截、拉起与透传
func (state *endpointState) handleTCPConnection(clientConn net.Conn) {
	defer clientConn.Close()

	// 1. 触发静默冷启动状态机（复用L7代理中的 startOnce 防惊群逻辑）
	if !state.engine.isReady(state.cfg.InternalPort) {
		// TCP 没有 HTTP REquest 的 Context，这里给冷启动过程一个独立的超时上下文
		ctx, cancel := context.WithTimeout(context.Background(), defaultStartTimeout)
		defer cancel()

		if err := state.startOnce(ctx); err != nil {
			log.Printf("[L4 Proxy] cool start failed [%s]: %v", state.cfg.Name, err)
			return
		}
	}

	// 2. 拨号本地依赖进程的真实端口
	targetAddr := "127.0.0.1:" + state.cfg.InternalPort
	targetConn, err := net.Dial("tcp", targetAddr)
	if err != nil {
		log.Printf("[L4 Proxy] can not connect target port %s: %v", targetAddr, err)
		return
	}
	defer targetConn.Close()

	// 3. 进入内核态零拷贝全双工对拷状态机
	var wg sync.WaitGroup
	wg.Add(2)

	// 通道 A: 客户端 -> 目标进程
	go func() {
		defer wg.Done()
		// 如果跑在Linux上，自动触发底层 splice(2) 系统调用
		_, _ = io.Copy(targetConn, clientConn)

		// TCP 半关闭处理：通知目标进程，客户端已经发送完毕，但仍然等待响应
		if tc, ok := targetConn.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}()

	// 通道 B：目标进程 -> 客户端
	go func() {
		defer wg.Done()
		_, _ = io.Copy(clientConn, targetConn)

		// 目标进程数据发送完毕，通知客户端
		if tc, ok := clientConn.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}()

	// 阻塞等待双向通道全部关闭，回收Goroutine
	wg.Wait()
}
