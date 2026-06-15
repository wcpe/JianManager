package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/wxys233/JianManager/internal/worker/process"
)

func main() {
	// TODO: 加载 Worker Node 配置
	// TODO: 连接 Control Plane gRPC 并注册
	// TODO: 启动心跳上报
	// TODO: 启动 gRPC 服务器
	// TODO: 启动 WS 服务器

	slog.Info("Worker Node 启动")

	_ = process.NewManager("./servers")

	addr := ":9101"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}

	fmt.Printf("Worker Node 监听 %s\n", addr)
	// 实际的 gRPC 和 WS 服务器将在后续实现
}
