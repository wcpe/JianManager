// Package grpcmsg 提供 CP↔Worker gRPC 单消息尺寸上限的单一真值与应用层守卫（FR-305）。
//
// 背景：直拨链路的上限由 grpc.Server 选项显式限定（64MiB），而反向隧道（ADR-066）挂在
// grpctunnel 的 ReverseTunnelServer 上——v0.3.0 不暴露任何尺寸配置，唯一天花板是硬编码的
// math.MaxUint32（4GB），造成同一载荷「直拨拒收、隧道吃进」的双轨不一致与整块缓冲 OOM 暴露面。
// 治理手段为应用层拦截器（见 docs/specs/tunnel-message-size-guard/spec.md）：请求于进入
// handler 前判限、响应于返回前判限，两方向语义都落在本包内，不依赖 grpctunnel/grpchan 的
// 选项透传行为。
package grpcmsg

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// MaxMessageBytes CP↔Worker 单条 gRPC 消息尺寸上限的单一真值（收/发、直拨/隧道共用）。
// 取 64MiB：与探针依赖单文件上限同量级，为运行库缓存增长留足余量（沿革见 FR-010/FR-114 的
// 7.6MB DeployServerProbe 载荷把默认 4MiB 放宽到 64MiB 的决定）。
const MaxMessageBytes = 64 << 20

// exceededErr 统一超限错误：ResourceExhausted + 中文可操作引导（与直拨框架拒收同 code）。
func exceededErr(direction string, size int) error {
	return status.Errorf(codes.ResourceExhausted,
		"单条 gRPC 消息%s %d 字节超过上限 %dMiB（大文件请改走流式接口；若为旧版本节点请升级）",
		direction, size, MaxMessageBytes>>20)
}

// checkSize 判限 msg 的 marshal 尺寸（proto.Size 为精确 marshal 字节数；非 proto 消息不判，
// 交由后续编码层处理）。
func checkSize(msg any, direction string) error {
	pm, ok := msg.(proto.Message)
	if !ok {
		return nil
	}
	if size := proto.Size(pm); size > MaxMessageBytes {
		return exceededErr(direction, size)
	}
	return nil
}

// UnaryServerInterceptor 返回双向判限的 unary 服务端拦截器：
// 请求超限 → 不进 handler 直接拒收；响应超限 → 丢弃响应改为拒收（发送前拦截）。
func UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if err := checkSize(req, "（请求）"); err != nil {
			return nil, err
		}
		resp, err := handler(ctx, req)
		if err != nil {
			return resp, err
		}
		if err := checkSize(resp, "（响应）"); err != nil {
			return nil, err
		}
		return resp, nil
	}
}

// WrapRegistrar 包装 grpc.ServiceRegistrar：注册时改写 ServiceDesc 的 **unary** handler，
// 使其经本包判限拦截器执行——grpctunnel 的 ReverseTunnelServer 实现 ServiceRegistrar，
// 包一层即让隧道承载的 unary RPC 获得与 grpc.NewServer 尺寸选项等效的 64MiB 守卫（FR-305）。
//
// 只守 unary、**不包裹 stream handler**：大 unary 是无框架上限的整块缓冲 OOM 真风险；而
// grpctunnel 的流按 16KiB 分块传输（`chunkMax`）、单帧天然远小于上限，包裹其 ServerStream 的
// RecvMsg/SendMsg 反而干扰 grpctunnel 的分块流控——真机复现：并发常驻 StreamInstanceEvents +
// 7.6MB DeployServerProbe unary 一起时死锁 DeadlineExceeded。直拨侧的流由 ServerOptions 的
// MaxRecvMsgSize/MaxSendMsgSize 兜底，隧道侧的流由 grpctunnel 分块天然受限，无需再包。
// 原 desc 不被修改（浅拷贝壳 + 重建 Methods 切片）。
func WrapRegistrar(reg grpc.ServiceRegistrar) grpc.ServiceRegistrar {
	return &guardRegistrar{inner: reg}
}

type guardRegistrar struct{ inner grpc.ServiceRegistrar }

func (g *guardRegistrar) RegisterService(desc *grpc.ServiceDesc, impl any) {
	unaryInt := UnaryServerInterceptor()

	wrapped := *desc // 浅拷贝壳，Methods 切片下面重建，避免污染全局生成的 desc；Streams 原样透传
	wrapped.Methods = make([]grpc.MethodDesc, len(desc.Methods))
	for i, m := range desc.Methods {
		m := m // 捕获副本
		orig := m.Handler
		m.Handler = func(srv any, ctx context.Context, dec func(any) error, chainedInt grpc.UnaryServerInterceptor) (any, error) {
			// 组合语义：先跑注册器自带拦截器链（chainedInt，如有），再经本包判限拦截器进原 handler。
			return orig(srv, ctx, dec, chainInterceptors(chainedInt, unaryInt))
		}
		wrapped.Methods[i] = m
	}
	g.inner.RegisterService(&wrapped, impl)
}

// chainInterceptors 把外层已有拦截器（可为 nil）与判限拦截器串成一个。
func chainInterceptors(outer, inner grpc.UnaryServerInterceptor) grpc.UnaryServerInterceptor {
	if outer == nil {
		return inner
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		return outer(ctx, req, info, func(ctx context.Context, req any) (any, error) {
			return inner(ctx, req, info, handler)
		})
	}
}

// CallOptions 返回 CP 客户端两方向显式上限（防默认漂移：grpc-go 客户端接收默认仅 4MiB，
// 发送默认无界；统一显式到 MaxMessageBytes，与 Worker 服务端同界，FR-305）。
func CallOptions() []grpc.CallOption {
	return []grpc.CallOption{
		grpc.MaxCallRecvMsgSize(MaxMessageBytes),
		grpc.MaxCallSendMsgSize(MaxMessageBytes),
	}
}
