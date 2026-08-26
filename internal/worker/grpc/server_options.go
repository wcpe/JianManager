package grpc

import (
	"google.golang.org/grpc"

	"github.com/wcpe/JianManager/internal/platform/grpcmsg"
)

// ServerOptions 返回 Worker gRPC 服务端选项（直拨链路）。
//
// 单条消息收/发上限取 grpcmsg.MaxMessageBytes（64MiB 单一真值，FR-305）：
//   - 接收：保留较大上限以兼容历史 DeployServerProbe 字节载荷与既有大 unary 请求；
//     新版探针部署只传 CP-local URL、摘要与版本，不再携带 jar 或 libraries_zip，
//     致建服/建代理时探针部署失败、监控与插件桥全链路非功能（FR-010/FR-114，ADR-016）；
//   - 发送：原为框架默认无界，显式同界防超大响应整块缓冲（与隧道侧 WrapRegistrar 守卫、
//     CP 客户端 CallOptions 三处同源同值，见 docs/specs/tunnel-message-size-guard/spec.md）。
func ServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.MaxRecvMsgSize(grpcmsg.MaxMessageBytes),
		grpc.MaxSendMsgSize(grpcmsg.MaxMessageBytes),
	}
}
