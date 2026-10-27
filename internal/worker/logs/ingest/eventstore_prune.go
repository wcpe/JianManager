// 本文件把 eventstore.Prune 接上自动化调用路径（此前 Prune 只有实现、没有调用点）。
//
// # 为什么需要（背景）
//
// FR-484 把事件正文从内存挪到了磁盘段，但段只有「追加」没有「回收」，因此会随采集总量
// 无限增长（见 prune.go 文件头第 3 条）。本文件给出**有界、可证**的回收接线。
//
// # 契约变更（产品决策，已确认）
//
// 旧契约（runtime.go 的段追加处原注释）：段是「VL 数据根丢失后重建 projection 的权威集合，
// 故不裁剪只换介质」。平台不承担原档长期保存责任 —— 原档由用户自己在每台服务器上保留文本
// 日志，VL 只做加速索引查询。因此新契约是：
//
//	段是「投递完成前的暂存与重放来源，投递并逐字段校验通过后可回收」。
//
// 这条变更同时意味着：被回收区间**不再**参与「VL 数据根丢失后重建 projection」，
// 该能力对旧区间主动放弃（用户已确认）。
//
// # 水位判据：为什么不是 positions.Delivery
//
// 直觉上「投递水位」应当是 ledger 的 Positions.Delivery，但**它不能用作本判据**：
// delivery_position 的真实语义是「所有字节均有**请求结果**的连续前缀末端」
// （见 ledger/ledger.go 的 contiguousDeliveryEnd 与 delivery_batch_prune.go 文件头），
// 它按位置求连续前缀，**不按投递状态过滤**；而 acquire 侧的非 2xx 结果会以
// DeliveryUnknown 登记成批次（见 acquire/wal.go 的 recordHTTPResultLocked），
// 于是「请求失败」同样会把 delivery_position 推上去。用它当回收水位会把
// **投递失败、仍需重放**的事件正文删掉 —— 正是最不能删的那一段。
//
// 本文件采用的判据是**两条独立证据的交集前缀**：
//
//	watermark = min(Positions.Reclaim, 逐字段校验覆盖的连续前缀末端)
//
// 两条证据各自堵住一类事故：
//
//  1. **逐字段校验覆盖（VerifiedRuns，ingest 侧持久凭据）堵「删掉没投递的正文」。**
//     VerifiedRuns 是「写 VL 后逐字段可见性校验通过」的源位置连续区间
//     （见 persistedSource.VerifiedRuns 与 gap_autoresolve.go 的 recordVerifiedRuns），
//     它才是「成功投递到 VL 且逐字段校验通过」在代码里的落点。
//     它是**区间集合**（MergePositionRanges 的产物：升序、互不相接——相接会被合并），
//     因此绝不能取「所有区间的最大末端」当水位：中间的空洞（校验失败的簇、永不投递的
//     弃投区间）会被凸包一并宣称已覆盖。本文件只取**锚点所在那一段区间的末端**，
//     即「从段内最小位置起，连续被校验覆盖到哪」。
//
//  2. **Reclaim 堵「删掉还会被重新追加的身份」。**
//     WAL 的回收只丢 record_end <= Reclaim 的条目（见 acquire/wal.go 的
//     pruneReclaimedLocked）。若不加这一层，被回收段的身份会同时从
//     「段的 seen 集合」（runtime.go 的 m.events.Iterate 重建）里消失，
//     而 durable WAL 里仍有同一条 ⇒ canonicalRecoveryEvents 仍会返回它 ⇒
//     missingFromEventStore 判它「缺」⇒ 重新追加进段、并作为待写事件去写 VL
//     ⇒ VL 出现重复行 ⇒ 逐字段校验以 duplicate/content mismatch 响亮失败。
//     夹上 Reclaim 后被回收事件的 record_end 全部 <= Reclaim ⇒ 其 WAL 条目已被丢弃
//     ⇒ 不可能再从权威集合回来，上述连锁不成立。
//
// 两条证据都只前进（单调不减），故判据单调：
//   - VerifiedRuns 的**有界保留是安全方向**：超出 maxVerifiedRunsPerSource 时丢弃的是
//     **位置最旧**的区间（`merged[len-64:]`，见 recordVerifiedRunsReporting）。本文件用的锚点是
//     「包含段内最小位置的那一段」——最旧的那些区间被丢时，锚点段会先消失 ⇒ 找不到锚点
//     ⇒ 水位退化为 0 ⇒ 一个段都不删（少删方向）。它**不可能**因为裁剪而取到比真实覆盖
//     更高的位置，因为取的是某段的 To，而不是外推。
//   - Positions.Reclaim 只在 target > Reclaim 时推进（见 TryReclaim 的注释）；唯一的例外是
//     Restore 的落库态违规下调分支（LEDGER-4），它把 Reclaim **调低** ⇒ 水位变小 ⇒ 同样安全方向。
//
// # 接线时机：每日一次，且**不**在启动时立即执行
//
// 不挂在投递成功路径上：Prune 要为每个候选段做删前重扫（calibrateSegment，成本与该段字节数
// 同阶），逐批调用会把投递延迟拖进段大小量级。
// 不在启动时立即跑：与 retention 驱动器（它刻意首轮立即执行，因为「停机期间攒下的过期分区」
// 最急）不同，本操作是**回收平台自己的暂存**，早一天晚一天没有正确性差别，而重启风暴下
// 「每次启动都扫一遍全量段」是纯粹的 IO 浪费且会与启动恢复抢磁盘。故只在配置的本地整点跑。
package ingest

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/eventstore"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
)

