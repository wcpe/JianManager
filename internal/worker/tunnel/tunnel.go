// Package tunnel 维护 Worker→CP 的常驻 gRPC 反向隧道（FR-281，见 ADR-066）。
//
// Worker 主动拨 CP 既有 gRPC 端口开反向隧道，把本机 WorkerService 实现挂到隧道上；
// CP 指令经隧道下发——worker 零入站端口要求（NAT/内网可接入），断连指数退避重连。
package tunnel

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jhump/grpctunnel"
	"github.com/jhump/grpctunnel/tunnelpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/wcpe/JianManager/internal/platform/grpcmsg"
)

// nodeUUIDHeader / nodeSecretHeader 隧道建立请求携带节点身份的 gRPC metadata header 名。
// 与 internal/worker/register、internal/controlplane/grpc 中常量一致（wire 约定，ADR-039）。
const (
	nodeUUIDHeader   = "node-uuid"
	nodeSecretHeader = "node-secret"
)

// Runner 常驻反向隧道运行器：Start 后台维护隧道直到 Stop。
type Runner struct {
	cpAddr     string
	nodeUUID   string
	nodeSecret string
	register   func(grpc.ServiceRegistrar) // 每次隧道重建时调用，向新隧道 server 注册服务

	// 退避参数（测试可缩短；生产用默认值）
	initialBackoff time.Duration
	maxBackoff     time.Duration
	// 额外拨号选项（测试注入 bufconn dialer；生产为空）
	dialOpts []grpc.DialOption

	stopOnce sync.Once
	stopCh   chan struct{}
}

// New 创建隧道运行器。register 回调在每次隧道（重）建立时调用一次。
func New(cpAddr, nodeUUID, nodeSecret string, register func(grpc.ServiceRegistrar)) *Runner {
	return &Runner{
		cpAddr:         cpAddr,
		nodeUUID:       nodeUUID,
		nodeSecret:     nodeSecret,
		register:       register,
		initialBackoff: 2 * time.Second,
		maxBackoff:     60 * time.Second,
		stopCh:         make(chan struct{}),
	}
}

// Start 启动后台隧道维护 goroutine。
func (r *Runner) Start() { go r.loop() }

// Stop 停止隧道并退出维护循环（幂等）。
func (r *Runner) Stop() { r.stopOnce.Do(func() { close(r.stopCh) }) }

func (r *Runner) loop() {
	// 稳定运行超过该时长即视为「上一次断开与本次无关」，重置退避。
	const stableReset = time.Minute
	backoff := r.initialBackoff
	for {
		select {
		case <-r.stopCh:
			return
		default:
		}

		startedAt := time.Now()
		err := r.serveOnce()
		select {
		case <-r.stopCh:
			return
		default:
		}
		if time.Since(startedAt) > stableReset {
			backoff = r.initialBackoff
		}
		slog.Warn("反向隧道断开，退避后重连", "error", err, "backoff", backoff.String())
		select {
		case <-r.stopCh:
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > r.maxBackoff {
			backoff = r.maxBackoff
		}
	}
}

// serveOnce 建一次隧道并阻塞服务到断开；返回断开原因。
func (r *Runner) serveOnce() error {
	opts := append([]grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}, r.dialOpts...)
	conn, err := grpc.NewClient(r.cpAddr, opts...)
	if err != nil {
		return err
	}
	defer conn.Close()

	rts := grpctunnel.NewReverseTunnelServer(tunnelpb.NewTunnelServiceClient(conn))
	// 消息尺寸守卫（FR-305）：grpctunnel 不接受 grpc.ServerOption，唯一天花板是 4GB 硬编码——
	// 经 WrapRegistrar 在注册层施加 64MiB 双向上限。
	r.register(grpcmsg.WrapRegistrar(rts))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Stop 时中断阻塞中的 Serve（Serve 自身只随 ctx/流断开返回）。
	go func() {
		select {
		case <-r.stopCh:
			rts.Stop()
		case <-ctx.Done():
		}
	}()

	// 身份经 metadata 出示（ADR-039）：CP 拦截器校验通过后按 UUID 登记隧道归属。
	// 用独立 serveCtx 承载附加 metadata 的派生 ctx，不回写 ctx——否则与上面读 ctx.Done() 的
	// goroutine 数据竞争（AppendToOutgoingContext 会重新赋值 ctx 变量）。
	serveCtx := metadata.AppendToOutgoingContext(ctx, nodeUUIDHeader, r.nodeUUID, nodeSecretHeader, r.nodeSecret)
	started, err := rts.Serve(serveCtx)
	if !started {
		slog.Debug("反向隧道未能建立", "error", err)
	}
	return err
}
