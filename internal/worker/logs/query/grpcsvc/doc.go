// Package grpcsvc 实现 WorkerService 的 Log* / GetLogCapabilities RPC 层。
//
// 职责：
//   - 把 gRPC 请求经 grpcmap 反向映射为 query.QueryRequest；
//   - 调用可注入的 QueryService（生产为 *query.Service，测试为 fake）；
//   - 把 query 结果映射回 workerpb.Log* 响应；
//   - 老 Worker / 服务禁用 / RangeClient 未实现时：GetLogCapabilities.supported=false
//     或响应 error=LOG_UNSUPPORTED，绝不得伪装成空成功；
//   - ClosedVisibleSeq 从 planner view 回填到 coverage targets。
//
// 本包不实现真实 VL 检索，也不注册 gRPC server（装配由 worker 主进程完成）。
package grpcsvc