// 事件段回收的默认调度时刻：本地凌晨 3 点（与 log_retention.trigger.sweep_at_hour 的默认一致，
// 运维心智里「凌晨做清理」是同一条）。与保留驱动器的 hour 相互独立配置：两者删的东西不同，
// 不能因为改了一个就把另一个的时刻也挪走。
const (
	DefaultEventStorePruneSweepAtHour = 3
	// eventStorePruneMinInterval 是两轮之间的最小间隔，防止 sweep_at_hour 配错（例如线程热更
	// 反复重建 Manager）导致 Prune 被高频触发。低频是本功能的前提，不是优化。
	eventStorePruneMinInterval = 12 * time.Hour
)

// EventStorePruneConfig 是事件段回收的配置面（键 `log_retention.eventstore_prune.*`）。
//
// 零值语义：Enabled=false 是**真正的默认**（不是「无法区分显式关闭」）——回收是删除类能力，
// 用户护栏要求默认全关，必须由运维显式开启。这与 ledger.DeliveryBatchPruneConfig 的
// 「零值退化为默认开启」不同，原因就是这里删的是**段**（唯一副本），那边删的是账本历史条目
// （已被独立证据覆盖的冗余）。
type EventStorePruneConfig struct {
	// Enabled 为 false 时完全不回收（一个段都不删、连 Count 都不额外读）。
	Enabled bool
	// SweepAtHour 是每日触发的本地整点（0..23）；越界回退默认 3 点，绝不因为误写而变成
	// 「每小时都跑」（那会把低频前提抹掉）。
	SweepAtHour int
}

// DefaultEventStorePruneConfig 返回默认配置：**关闭**、每日 3 点。
func DefaultEventStorePruneConfig() EventStorePruneConfig {
	return EventStorePruneConfig{Enabled: false, SweepAtHour: DefaultEventStorePruneSweepAtHour}
}

// Normalized 把非法值收敛到默认。越界的小时数是误写，回退默认而不是夹到边界或当作 0 点：
// 「配错就按默认时刻跑」比「配错就半夜 0 点跑」可预期。
func (c EventStorePruneConfig) Normalized() EventStorePruneConfig {
	out := c
	if out.SweepAtHour < 0 || out.SweepAtHour > 23 {
		out.SweepAtHour = DefaultEventStorePruneSweepAtHour
	}
	return out
}

