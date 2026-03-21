package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	mesh "github.com/SUTFutureCoder/gophermesh/sdk"
)

func main() {
	configPath := flag.String("config", "config.json", "Path to the GopherMesh config file")
	flag.Parse()

	// 1. 加载配置
	cfg, err := mesh.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	// 2. 初始化核心引擎
	engine, err := mesh.NewEngine(cfg)
	if err != nil {
		log.Fatalf("init engine: %v", err)
	}

	// 3. 监听系统级别中断信号（Ctrl+C, SIGTERM）
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log.Printf("starting mesh engine... current role: [%s]", engine.Role())

	// 4. 阻塞运行，直到收到系统中断信号
	if err := engine.Run(ctx); err != nil {
		log.Printf("mesh engine terminated: %v", err)
	}

	// 5. 触发优雅退出，给予底层Python/C++进程5秒善后时间
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := engine.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("mesh engine terminated error: %v", err)
	}

	log.Printf("mesh engine safe terminated")
}
