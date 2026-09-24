// Package lifecycle 实现 FR-476 Worker 日志分区 Catalog 与冷热 Lifecycle 编排。
//
// 边界：
//   - LogLifecycleManager 驱动 catalog 状态机：冻结路由→排空→snapshot→staging→
//     QueryPlanner 排除→原子切 owner→租约排空→detach→清理。
//   - snapshot/copy/verify/detach 等物理操作可注入（Ops），本包不直接控制 VL 进程。
//   - Runtime 为可选薄接口，可由 vlsup.Supervisor 适配；不是完整 VL process control。
//   - 迟到事件走 catalog.RouteLateEvent，禁止按 now-7d 重新生成 HOT 权威。
//   - Recover 在启动时调用 catalog.StartingRecover。
//   - retention 协同：last-copy 保护，下一层未确认接收前不得 detach/删除最后有效副本。
//
// 不重新定义 FR-472。本包 import：catalog / vlsup / logtypes。
package lifecycle