// EventStorePruneSourceResult 是单源一次回收的实测结果（含「为什么没删」，见 Skipped）。
type EventStorePruneSourceResult struct {
	Key string
	// Watermark 是本轮实际交给 Prune 的水位（0 = 没有下界证据）。
	Watermark uint64
	// Reclaim 与 VerifiedEnd 是水位两个输入的原始读数（对账用：一眼看出是哪一个把水位压住了）。
	Reclaim     uint64
	VerifiedEnd uint64
	Result      eventstore.PruneResult
	Err         error
}

// EventStorePruneResult 是一轮全源回收的汇总。
type EventStorePruneResult struct {
	At time.Time
	// Sources 是逐源结果（只含**有事件段**的源：没有段就没有可回收的东西，
	// 把它们也列进来只会让「本轮 0 回收」读不出到底是没数据还是被条件挡住）。
	Sources []EventStorePruneSourceResult
	// ReclaimedSegments/Bytes/Events 是本轮真正回收的合计。
	ReclaimedSegments int
	ReclaimedBytes    int64
	ReclaimedEvents   int64
	// Failures 是本轮返回错误的源数（水位算不清、校准失败、写清单失败等）。
	Failures int
	// Disabled 为 true 表示配置未开启：本轮**没有做任何事**（未枚举源、未触碰段）。
	Disabled bool
}

// eventStorePruneWatermark 计算单源本轮可用的回收水位，返回 (水位, Reclaim, VerifiedEnd)。
//
// 水位 0 表示「没有下界证据」，Prune 会直接返回且一个字节都不删 —— 这正是所有不确定情形的
// 收敛点，本函数**任何**拿不准的分支都返回 0 而不是猜一个更大值。
//
// anchor 取段内最小 record_end（Segments[0].MinEnd）：它是段内所有事件 record_end 的下确界，
// 于是「锚点所在区间覆盖到 To」+「被回收段的 MaxEnd <= To」两条合起来，
// 保证被回收的**每一条**事件的 record_end 都落在同一段逐字段校验覆盖区间内。
// 反过来若只取 VerifiedRuns 的末端最大值，段内低于首个覆盖起点的未校验事件会被一并删掉。
func (m *Manager) eventStorePruneWatermark(key string, saved persistedSource) (watermark, reclaim, verifiedEnd uint64) {
	if m.events == nil {
		return 0, 0, 0
	}
	// Reclaim 只从账本读：它与 Reclaim 的推进共用同一真源，不另做缓存（缓存一旦落后就会
	// 把水位抬高，方向错）。管道不存在（源已下线/未装配）时没有 Reclaim 证据 ⇒ 水位 0。
	if pipe, ok := m.pipeFor(key); ok {
		if positions, ok := pipe.Positions(); ok {
			reclaim = positions.Reclaim
		}
	}
	if reclaim == 0 {
		// 没有任何字节被裁定「原档可以走」⇒ 连 WAL 都还没释放，此时回收段一定会让
		// 「段与 WAL 的身份集合」对不上（见文件头第 2 条）。直接放弃。
		return 0, reclaim, 0
	}
	segments := m.events.Segments(key)
	return eventStorePruneWatermarkFrom(reclaim, saved.VerifiedRuns, segments)
}

