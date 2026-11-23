// Package grpcmap 把 query 包类型与 proto/workerpb 的 Log* 消息双向映射。
//
// 契约边界：
//   - OrderVersion wire 值统一为 query.SortVersion，避免 view reuse 因 order_version 不一致失败；
//   - partial / unsupported 必须落在 response error / coverage，不得抹成空成功；
//   - ClosedVisibleSeq / CatalogGeneration 在 proto 侧是 string，映射用十进制；
//   - 本包不发起 gRPC，也不持有 planner/service 状态。
package grpcmap
