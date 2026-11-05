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
// ## 重复怎么办：**没有去重层**（已实测，勿再假设）
//
// 这是 **at-least-once**：重读段的字节会被再投递一次，而**没有任何一层会把重复抹掉**。
//
// 仓库里先后有三处被当作“去重层”的假设，现在**三个都不成立**：
//
//  1. 写前幂等（`dropEventsAlreadyWrittenCtx`）：2026-11-02 简化后失去全部生产调用点；
//  2. `projection_generation` 换代次过滤：2026-11-03 重建链删除后不再写该标签；
//  3. vl 侧 `event_id` 幂等去重：**2026-11-05 实测证伪**（重要）。
//
// 第 3 条的实测（生产 VL 19461，2026-11-05）：
//
//	向 /insert/jsonline 写两次**完全相同**的行（同 _time、同 event_id、同 _msg）
//	两次都 HTTP 200；随后查询得到 count = 2，两行 _stream_id 也完全相同。
//
// ⇒ VictoriaLogs 是 append-only 时序库，`/insert/jsonline` **不做任何字段级唯一约束**。
// `event_id` 只是一个普通字段。
//
// 所以：**重投就是真的多写行**。后果不是“重复不影响”，而是：
//   - 查询侧按行数统计的读数会偏大；
//   - 回滚/重启循环会让存储无界增长（历史事故同形：
//     `recovery_generation.go` 里记的「projection-130 落 2,294,248 行 = 573,562 × 4」）。
//
// 这也意味着「下游按 event_id 幂等」不能在稳定性论证里当前提用。
// 真要消除重复，得在**写入前**做 event_id 去重（即重新接上第 1 条的能力）——
// 那是待办，不是已具备的能力。
//
// 这与 Filebeat 的语义一致：它也只保证至少一次（不保证去重）。
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
