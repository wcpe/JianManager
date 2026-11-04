package ingest

import (
	"log/slog"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
)

// rollbackReadToReclaim 把某源的读/耐久水位回退到它的**回收水位**，使重启后重读
// 「已读、但未被回收证明»的字节。
//
// ## 为什么需要它（2026-10-29 架构变更）
//
// WAL 已改为**纯内存队列、不落盘**。旧实现在重启时靠 `source_wal` 表逐条恢复
// 「已读未投递」的事件；表没了，那些事件就没了——而 tailer 的游标又已越过它们，
// 不处理就是**永久丢数据**。
//
// 解法是换一套可靠性模型（不是补丁）——对齐 Filebeat 的 registry 语义：
//
//   - 队列内容（内存队列）**重启即丢**；
//   - 可靠性靠**持久化的 offset**（本项目的 `position` 表：read / durable / reclaim）
//     加上**至少一次重读**。
//
// 官方原文（Elastic, configuring-internal-queue）：
//
//	"The disk queue stores pending events on the disk rather than main memory...
//	 and **to save events when a Beat or device is restarted**."
//
// ⇒ 内存队列本身就不保证跨重启保存，它靠 disk queue 才有那个保证。
// 我们选择不落盘（用户裁定），就把「不丢」的责任交给重读。
//
// ## 为什么回退到 Reclaim 而不是 0
//
// `reclaim` 的语义是「这些字节已被证明安全另存」（投影/恢复段已承接），重读它们
// 既浪费又会让 `releaseRecovery` 的 from >= to 恒成立、静默不再建段（2026-09-30 生产实证）。
// 所以回退下界就是 reclaim。
//
// ## 为什么 durable 也要一起退
//
// 旧实现里 `durable` 与 `source_wal` 行是**同一个提交单元**落库的，语义是
// 「这些字节已经在库里了」。WAL 不落盘后这个语义不再成立：那些字节其实只在内存里，
// 重启就没了。而 tailer 的游标取 `min(durable, read)`（见 acquire/tailer.go 的
// NewFileTailer）——只退 read 不退 durable 等于白退。
//
// ## 重复怎么办
//
// 这是 **at-least-once**：重读段的字节会被再投递一次。重复只由一层吸收：
//   - `event_id` 幂等：同 id 同哈希的重复行在 VL 写入侧收敛（同 id 同 hash 不产生新行）。
//
// **2026-11-05 订正**：这里曾写着第二层「写前幂等（`dropEventsAlreadyWrittenCtx`）：
// 投递前按 `event_id:in(...)` 查一次」。**那句话描述的行为不存在**——那个函数在
// 2026-11-02 投递路径简化后失去了全部生产调用点（见它在 runtime_delivery_pipeline.go
// 的注释）。所以不要指望写前会先筛一遍；能重复写，靠 VL 侧 event_id 幂等收。
//
// 2026-11-03：~~`projection_generation` 换代次~~ 这一层也已不存在——重建链删除后不再换代次，
// 而且投递面也不再带 `projection_generation` 标签（`insertInBatchesCtxYield` 传空串）。
//
// 这与 Filebeat 的语义完全一致：它也只保证至少一次。
func rollbackReadToReclaim(led *ledger.Ledger, key ledger.SourceKey) {
	if led == nil {
		return
	}
	pos, ok := led.Positions(key)
	if !ok {
		return
	}
	// 已经回退过（read/durable 都已等于 reclaim）⇒ 幂等返回，不重复打日志。
	if pos.Read <= pos.Reclaim && pos.Durable <= pos.Reclaim {
		return
	}
	lost := pos.Durable - pos.Reclaim
	// 必须先退 durable 再退 read：`AdvanceRead` 会把下界夹到 reclaim，
	// 而 `AdvanceDurable` 是同代次内**单调前进**的——它不提供回退入口，
	// 所以这里直接改账本条目（这是重启恢复期的一次性修正，不是运行期路径）。
	led.RollbackDurableToReclaim(key)
	if err := led.AdvanceRead(key, pos.Reclaim); err != nil {
		slog.Warn("重启回退读水位失败（重读范围以账本当前值为准）",
			"logSourceID", key.LogSourceID, "target", pos.Reclaim, "error", err)
		return
	}
	slog.Info("WAL 改为纯内存：已把读水位回退到回收水位，重启后重读该区间（at-least-once）",
		"logSourceID", key.LogSourceID,
		"reclaim", pos.Reclaim, "readBefore", pos.Read, "durableBefore", pos.Durable,
		"rereadBytes", lost)
}
