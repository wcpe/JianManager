// 本文件实现「投递被 VL 接受、但本批在 VL 里查不到」的**降级出口**（2026-11-05，审计 P0-1 的结构性修复）。
//
// # 它要堵的那个闭环
//
// `/insert/jsonline` 会在多种情况下返回 **HTTP 200 但静默丢弃整批**：
//   - 数据点早于 `-retentionPeriod` 窗口；
//   - 单行字段数超 VL 上限；
//   - 任何服务端认为不可接受的理由。
//
// 而投递路径只看 HTTP 状态码：拿到 200 就登记 `REQUEST_DONE`
// （`acquire.pipeline.recordHTTPResults`）⇒ `reclaim` 随 `REQUEST_DONE` 推进
// （`ledger.contiguousCertifiedEnd`）⇒ `pruneReclaimedLocked` **物理删掉** WAL 条目。
//
// 净效果：`EventStateAcked`、VL 里 0 行、WAL 已删、无缺口凭据、无告警。
// 这就是「查不到数据但报成功」最完整的形态。
//
// # 为什么核对本身不在本文件做（第一版草案就是这么错的）
//
// 最初的草案把「退避 10s→20s→30s 的**同步**核对」塞进 `deliverCtx`，投递返回前等满 60s。
// 那与两条既有事实直接冲突：
//   - `acquire.recordHTTPResults` 在 `deliverCtx` **返回之后**才登记 REQUEST_DONE
//     ⇒ 在投递路径里同步等 60s = **每批阻塞采集轮 60s**；
//   - 本仓历史读数（FR-498 长稳）里「写后强制可见性校验」曾占单轮 91.2% 中的 85.6%，
//     正是因此才把它整条移出投递路径。
//
// 所以核对**复用既有的异步缺口核对链**（`scheduleGapResolve` → `executeGapResolve`）：
// 它本来就在投递成功后异步问「这批到底在不在 VL」，查不到就留在待办里下一轮再问。
// 本文件补上的是它**一直缺的那一半出口**：这么重试是**无界**的 ——
// VL 永久拒收时待办会无限增长、永不收敛（这正是同一条链反方向上的无声失效）。
//
// 判定与计数在 `gap_resolve_worker.go`（那里持有待办与轮次），本文件只负责：
// 证据的形状，以及「确定不可见」之后**怎么记账**。
//
// # 为什么必须「有界重试 + 降级」而不是纯 fail-closed
//
// VL 有写入后可见延迟（本仓多处实测约 1.0~1.06s；本轮实测 10s 内必可见）。若查不到就永久
// 不记成功，一次抖动就让整个源停摆——那比丢一批更糟（之后所有新日志一起丢）。
//
// 所以：**有界重试 → 耗尽后降级**，承认可能丢，但绝不静默、也绝不无界卡住。
//
// # 三态（这是本设计的核心，务必分清）
//
//   - **可见**：正常，不走本路径（`recordVerifiedRuns` 照常记区间凭据）。
//   - **查询失败**（网络/超时/5xx）：**不知道**。这种情况绝不能判「确定丢」——
//     那会把「不确定」固化成「确定丢了」并永久移除待办。处置：保留待办、**不计轮次**，
//     下轮再问（见 `gap_resolve_worker.go` 里 `probeOK` 的分流）。
//   - **查询成功但查不到**（连续多轮）：**确定不可见** ⇒ 降级 DROPPED + 记缺口。
//
// 三者混淆任何一个方向，都会把系统推向更坏的一侧：
// 把「查询失败」当「不可见」⇒ 一次 VL 抖动就把正常数据全记成丢失；
// 把「不可见」当「查询失败」⇒ 重试永不收敛 ⇒ 待办无界增长 ⇒ 同样拖垮采集。
package ingest

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 判「确定不可见」的界。
//
// # 为什么是「3 轮」+「60s 窗」两个条件同时成立
//
// 单个条件都会给出不诚实的结果：
//   - **只看轮次**：`gapResolveRetryBackoff` 是 30s，但新批次登记（`schedule` 合并）会让
//     待办被提前唤醒几次；只数轮次可能在十几秒内就凑够 3 轮 —— 那还在可见延迟的正常范围内。
//   - **只看墙钟**：一次 60s 的 VL 抖动期间没有核对过，窗口照样满足，判据却没有任何
//     「问过几次」的证据。
//
// 两者同时成立 ⇒ 至少经过 maxGapResolveMissRounds-1 个退避间隔、且每一轮都
// **查询成功但查不到**（见 probeOK）。与 VL「10s 内必可见」的实测上界留了 6 倍余量。
//
// 用 var 而非 const：用例需要把窗口压到毫秒级/把轮次数钉成 1（与既有
// `gapVisibilityBudgetForTest` 同一做法），否则每条降级用例都要真等 60s。
var (
	// maxGapResolveMissRounds 是连续「查询成功但查不到」的核对轮次上限。
	maxGapResolveMissRounds = 3
	// gapResolveMissWindow 是从**首次**判定未可见到最终降级的最小墙钟跨度。
	//
	// 默认 60s：3 轮 × 30s 退避 ⇒ 第 3 轮必然落在起点 +60s 之后（每轮自身还有执行耗时），
	// 所以这个界不会因为定时器抖动而永远差一点点。
	// **若生产观测到误判（写进去了却判不可见），只能调大这个值，不能调小。**
	gapResolveMissWindow = 60 * time.Second
)

