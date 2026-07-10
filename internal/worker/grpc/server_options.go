package grpc

import "google.golang.org/grpc"

// maxGRPCRecvMessageBytes 放宽 Worker gRPC 服务端单条消息接收上限。
//
// gRPC 默认服务端接收上限为 4MiB，而 CP 经 DeployServerProbe 一次下发内嵌 ServerProbe jar
// 加运行库缓存 libraries_zip 可达数 MB（实测 ~7.6MB），会被服务端以 ResourceExhausted 拒收，
// 导致建服/建代理时探针部署失败、监控与插件桥全链路非功能（FR-010/FR-114，ADR-016）。
// 取 64MiB 与探针依赖单文件上限 probeDependencyMaxBytes 同量级，为运行库缓存增长留足余量。
const maxGRPCRecvMessageBytes = 64 * 1024 * 1024

// ServerOptions 返回 Worker gRPC 服务端选项。
func ServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.MaxRecvMsgSize(maxGRPCRecvMessageBytes),
	}
}