// eventStorePruneWatermarkFrom 是水位判据的**纯函数形态**（无 IO、无锁、无 Manager 依赖），
// 便于把安全论证逐条钉成用例。
//
// 入参 reclaim 是账本已释放到的位置，runs 是该源逐字段校验覆盖区间，segments 是该源段清单。
// 返回 (水位, Reclaim, VerifiedEnd)；水位为 0 表示「没有下界证据」，调用方必须一个段都不删。
//
// 三个前置条件任一不成立即返回 0（宁占盘不丢数据的收敛点）：
//  1. reclaim == 0：WAL 一条都没释放 ⇒ 段与 WAL 的身份集合还可能对不上（见文件头第 2 条）；
//  2. 段数 < 2：Prune 侧也要求至少保留最后一段，提前收敛免得白算；
//  3. 段内最小位置不在任何校验覆盖区间内：锚点缺失就意味着「段里最早的那些事件没有
//     逐字段校验通过的证据」，此时任何大于它的水位都会把未校验事件一起回收。
func eventStorePruneWatermarkFrom(reclaim uint64, runs []ledger.PositionRange, segments []eventstore.SegmentMeta) (watermark, effectiveReclaim, verifiedEnd uint64) {
	if reclaim == 0 {
		return 0, 0, 0
	}
	if len(segments) < 2 {
		return 0, reclaim, 0
	}
	anchor := segments[0].MinEnd
	verifiedEnd = contiguousVerifiedEndAt(runs, anchor)
	if verifiedEnd == 0 {
		return 0, reclaim, 0
	}
	if verifiedEnd < reclaim {
		return verifiedEnd, reclaim, verifiedEnd
	}
	// 取更小者：Reclaim 更小时用它（WAL 已释放的范围之外不动），VerifiedEnd 更小时用它
	// （校验覆盖之外不动）。两者都不满足的区间一律不删。
	return reclaim, reclaim, verifiedEnd
}

// contiguousVerifiedEndAt 返回「包含 pos 的连续校验覆盖区间的末端」，无覆盖返回 0。
//
// runs 是 MergePositionRanges 的产物（升序、互不相接：相接或重叠的区间已被合并，容差 1 字节），
// 因此在其中至多有一段包含 pos，不需要再串接后继区间 —— 有任何后继都意味着中间存在空洞，
// 而空洞正是不能跨越的部分。
func contiguousVerifiedEndAt(runs []ledger.PositionRange, pos uint64) uint64 {
	for _, run := range runs {
		if run.From <= pos && pos <= run.To {
			return run.To
		}
	}
	return 0
}

// eventStorePruneConfigOf 归一化配置：nil（键未配）走默认 = **关闭**。
//
// 与 indexPruneConfigOf 的差别只在默认值方向：删除类能力不配置即关闭，这不是「保守猜一个」，
// 而是用户护栏要求的默认（宁占盘不丢数据）。
func eventStorePruneConfigOf(configured *EventStorePruneConfig) EventStorePruneConfig {
	if configured == nil {
		return DefaultEventStorePruneConfig()
	}
	return configured.Normalized()
}

// SetEventStorePrune 运行时替换配置（配置热更/测试注入用）；入参按 Normalized 收敛。
func (m *Manager) SetEventStorePrune(cfg EventStorePruneConfig) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.eventStorePrune = cfg.Normalized()
	m.mu.Unlock()
}

