// Package logcoord 实现 FR-480 CP 跨 Worker 日志查询协调器基础（纯 service + 接口）。
//
// 契约来源：
//   - docs/specs/cp-log-query-coordinator/spec.md
//   - docs/specs/worker-log-platform-contract/spec.md（授权目标、覆盖、可合并聚合、Export 一致性）
//   - proto/worker.proto LogSearchResponse / LogCoverage / LogStats*
//
// 本包不接线 Gin router、不注册 gRPC server；跨 Worker 调用经 WorkerClient 接口注入
// （生产实现走 CP 反向 gRPC 隧道）。字段与覆盖语义以 FR-473 为准，本包不另立契约。
package logcoord