// deliveryMissEvidence 是判「确定不可见」时**必须留下**的判断依据（审计硬约束 1）。
//
// # 为什么这些字段是强制的
//
// 「重试耗尽仍查不到」有两种成因，处置完全不同：
//   - **真丢**（VL 拒绝/丢弃）⇒ 记缺口、别再重试；
//   - **超过窗口的延迟**（高负载、flush 滞后）⇒ 重投会造重复行，而 VL **不去重**。
//
// 自动区分不了，但必须让运维能判：所以把「问了几轮」「等了多久」「每轮缺几条」
// 一起写进缺口 detail 与日志。`Rounds` 还是「连续批次数」的直接读数 ——
// 连续多批都这样更像系统性真丢，偶发一批更像瞬时延迟。
type deliveryMissEvidence struct {
	// Missing 是降级那一刻仍未确认可见的事件数（本批 + 待办里累积的）。
	Missing int
	// Total 是参与本次判定的待办事件总数（= Missing + 已确认可见的那些）。
	// 只用于让 detail 里「缺几条 / 共几条」可解释。
	Total int
	// Rounds 是该源连续「查询成功但查不到」的核对轮次（含判定这一轮）。
	Rounds int
	// Window 是从首次判定未可见到降级的实际墙钟跨度。
	Window time.Duration
}

// recordVisibleDeliveryMiss 把「本批确定不可见」处置成：记可见缺口 + 降级 DROPPED。
//
// # 为什么必须降级 DROPPED，而不是继续留在待办里重试
//
// 继续等 = 待办无界增长（`mergeGapResolveEvents` 只做并集，永不缩小）⇒ 一次核对要查的事件
// 越来越多 ⇒ 探测被切成几十片 ⇒ 任何预算都超时 ⇒ 永不收敛（2026-11-02 实测的 361→6452）。
// 而这类「VL 拒绝写入」往往**不可自愈**（retention 超限、字段数超限都不会自己变好）。
//
// DROPPED 的语义正好是「确定不会有结果了」：它可以把 `delivery` 水位推过去，
// 但**绝不进 `RequestDoneRanges`**（`RecordDroppedRange` 的注释写明了这条），
// 所以不会污染投递证据、不会谎报"已投递"。
//
// # 为什么按**连续覆盖区间**逐个记账，而不是取首尾凸包
//
// 待办里的事件来自多个批次，中间完全可能夹着与本次判定无关的位置。
// 用凸包会把这些字节一起标成「确定丢了」——那正是 2026-11-05 折叠凸包那轮
// 已经踩过的坑（`capacityDroppedRangesLocked` 的注释）。`gapRangesOfEvents` 给的是
// 精确的连续覆盖区间，本函数只按它记账。
//
// 返回错误时调用方必须把待办**放回去重试**——绝不静默吞掉账本写入失败。
func (m *Manager) recordVisibleDeliveryMiss(
	source SourceConfig,
	led *ledger.Ledger,
	key ledger.SourceKey,
	events []logtypes.Event,
	ev deliveryMissEvidence,
) error {
	if led == nil {
		return fmt.Errorf("ingest: 可见性降级缺少账本（source=%s）", source.LogSourceID)
	}
	runs := gapRangesOfEvents(events)
	if len(runs) == 0 {
		return fmt.Errorf("ingest: 可见性降级需要非空事件集合（source=%s）", source.LogSourceID)
	}
	// detail 必须写清四件事：区间规模、为何判丢、**判断依据**（供人工区分延迟 vs 真丢）、补救方向。
	// 运维看这一条就能判断「要不要从源重采这一段」，而不是只看到一句「丢了」。
	detail := fmt.Sprintf(
		"VL accepted the batch (HTTP 2xx) but %d/%d events are still not queryable after %d consecutive "+
			"verification rounds spanning %s (each round queried VL successfully; a failed query is never counted as a miss); "+
			"this means VL silently dropped them (retention window, field-count limit, or another server-side rejection) "+
			"and re-sending the same bytes will be dropped the same way; they are NOT retried any more — "+
			"remedy: re-collect this range from the source after fixing the rejection reason",
		ev.Missing, ev.Total, ev.Rounds, ev.Window.Round(time.Millisecond))
	// 先记缺口（可见度优先），再推 DROPPED 水位。
	// 顺序有意义：若缺口登记失败就不推水位，宁可让 reclaim 停着等人看到，
	// 也不要出现「水位过了但账本上没有任何痕迹」的形态。
	for _, run := range runs {
		if err := led.RecordGap(key, run.From, run.To, ledger.GapReasonDeliveredNotVisible, detail); err != nil {
			return fmt.Errorf("ingest: 登记「VL 接受但不可见」缺口失败: %w", err)
		}
	}
	for _, run := range runs {
		if err := led.RecordDroppedRange(key, run.From, run.To); err != nil {
			return fmt.Errorf("ingest: 降级为 DROPPED 失败: %w", err)
		}
	}
	// 数据丢失用 WARN 而不是 INFO：它是「静默变可见」的转折点，不能淹没在采集日志里。
	slog.Warn("投递被 VL 接受但连续多轮查不到，已降级为 DROPPED 并记录可见缺口（数据未落库）",
		"source", source.LogSourceID, "generation", source.SourceGeneration,
		"ranges", len(runs), "missing", ev.Missing, "total", ev.Total,
		"rounds", ev.Rounds, "windowMs", ev.Window.Milliseconds())
	return nil
}