// EventStorePruneConfig 返回本 Manager 当前生效的回收配置（观测/回归用）。
func (m *Manager) EventStorePrune() EventStorePruneConfig {
	if m == nil {
		return DefaultEventStorePruneConfig()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.eventStorePrune
}

// PruneEventStoreSegments 对全部有事件段的源执行一轮有界回收。
//
// 错误口径：单源失败不阻断其它源（回收是空间优化，不是正确性前提），但失败源数如实汇总，
// 使观测面能区分「本轮 0 回收是因为条件不满足」与「本轮有源报错」。
// 只有「枚举源」这一步失败才返回 error（那说明 Manager 状态本身不可用）。
func (m *Manager) PruneEventStoreSegments() (EventStorePruneResult, error) {
	result := EventStorePruneResult{At: time.Now()}
	if m == nil || m.events == nil {
		return result, errors.New("ingest: 事件段存储未装配，无法回收")
	}
	// 配置闸放在**这里**而不是只放在循环驱动器里：本方法是唯一的执行入口，任何未来新增的
	// 调用点（运维端点、测试、别的周期任务）都必须被同一个开关挡住。
	// 关闭时立刻返回，连源清单都不枚举（「完全不 Prune」的强语义）。
	if cfg := m.EventStorePrune(); !cfg.Enabled {
		result.Disabled = true
		return result, nil
	}
	// 源清单取一次快照：本函数会长时间做段扫描，不能持 m.mu。
	m.mu.Lock()
	keys := make([]string, 0, len(m.state.Sources))
	saved := make(map[string]persistedSource, len(m.state.Sources))
	for key, src := range m.state.Sources {
		keys = append(keys, key)
		saved[key] = src
	}
	m.mu.Unlock()

	for _, key := range keys {
		src := saved[key]
		watermark, reclaim, verifiedEnd := m.eventStorePruneWatermark(key, src)
		if watermark == 0 {
			// 水位 0：连 Prune 都不必调（它对 watermark==0 也是立即返回，但这里跳过可以
			// 省掉一次 Segments 复制与锁进入）。不记为失败：这是正常的「还没到可回收状态」。
			continue
		}
		res, err := m.events.Prune(key, watermark)
		entry := EventStorePruneSourceResult{
			Key: key, Watermark: watermark, Reclaim: reclaim, VerifiedEnd: verifiedEnd,
			Result: res, Err: err,
		}
		result.Sources = append(result.Sources, entry)
		if err != nil {
			result.Failures++
			slog.Warn("事件段回收失败（本轮保留全部段）",
				"logSourceID", key, "watermark", watermark, "reclaim", reclaim,
				"verifiedEnd", verifiedEnd, "error", err)
			continue
		}
		if res.ReclaimedSegments > 0 {
			result.ReclaimedSegments += res.ReclaimedSegments
			result.ReclaimedBytes += res.ReclaimedBytes
			result.ReclaimedEvents += res.ReclaimedEvents
			// 段变了：所有「按行数判段未变」的进程内缓存立刻失效。
			// 见 invalidateEventStoreRowCaches 的说明。
			m.invalidateEventStoreRowCaches(key)
			slog.Info("事件段已回收",
				"logSourceID", key, "watermark", watermark, "reclaim", reclaim,
				"verifiedEnd", verifiedEnd, "segments", res.ReclaimedSegments,
				"bytes", res.ReclaimedBytes, "events", res.ReclaimedEvents,
				"remaining", res.RemainingSegments)
		} else if res.Skipped != "" {
			slog.Debug("事件段本轮未回收",
				"logSourceID", key, "watermark", watermark, "reclaim", reclaim,
				"verifiedEnd", verifiedEnd, "reason", res.Skipped)
		}
	}
	// 记录本轮快照供 /metrics 读数：抓取方可能高频来取，不能在指标端点里现算（那会把
	// 一次抓取变成一次全量段扫描）。放在返回前统一写，保证「返回了」与「指标可见」一致。
	m.mu.Lock()
	m.lastEventStorePrune = result
	m.mu.Unlock()
	return result, nil
}

// LastEventStorePrune 返回最近一轮事件段回收的结果快照（观测用，零拷贝开销极小）。
//
// 零值（At 为零）表示**从未运行过**，调用方据此区分「没跑过」与「跑过但零回收」。
func (m *Manager) LastEventStorePrune() EventStorePruneResult {
	if m == nil {
		return EventStorePruneResult{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastEventStorePrune
}

// EventStorePruneSnapshot 实现 retention.EventStorePruneSource，供 /metrics 读数。
//
// 返回值把「是否跑过」「是否被配置关闭」「本轮各计数」一次带出，使指标端点不必再判空：
// ran=false 时其余计数无意义（包内已置零），disabled=true 表示配置未开启、本轮什么都没做。
//
// 为什么不由本包直接产出 Prometheus 文本：指标出口（HELP/TYPE/序列顺序）在 retention 包
// 单点维护，本包只提供读数；否则同一份格式会在两处漂移。
func (m *Manager) EventStorePruneSnapshot() (ran bool, at time.Time, segments, bytes, events int64, failures int, disabled bool) {
	if m == nil {
		return false, time.Time{}, 0, 0, 0, 0, true
	}
	m.mu.Lock()
	snap := m.lastEventStorePrune
	cfg := m.eventStorePrune
	m.mu.Unlock()

	disabled = !cfg.Enabled
	if snap.At.IsZero() {
		return false, time.Time{}, 0, 0, 0, 0, disabled
	}
	return true, snap.At, int64(snap.ReclaimedSegments), snap.ReclaimedBytes,
		snap.ReclaimedEvents, snap.Failures, disabled
}

// invalidateEventStoreRowCaches 丢弃该源全部「段行数/段内容未变」类缓存，并推进段版本号。
//
// 为什么必须有它（这是 Prune 接线后唯一**新增**的不变量维护）：本包原有三处缓存都以
// 「段只追加、行数只增不减」为前提判断「段还是不是记备忘时那一段」：
//   - eventsStoredRows（cachedStoredRows 的基线）；
//   - storedCoverageMemo（storedCoverage 按 rows + epoch 判命中）；
//   - canonicalRecoveryEventsReusing 的 recoveryReceipt（按 rows + epoch 判复用）。
//
// Prune 之后行数**变小**，三条前提同时失效。三处缓存本身都已经带了 rows 比对，理论上会被
// 行数变化自然打出 miss；但「行数变小」不是唯一形态（回收后又追加回同样的行数），
// 那时只有 epoch 能区分。因此这里**同时**推进 epoch 并显式丢弃两张表：
// 让「段被回收过」直接等价于「缓存全部失效」，不依赖「行数恰好对不上」这种巧合。
func (m *Manager) invalidateEventStoreRowCaches(key string) {
	m.mu.Lock()
	if m.eventsStoredEpoch == nil {
		m.eventsStoredEpoch = map[string]uint64{}
	}
	m.eventsStoredEpoch[key]++
	delete(m.storedCoverageMemo, key)
	delete(m.eventsStoredRows, key)
	m.mu.Unlock()
}

// RunEventStorePrune 按配置的每日整点反复执行一轮回收，直到 ctx 结束。
//
// 与 retention.Driver 的调度差异（刻意，不是遗漏）：**不在启动时立即执行一次**。理由见文件头
// 「接线时机」。首轮等到下一个触发时刻；若进程在等待期间重启，下一轮仍等下一个整点，
// 不会因为重启而提前。这牺牲「重启后立刻回收」，换来「重启风暴不会变成高频段扫描」。
func (m *Manager) RunEventStorePrune(ctx context.Context, cfg EventStorePruneConfig) {
	cfg = cfg.Normalized()
	if m == nil || !cfg.Enabled {
		return
	}
	var last time.Time
	for {
		now := time.Now()
		next := nextEventStorePruneAt(now, cfg.SweepAtHour)
		if !last.IsZero() && next.Sub(last) < eventStorePruneMinInterval {
			// 防误配/防热更抖动：同一小时内反复进入本函数时把下一轮推到 last+最小间隔。
			next = last.Add(eventStorePruneMinInterval)
		}
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		last = time.Now()
		res, err := m.PruneEventStoreSegments()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("事件段回收本轮失败（下轮重试）", "error", err)
			continue
		}
		slog.Info("事件段回收完成一轮",
			"scanned", len(res.Sources), "failures", res.Failures,
			"segments", res.ReclaimedSegments, "bytes", res.ReclaimedBytes,
			"events", res.ReclaimedEvents)
	}
}

// nextEventStorePruneAt 返回 now 之后最近的一个本地 hour 整点（严格大于 now）。
func nextEventStorePruneAt(now time.Time, hour int) time.Time {
	if hour < 0 || hour > 23 {
		hour = DefaultEventStorePruneSweepAtHour
	}
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, now.Location())
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	return next
}
