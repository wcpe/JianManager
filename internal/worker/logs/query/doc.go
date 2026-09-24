// Package query implements FR-478 Worker Log QueryPlanner foundation.
//
// 契约来源（不得在本包另立契约）：
//   - docs/specs/worker-log-platform-contract/spec.md §5 Catalog / §6 Query View / RPC
//   - docs/specs/worker-log-query-federation/spec.md §3.1 QueryPlanner 路由
//
// 本包职责边界：
//   - 按 Catalog 选择每个分区唯一、不重叠的权威查询副本（OwnerForQuery）；
//   - 产出 Coverage（complete/partial、targets、closed_visible_seq、缺失层原因）；
//   - Search/Stats/Tail stub 消费 planner ranges + budget + SortKey 契约；
//   - VIEW_STALE（view_id/generation 不匹配）与 Unimplemented 能力协商路径。
//
// 不做：真实 VL HTTP 检索（仅 injectable RangeClient）、CP 授权引擎、浏览器直连。
//
// 注意：proto/worker.proto 已冻结 Log* 消息，但 proto/workerpb 生成代码尚未包含
// 该批类型；本包使用与契约语义对齐的 Go 类型，待 protoc 重新生成后再做映射层。
package query
