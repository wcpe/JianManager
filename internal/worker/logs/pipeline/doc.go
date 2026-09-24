// Package pipeline 集成 FR-473/435 采集与归一化：tail/stdio/archive → normalize → WAL → DeliveryHook。
//
// 边界：
//   - 默认采集模式 FILE_PRIMARY（Paper）；STDIO_PRIMARY 为受管 Raw 通道。
//   - EventBoundaryHook（acquire）与 normalize.Normalizer 在本包接线；多行堆栈归并为一条 logtypes.Event。
//   - WAL 先 durable 再 delivery；DeliveryHook 的 HTTP 2xx 只写 REQUEST_DONE，不推进 reclaim。
//   - reclaim 必须经 logtypes.CanReclaim 门禁（恢复分段责任已转移且无 hold）。
//   - 缺口写入 ledger，禁止静默丢弃。
//
// 不重新定义 FR-472 Shared Contracts。本包只 import：
// logtypes / ledger / acquire / normalize（可选 catalog/vlsup 由 lifecycle 负责）。
package pipeline
