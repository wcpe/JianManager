package grpc

import (
	"google.golang.org/grpc"

	"github.com/wcpe/JianManager/internal/platform/grpcmsg"
)

// ServerOptions 返回 Worker gRPC 服务端选项（直拨链路）。
//
// 单条消息收/发上限取 grpcmsg.MaxMessageBytes（64MiB 单一真值，FR-305）：
//   - 接收：gRPC 默认仅 4MiB，CP 经 DeployServerProbe 一次下发内嵌 ServerProbe jar 加运行库
//     缓存 libraries_zip 可达数 MB（实测 ~7.6MB），默认值会以 ResourceExhausted 拒收，
//     致建服/建代理时探针部署失败、监控与插件桥全链路非功能（FR-010/FR-114，ADR-016）；
//   - 发送：原为框架默认无界，显式同界防超大响应整块缓冲（与隧道侧 WrapRegistrar 守卫、
//     CP 客户端 CallOptions 三处同源同值，见 docs/specs/tunnel-message-size-guard/spec.md）。
func ServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.MaxRecvMsgSize(grpcmsg.MaxMessageBytes),
		grpc.MaxSendMsgSize(grpcmsg.MaxMessageBytes),
	}
}
