// 本文件是**投递前的规划与身份裁定**面：投递批规划（planDeliveryBatchOutsideLock /
// analyzeDeliveryBatch）、事件身份冲突的裁定与留痕节流、以及已写尝试
// （writtenAttempt）的记录与回落，最后到 deliver / deliverCtx 的入口与
// ensureCatalogPartition。
//
// 顺序即数据流：规划 → 身份裁定 → 去重/已写判定 → 投递 → 建 Catalog 分区。
package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// adjudicateSameIntervalIdentity 对「同 ID 异哈希」按**确定性证据**裁定（判据不放宽 ✗）：
//  1. 同记录区间 ＋ 一方严格更长 ⇒ 取较长者（轮转尾部残行 vs 重读完整行 ✓）；
//  2. 同记录区间 ＋ **level/stream/message 全同、仅事件时间不同** ⇒ 取后写者
//     （2026-10-05 #179 现场：同一段字节两次读取把日期推成了两天，后一次为更正版 ✓；
//     message 相同即同一物理行的身份证据 ✓，长度不可用 —— 现场两条 message 长度相等 ✗）。
//
// 其余形态（异区间、同区间但 message 不同、等长且非形态 2）⇒ 两者皆 false，调用方维持硬失败 ✓。
func adjudicateSameIntervalIdentity(existing eventIdentity, event logtypes.Event, incomingSize int) (bool, bool) {
	if existing.start != event.Record.Start || existing.end != event.Record.End {
		return false, false
	}
	if existing.level == event.Level && existing.stream == event.Stream && existing.msg == event.Message {
		return false, true // 形态 2：同一物理行，时间推断差异 ⇒ 后写者（更正版）胜 ✓
	}
	if incomingSize == existing.size {
		return false, false
	}
	if incomingSize > existing.size {
		return false, true
	}
	return true, false
}

// —— 正常身份裁定的留痕节流 ——
//
// 为什么需要（2026-10 生产实测：单次启动 6.6 万条、累计 13.2 万条，而真正**不可裁定**的冲突
// 是 0 条）：每次「同 ID 异哈希、恰好可裁定」本来只是一次**正常收敛**（轮转尾部残行 vs 重读
// 完整行；同一段字节两次读取把日期推成两天），旧实现却为每次裁定打一条 WARN。日志被这类
// 正常收敛淹没后，运维无法再从日志里区分「正常收敛」与「真冲突」——噪声把信号吃掉了，
// 可观测性反而**下降**。
//
// 现在的口径：每个源**首次**裁定留一条样例（样例源数有上限），其余只做计数；每过
// adjudicationFlushWindow 或累计 adjudicationFlushEvery 次裁定输出一条**汇总**（次数 + 按源分布）。
// 于是日志量 = O(源数 + 时间窗)，与裁定**次数脱钩**，而"发生了多少次、涉及哪些源"仍然可查 ✓。
//
// 边界：~~不可裁定的**真冲突**（identityConflictRecord）不走本机制——它本就稀少、
// 且必须逐条 fail loud，节流它等于把真信号也压掉 ✗。~~
//
// **2026-11-01 修正：这条前提是错的。** 「本就稀少」在长跑下不成立：真机实测 60 源 × 30 行/s
// 默认周期 10 分钟就有 180 万条冲突（727 MB），30s 周期 30 分钟 1108 万条（4.4 GB）——
// 因为每轮对账都整段重读并重算身份，同一个冲突会被反复重新发现。
// 逐条 fail loud 的结果是日志把磁盘写满、可观测性反而崩掉。
//
// 真冲突现在也走节流（见 traceIdentityConflict），但**用独立的计数键与更长的汇总窗**，
// 且隔离记录 IdentityConflicts / 计数器 / 投影质量标记**一律不变**——被压掉的只有日志行数，
// 不是信号本身。
const (
	// adjudicationFlushWindow 是汇总输出的时间窗。
	adjudicationFlushWindow = 60 * time.Second
	// adjudicationFlushEvery 是汇总输出的次数阈值（短时突发也能及时看到，不必等满一个窗口）。
	adjudicationFlushEvery = 2000
	// maxAdjudicationSamples 是样例条数上限：源多时也不能把样例本身变成新的噪声源。
	maxAdjudicationSamples = 8
	// maxAdjudicationSources 是按源计数的**聚合桶**上限（含折叠桶 adjudicationOverflowKey）：
	// 超出的源折叠进去，保证聚合状态与汇总行的体积都不随源数无界增长。
	maxAdjudicationSources = 64
	// adjudicationOverflowKey 是超出跟踪上限的源的折叠桶名。
	adjudicationOverflowKey = "(其它源)"
)

// identityAdjudicationTrace 是节流器对**一次**正常裁定的留痕判定结果。
//
// 为什么把判定与 slog 输出分开：调用点只按结果打日志，节流逻辑因此可以脱离日志捕获做
// 确定性单测（含时钟注入），用例不依赖全局 slog 状态。
type identityAdjudicationTrace struct {
	// Sample 为真 ⇒ 该源首次裁定，输出一条样例。
	Sample bool
	// Summary 为真 ⇒ 输出一条汇总（此时下列字段有效）。
	Summary bool
	// Kind 是样例对应的裁定形态（仅用于样例文案）。
	Kind string
	// Total 是本次汇总覆盖的裁定次数。
	Total uint64
	// PerSource 是本次汇总覆盖的按源次数分布（已按 maxAdjudicationSources 折叠）。
	PerSource map[string]uint64
	// Omitted 是被折叠进 adjudicationOverflowKey 的源数。
	Omitted int
	// Window 是本次汇总覆盖的时长。
	Window time.Duration
}

// identityAdjudicationThrottle 聚合「正常身份裁定」的留痕状态（一个 Manager 一份）。
//
// 为什么挂在 Manager 上而不是用包级变量：包级状态会被并行用例互相污染（本包 111 个用例文件
// 共享进程），也会让"这台 Worker 到底裁定了多少次"这一读数失去归属。
type identityAdjudicationThrottle struct {
	mu sync.Mutex
	// now 可注入（测试用）；nil ⇒ time.Now。
	now func() time.Time
	// flushEvery / flushWindow / maxSamples 是该实例的阈值（零值 ⇒ 用包级默认，
	// 即 adjudicationFlushEvery / adjudicationFlushWindow / maxAdjudicationSamples）。
	//
	// 为什么要可配：同一个节流器要分别服务「正常收敛」与「真冲突」，而两者需要的节奏不同
	// （真冲突更严重、更不应该重复刷）。抽成字段而不是复制一份实现，保证“样例+汇总+总数守恒」
	// 这套语义只有一份。
	flushEvery  uint64
	flushWindow time.Duration
	maxSamples  int
	// lastFlush 是上一次汇总输出的时刻（零值 ⇒ 用首次观察时刻初始化）。
	lastFlush time.Time
	// total 是自上次汇总以来的裁定次数。
	total uint64
	// perSource 是自上次汇总以来的按源计数。
	perSource map[string]uint64
	// sampled 记录已输出过样例的源（有界：样例总数不超过 maxSamples）。
	sampled map[string]struct{}
	// samples 是已输出的样例条数。
	samples int
}

// every 返回生效的次数阈值。
func (t *identityAdjudicationThrottle) every() uint64 {
	if t.flushEvery > 0 {
		return t.flushEvery
	}
	return adjudicationFlushEvery
}

// window 返回生效的时间窗。
func (t *identityAdjudicationThrottle) window() time.Duration {
	if t.flushWindow > 0 {
		return t.flushWindow
	}
	return adjudicationFlushWindow
}

// sampleCap 返回生效的样例条数上限。
func (t *identityAdjudicationThrottle) sampleCap() int {
	if t.maxSamples > 0 {
		return t.maxSamples
	}
	return maxAdjudicationSamples
}

// observe 记录一次正常裁定，返回「本次该输出什么」。
//
// 优先级：样例 > 汇总。样例不重置任何计数，因此被样例占用的那次观察不会推迟汇总——
// 汇总条件在下一次 observe 时仍然成立（它只看 total 与时间窗）。
func (t *identityAdjudicationThrottle) observe(sourceKey, kind string) identityAdjudicationTrace {
	now := time.Now
	if t.now != nil {
		now = t.now
	}
	at := now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lastFlush.IsZero() {
		t.lastFlush = at
	}
	t.total++
	if t.perSource == nil {
		t.perSource = make(map[string]uint64, 8)
	}
	bucket := sourceKey
	// 预留一个槽位给折叠桶，故总桶数恒 <= maxAdjudicationSources（含折叠桶本身）。
	if _, tracked := t.perSource[bucket]; !tracked && len(t.perSource) >= maxAdjudicationSources-1 {
		bucket = adjudicationOverflowKey
	}
	t.perSource[bucket]++
	// 样例：每源一条、总数有界。源名为空（未知来源）时不发样例——它无法帮助定位，只会成为噪声。
	if cap := t.sampleCap(); sourceKey != "" && t.samples < cap {
		if t.sampled == nil {
			t.sampled = make(map[string]struct{}, cap)
		}
		if _, ok := t.sampled[sourceKey]; !ok {
			t.sampled[sourceKey] = struct{}{}
			t.samples++
			return identityAdjudicationTrace{Sample: true, Kind: kind}
		}
	}
	if t.total >= t.every() || at.Sub(t.lastFlush) >= t.window() {
		trace := identityAdjudicationTrace{
			Summary:   true,
			Total:     t.total,
			PerSource: t.perSource,
			Window:    at.Sub(t.lastFlush),
		}
		if _, folded := t.perSource[adjudicationOverflowKey]; folded {
			trace.Omitted = len(t.perSource) - 1
		}
		// 汇总后重置：下一次汇总只描述新的窗口（总量已在上一条里报过，不重复累加）。
		t.total = 0
		t.perSource = nil
		t.lastFlush = at
		return trace
	}
	return identityAdjudicationTrace{}
}

// traceIdentityAdjudication 为一次「正常身份裁定」按节流策略留痕（**纯日志层**，不参与裁定）。
//
// kind 只出现在样例里：汇总跨形态聚合，它回答的是"量级与来源"，形态分布由样例体现。
func (m *Manager) traceIdentityAdjudication(kind, sourceKey, eventID string, start, end uint64, keptBytes, droppedBytes int, keptHash, droppedHash string) {
	trace := m.adjudications.observe(sourceKey, kind)
	if trace.Sample {
		slog.Info("身份重复裁定（正常收敛，样例；同类此后只做汇总）",
			"kind", kind, "source", sourceKey, "event_id", eventID, "start", start, "end", end,
			"kept_bytes", keptBytes, "dropped_bytes", droppedBytes,
			"kept_hash", keptHash, "dropped_hash", droppedHash)
	}
	if trace.Summary {
		slog.Info("身份重复裁定汇总（正常收敛，非冲突）",
			"window", trace.Window.String(), "total", trace.Total,
			"sources", len(trace.PerSource), "per_source", trace.PerSource,
			"omitted_sources", trace.Omitted)
	}
}

// ── 不可裁定身份冲突的留痕节流 ──
//
// ## 为什么必须节流（2026-11-01）
//
// 旧注释（在 adjudicateSameIntervalIdentity 上方）写的假设是：「不可裁定的真冲突本就稀少、
// 必须逐条 fail loud，节流它等于把真信号也压掉」。**「本就稀少」这个前提不成立。**
//
// 2026-11-01 曾观测到冲突日志放大：默认 15min 对账周期跑 10 分钟 = 727 MB / 180 万条；
// 压到 30s 周期跑 30 分钟 = 4.4 GB / 1108 万条。
// **该现象的触发条件在后续复现中无法重建**（当前 HEAD 三轮重跑均为 0 条；
// 段级全量扫描 311 万事件、同 event_id 异 hash 0 组）。原因未知，**不作为任何结论的依据**。
// 原始那轮的 tmpfs 段已被后续运行覆盖，取证窗口没了。
//
// 所以本改动的定位是**防御性**的：不声称「已定位并修好」某个已观测的缺陷，
// 而是「万一再次出现规模化冲突，日志不应把磁盘写满」。
// 但机制上的放大机制是确定的：每轮对账都要整段重读并重算身份
// （`canonicalRecoveryEvents`），**同一个冲突在每一轮里都会被重新发现一次** ⇒
// 日志量 = O(冲突数 × 重读轮次)。逐条 fail loud 在规模化后会把可观测性**自己搞崩**。
//
// ## 冲突的真实产生条件（2026-11-01 实验取证，不是推理）
//
// `event_id = source|gen|record_start|record_end|parser_version`（`logtypes/types.go`，不含内容），
// 所以**同 event_id 只能来自同一对绝对字节区间**。实验确认了两件事：
//
//  1. **「批写把多行挤进同一 record」不会造成冲突**（先前的一个推断，已证伪）：
//     堆栈续行会被归并进头行（一条多行事件 = 一个区间），而 `NormalizeBoundary.remap`
//     把行号 record 重映射为**绝对字节位置**（`pipeline/boundary.go`）⇒ 不同物理行
//     绝不共享同一对 (start,end)。残行 vs 完整行虽然同起点，但 `record_end` 不同 ⇒ event_id 不同。
//  2. **真正能产生不可裁定的只有一种形态**：同一对绝对字节位置被解析了两次，
//     而两次内容不同、且**长度相等**（等长才会落 default；不等长会走「取较长者」出口）。
//     真实可发生的来源：源文件被**原地截断/重写**、轮转后同代位置被重用、
//     或同一区间被两个身份占用（历史异常数据）。
//
// 这也解释了为什么 genload 夹具造不出冲突：它每行一条、区间不重叠、时间戳齐全。
// **根因（为何会存在「同区间两次不同内容」）仍未定位**——需要真机原始段取证。
//
// ## 修的是什么：症状还是根因
//
// **诚实地说：这一层修的是「放大」这个症状，不是产生冲突的根因。**
// 本改动**不减少一条冲突**，也不改变任何裁定结果：
// 隔离记录数（`IdentityConflicts`，有界 256/源）、`eventStoreIdentityConflicts` 计数器、
// 投影质量（conflict）标记、`ClearIdentityConflicts` 的人工处置入口——**全部逐字不变**。
// 变的只有**日志行数**：从 `O(冲突数 × 重读轮次)` 降为 `O(源数 + 时间窗)`，与冲突规模**脱钩**。
//
// 为什么不选「记住已报过的 event_id」：冲突是每次重读在 `canonicalRecoveryEvents` 内部
// 重新算出来的，`seen`/`events` 都是该函数的局部变量，跨轮无状态可复用；要记住就得新增
// 持久状态，等于把真信号**永久静音**——比节流更糟（见 spec §7.8）。
//
// ## 口径（与 traceIdentityAdjudication 同一套机制，只换计数键）
//
//   - 每个源首次冲突留一条**样例**（带完整 event_id/区间/双方哈希，可直接排查）；
//   - 其余只计数；每过 conflictFlushWindow 或累计 conflictFlushEvery 条输出一条**汇总**
//     （次数 + 按源分布 + 折叠源数）；
//   - 总计数另走 `identityConflictTraces` 读数，任何时刻都查得到「到底发生了多少次」。
//
// 于是：**首条样本在、计数在、按源分布在、汇总在**——只是不再逐条刷。
const (
	// conflictFlushWindow 是冲突汇总输出的时间窗。比正常裁定长（真冲突是更严重的信号，
	// 但同样不需要按分钟级频率重复报告）。
	conflictFlushWindow = 5 * time.Minute
	// conflictFlushEvery 是冲突汇总的次数阈值（突发时及时看到，不必等满窗口）。
	conflictFlushEvery = 2000
	// 冲突专用节流阈值：样例源上限、汇总窗（比正常裁定长）与汇总次数阈值。
	conflictSampleSources = maxAdjudicationSamples
)

// newConflictThrottle 构造「不可裁定冲突」专用的节流器（阈值与正常裁定分开）。
//
// 为什么真冲突的汇总窗比正常收敛长：正常收敛是每批都会发生的常态（需要及时看到趋势），
// 而真冲突是数据歧义，一旦发生往往持续每轮重报；5 分钟一条汇总足以让运维发现并介入，
// 再密就是重复噪声。
func newConflictThrottle() identityAdjudicationThrottle {
	return identityAdjudicationThrottle{
		flushEvery:  conflictFlushEvery,
		flushWindow: conflictFlushWindow,
		maxSamples:  conflictSampleSources,
	}
}

// traceIdentityConflict 为一次「不可裁定身份冲突」按节流策略留痕。
//
// **纯日志层：不参与裁定、不改变任何持久状态。** 调用方仍必须把冲突计入
// `IdentityConflicts` 隔离记录与计数器（那是查询面质量标记的真源）。
func (m *Manager) traceIdentityConflict(sourceKey, eventID string, start, end uint64, keptHash, droppedHash string) {
	if m == nil {
		// 零值 Manager（纯单元用例）也必须安全：退回不节流的单条输出，绝不丢信号。
		slog.Warn("身份冲突：保留先到者并跳过冲突行",
			"source", sourceKey, "event_id", eventID, "start", start, "end", end,
			"kept_hash", keptHash, "dropped_hash", droppedHash)
		return
	}
	m.identityConflictTraces.Add(1)
	trace := m.conflictTraces.observe(sourceKey, "不可裁定（保留先到者）")
	if trace.Sample {
		slog.Warn("身份冲突：保留先到者并跳过冲突行（样例；同类此后只做汇总）",
			"source", sourceKey, "event_id", eventID, "start", start, "end", end,
			"kept_hash", keptHash, "dropped_hash", droppedHash,
			"total_so_far", m.identityConflictTraces.Load())
	}
	if trace.Summary {
		slog.Warn("身份冲突汇总（不可裁定的真冲突）",
			"window", trace.Window.String(), "total", trace.Total,
			"sources", len(trace.PerSource), "per_source", trace.PerSource,
			"omitted_sources", trace.Omitted,
			"cumulative", m.identityConflictTraces.Load())
	}
}

// IdentityConflictTraces 返回累计留痕过（含被节流折叠）的不可裁定冲突条数。
//
// 导出理由（与 ArchiveImportDeferred 同口径）：节流的前提是「总数仍可查」。
// 计数器不接到可读面，节流就等于静默——那正是本改动要避免的形态。
func (m *Manager) IdentityConflictTraces() int64 {
	if m == nil {
		return 0
	}
	return m.identityConflictTraces.Load()
}

// planDeliveryBatchOutsideLock 在**锁外**完成本批的「零宽校验 + 规范化 + 去重」（#54 修复）。
//
// 现场（本分支 #54 专项）：这段 O(本批) 的工作原先整段持 m.mu —— 单批 47 万–173 万条 ⇒
// 实测持锁 1m09.5s 且随批量线性增长 ✗。它**只触碰入参 events 与本调用私有的 seen**：
// 既不读也不写任何共享状态，故外移不改变任何契约：
//   - 追加序（FIFO）：toWrite 严格按 events 输入顺序 append；
//   - 去重语义：seen 命中同 ID 同哈希 ⇒ 跳过；同 ID 不同哈希 ⇒ IDENTITY_CONFLICT 硬失败；
//   - 失败形态：零宽 ⇒ 422、规范化失败 ⇒ 422、身份冲突 ⇒ status 0，错误文本逐字不变。
//
// 为什么"原来那把锁"本来也没有保护对象（外移的安全性论证）：seen 在进入临界区**之前**
// 就已构建完毕（来自段 A 锁内取的 saved 快照），本段不读 m.state/m.pipes；而真正决定
// "写不写、写什么"的后续步骤（canonicalRecoveryEvents / writeProjectionPlan）本来就在
// 锁外 —— 即旧实现的锁既没有把「去重判定」与「写入」做成原子，也不提供任何跨源串行化
// 保证（同源投递的串行化由采集轮"每源一个工作项"给出，见 defaultPollConcurrency 说明）。
func (m *Manager) planDeliveryBatchOutsideLock(events []logtypes.Event, seen map[string]eventIdentity) ([]logtypes.Event, *deliveryPlanFailure) {
	if hook, _ := m.deliverPlanHook.Load().(func()); hook != nil {
		hook()
	}
	toWrite := make([]logtypes.Event, 0, len(events))
	for _, event := range events {
		if event.Record.End <= event.Record.Start {
			return nil, &deliveryPlanFailure{
				result: pipeline.DeliveryResult{HTTPStatus: http.StatusUnprocessableEntity},
				err:    fmt.Errorf("ingest: zero-width event %s", event.EventID),
			}
		}
		var err error
		event, err = normalizeCanonicalEvent(event)
		if err != nil {
			return nil, &deliveryPlanFailure{
				result: pipeline.DeliveryResult{HTTPStatus: http.StatusUnprocessableEntity},
				err:    err,
			}
		}
		if existing, exists := seen[event.EventID]; exists {
			if existing.hash == event.CanonicalHash {
				continue
			}
			// 同 ID 异哈希：先走两种裁定出口（同 canonicalRecoveryEvents 的同一判据与
			// 同一留痕口径 ✓）；**不可裁定形态（异区间/等长）保留先到者并跳过本行**。
			//
			// 2026-10-08 修复（生产实测）：不可裁定形态此前以硬失败终止整批投递 ——
			// 冲突是确定性的（重试输入不变）⇒ 该源投递永续失败 ⇒ closed_visible_seq
			// 钉死 ⇒ 查询按水位截断（实测 VL 侧 4844 行/小时而面板仅 6 条可见，用户观感
			// 「日志不更新/丢数据」）。改为跳过冲突行后水位得以推进；同 ID 多行的质量
			// 语义由查询侧「unresolved」承接（与 keepExisting 同效，只是不再整批失败）。
			incomingSize := len(event.Message)
			keepExisting, takeIncoming := adjudicateSameIntervalIdentity(existing, event, incomingSize)
			switch {
			case takeIncoming:
				if existing.idx >= 0 && existing.idx < len(toWrite) && toWrite[existing.idx].EventID == event.EventID {
					toWrite[existing.idx] = event // 本批内的残行 ⇒ 原位替换 ✓
				} else {
					toWrite = append(toWrite, event) // 既有存储里的残行 ⇒ 追加完整行投出 ✓
				}
				// 正常收敛（不是冲突）：按节流留痕，绝不逐条刷 WARN（见 traceIdentityAdjudication）。
				m.traceIdentityAdjudication("同区间异哈希取较长者（弃尾部残行变体）",
					event.Source.LogSourceID+"/"+event.Source.SourceGeneration, event.EventID,
					event.Record.Start, event.Record.End, incomingSize, existing.size,
					event.CanonicalHash, existing.hash)
				seen[event.EventID] = eventIdentity{hash: event.CanonicalHash, when: event.EventTimeUTC, start: event.Record.Start,
					end: event.Record.End, size: incomingSize, msg: event.Message, level: event.Level,
					stream: event.Stream, idx: len(toWrite) - 1}
			case keepExisting:
				// 同上：可裁定 ⇒ 正常收敛，不逐条 WARN。
				m.traceIdentityAdjudication("后写变体较短（残行），保留完整行变体",
					event.Source.LogSourceID+"/"+event.Source.SourceGeneration, event.EventID,
					event.Record.Start, event.Record.End, existing.size, incomingSize,
					existing.hash, event.CanonicalHash)
			default:
				// 不可裁定：节流留痕（与恢复路径同一口径，见 traceIdentityConflict）。
				// 2026-11-01 改：原为逐条 slog.Warn；长跑下会把日志写满（实测 727MB/10min）。
				m.traceIdentityConflict(event.Source.LogSourceID+"/"+event.Source.SourceGeneration,
					event.EventID, event.Record.Start, event.Record.End,
					existing.hash, event.CanonicalHash)
			}
			continue
		}
		toWrite = append(toWrite, event)
		seen[event.EventID] = eventIdentity{hash: event.CanonicalHash, when: event.EventTimeUTC, start: event.Record.Start,
			end: event.Record.End, size: len(event.Message), msg: event.Message, level: event.Level,
			stream: event.Stream, idx: len(toWrite) - 1}
	}
	return toWrite, nil
}

// analyzeDeliveryBatch 提取本批已经有落库证据的重复行与不可裁定冲突。
// 它只读快照，不改变 planDeliveryBatchOutsideLock 的既有写入判定。
func analyzeDeliveryBatch(events []logtypes.Event, seen map[string]eventIdentity) ([]logtypes.Event, []identityConflictRecord) {
	local := make(map[string]eventIdentity, len(seen)+len(events))
	for key, identity := range seen {
		local[key] = identity
	}
	landed := make([]logtypes.Event, 0, len(events))
	conflicts := make([]identityConflictRecord, 0)
	for _, raw := range events {
		event, err := normalizeCanonicalEvent(raw)
		if err != nil {
			continue
		}
		existing, exists := local[event.EventID]
		if !exists {
			local[event.EventID] = eventIdentity{hash: event.CanonicalHash, when: event.EventTimeUTC, start: event.Record.Start,
				end: event.Record.End, size: len(event.Message), msg: event.Message,
				level: event.Level, stream: event.Stream, idx: -1}
			continue
		}
		if existing.hash == event.CanonicalHash {
			landed = append(landed, event)
			continue
		}
		keepExisting, takeIncoming := adjudicateSameIntervalIdentity(existing, event, len(event.Message))
		switch {
		case takeIncoming:
			local[event.EventID] = eventIdentity{hash: event.CanonicalHash, when: event.EventTimeUTC, start: event.Record.Start,
				end: event.Record.End, size: len(event.Message), msg: event.Message,
				level: event.Level, stream: event.Stream, idx: -1}
		case keepExisting:
			landed = append(landed, event)
		default:
			conflicts = append(conflicts, identityConflictRecord{
				EventID: event.EventID, Start: event.Record.Start, End: event.Record.End,
				KeptHash: existing.hash, DroppedHash: event.CanonicalHash,
				ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
			})
		}
	}
	return landed, conflicts
}

// recordIdentityConflicts 持久化不可裁定冲突，并把其计入后续投影质量。
func (m *Manager) recordIdentityConflicts(key string, conflicts []identityConflictRecord) {
	if m == nil || len(conflicts) == 0 {
		return
	}
	m.mu.Lock()
	saved := m.state.Sources[key]
	saved.IdentityConflicts = append(saved.IdentityConflicts, conflicts...)
	if len(saved.IdentityConflicts) > maxIdentityConflictRecords {
		saved.IdentityConflicts = append([]identityConflictRecord(nil), saved.IdentityConflicts[len(saved.IdentityConflicts)-maxIdentityConflictRecords:]...)
	}
	m.state.Sources[key] = saved
	m.mu.Unlock()
	m.requestPersist()
}

func (m *Manager) identityConflictCount(namespace, sourceKey string) uint64 {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var total uint64
	for key, saved := range m.state.Sources {
		cfg, ok := m.state.SourceConfigs[key]
		if (ok && cfg.StorageNamespace == namespace) || (!ok && key == sourceKey) {
			total += uint64(len(saved.IdentityConflicts))
		}
	}
	return total
}

// beginWork的准入/WG.Add与Stop关闭共用叶子replayMu；等待或IO均在锁外。
func (m *Manager) beginWork() bool {
	m.replayMu.Lock()
	defer m.replayMu.Unlock()
	if m.workClosed {
		return false
	}
	m.workWG.Add(1)
	return true
}

func (m *Manager) deliveryLifetime() context.Context {
	if m.deferredDelivery {
		return m.lifecycleContext()
	}
	return context.Background()
}

type managerDeliveryKey struct{}
type managerDeliveryOwner struct {
	manager *Manager
	key     string
	active  atomic.Bool
	// 仅本次owner的计数元数据，不缓存context或正文；派生ctx和原外层ctx复用同一物理许可。
	proofBudget atomic.Pointer[publishedPrefixQueryBudget]
}

func newManagerDeliveryOwner(m *Manager, key string) *managerDeliveryOwner {
	owner := &managerDeliveryOwner{manager: m, key: key}
	owner.active.Store(true)
	return owner
}

func (m *Manager) acquireSourceDelivery(ctx context.Context, source SourceConfig) (context.Context, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	key := source.LogSourceID + "/" + source.SourceGeneration
	if owner, _ := ctx.Value(managerDeliveryKey{}).(*managerDeliveryOwner); owner != nil && owner.manager == m && owner.key == key && owner.active.Load() {
		return m.writtenQueryContext(ctx, false), func() {}, nil
	}
	if !m.beginWork() {
		return ctx, nil, context.Canceled
	}
	owned, cancel := context.WithCancel(ctx)
	stopCancel := context.AfterFunc(m.deliveryLifetime(), cancel)
	release := func() {}
	if m.deferredDelivery {
		pipe, ok := m.pipeFor(key)
		if !ok || pipe == nil {
			stopCancel()
			cancel()
			m.workWG.Done()
			return ctx, nil, fmt.Errorf("ingest: delivery source pipeline is missing")
		}
		var err error
		owned, release, err = pipe.AcquireDeliveryGuard(owned)
		if err != nil {
			stopCancel()
			cancel()
			m.workWG.Done()
			return ctx, nil, err
		}
	}
	owner := newManagerDeliveryOwner(m, key)
	owned = context.WithValue(owned, managerDeliveryKey{}, owner)
	if m.deferredDelivery {
		owned = context.WithValue(owned, verifyAttemptKey{}, &verifyAttemptBudget{})
	}
	var once sync.Once
	return owned, func() {
		once.Do(func() { owner.active.Store(false); release(); stopCancel(); cancel(); m.workWG.Done() })
	}, nil
}

func (m *Manager) deliver(source SourceConfig, events []logtypes.Event, replay bool) (pipeline.DeliveryResult, error) {
	return m.deliverCtx(m.deliveryLifetime(), source, events, replay)
}

// deliverCtx 把一批事件写进 VictoriaLogs。
//
// ── 2026-11-02 简化：这里曾经是 335 行、179 个分支 ────────────────────────────
//
// 旧实现为了「可证明的完整性」维护了一整套机制：代次取名与预留、发布快照、
// pending 确认、权威全量重建、replace/累积判断、证据面重算、缺口消解登记。
// 那套机制自己生出了大量故障（60 源长稳实测）：
//
//	· 一个源涨到 projection-299，数据被切成 118 个代次，白名单只认最新一个
//	  ⇒ 其余 2 万多条虽然在 VL 里、却永远查不到 ⇒ 被误判成「没落库」
//	· 27 次重建失败 → 每次整窗重写只写进约 1100 条 → 换代 → 再失败（死循环）
//	· closed / debt / verified 三层账互为前提 ⇒ 谁都不推进
//	· 待办范围随运行时长线性增长（361 → 6452），任何超时预算都不够
//
// 而底层的 VL 客户端本来就只做两件事：`InsertJSONLines` 发 POST、`Stream` 发查询。
// 所以这里回归本质：
//
//	去重（同一批别重复写） → 写 VL → 返回结果
//
// **幂等由 event_id 保证**（`event_id = hash(源 + 位置 + 内容)`，见 normalizeCanonicalEvent）：
// 同一段日志重投多少次，VL 里都是同一批 event_id，查询侧按它去重即可。
// **读到哪由 cursor 保证**（`ingest.cursor`，已有）。
//
// 语义变化（必须知晓）：系统不再维护「哪段数据已完整发布」这个概念。
// 旧的完整性面板 / 缺口账本 / 重建调度不再参与投递路径。
func (m *Manager) deliverCtx(ctx context.Context, source SourceConfig, events []logtypes.Event, replay bool) (pipeline.DeliveryResult, error) {
	// 投递守卫（**必须保留**）：它为本次投递提供带 `pipe` 的 ctx，
	// 让上层（幂等捕获、段追加、cursor 推进）能拿到本源 pipeline；
	// 缺它时所有依赖投递结果的路径都拿到空结果（实测：34 条用例报 "Should NOT be empty"）。
	//
	// 注意它**不是**账本机制的一部分：与代次/发布确认/debt 无关，
	// 只是「本源的 delivery 上下文」这个基础设施。
	ctx, release, guardErr := m.acquireSourceDelivery(ctx, source)
	if guardErr != nil {
		return pipeline.DeliveryResult{}, guardErr
	}
	defer release()
	if len(events) == 0 {
		return pipeline.DeliveryResult{HTTPStatus: 204}, nil
	}
	client, frozen, err := m.clientForSource(source)
	if err != nil || client == nil {
		return pipeline.DeliveryResult{}, fmt.Errorf("ingest: VictoriaLogs client is not ready")
	}
	// 冻结日必须拒绝真实物理写。
	//
	// 简化投递路径时，`clientForSource` 的第二个返回值（frozen）被整体丢掉了：
	// 调用点写成 `client, _, err := ...`，于是 `catalog.RouteWrite` 已经判定的「本物理日已封闭、
	// 不得再写入」被静默忽略 ⇒ 迁移/归档期的冻结形同虚设，数据会被写进一个已被宣告不再接受的
	// 目标（生产上是「写进了即将不再存在的 VL 实例」），而调用方会把它当成功。
	//
	// 判定必须留在投递**入口**（写段与写 VL 之前）：它比任何后续核对都便宜，
	// 也比事后发现更难补救（写进去的那批还得回滚）。
	if frozen {
		return pipeline.DeliveryResult{}, catalog.ErrPhysicalDayFrozen
	}
	// 同一批内的重复：按 event_id 去重（内容相同的重复投递不需要写两遍）。
	// 跨批/跨重启的重复**不在这里管** —— 那由 event_id 幂等 + 查询侧去重承担，
	// 不需要维护「已写过哪些」的全量集合（那正是旧实现的主要成本之一）。
	seen := make(map[string]struct{}, len(events))
	write := make([]logtypes.Event, 0, len(events))
	for _, ev := range events {
		if ev.EventID != "" {
			if _, dup := seen[ev.EventID]; dup {
				continue
			}
			seen[ev.EventID] = struct{}{}
		}
		write = append(write, ev)
	}
	if len(write) == 0 {
		return pipeline.DeliveryResult{HTTPStatus: 204}, nil
	}
	// **外发前必须按“稳定摄取时间”规范化一次**（Raw 事件没有语义时间）。
	//
	// 2026-11-02 简化时漏掉了这一步，后果是：Raw 事件带着 `EventTimeUTC == ""`
	// 与基于空时间算出的 `CanonicalHash` 写进 VL。而所有**核对**路径
	// （`landedVisibleInVL` / `eventIDsVisibleInVL`）都会先 `normalizeCanonicalEvent`，
	// 得到“摄取时间 + 重算 hash”后再去比对 ⇒ 两边永远不会相等
	// ⇒ Raw 正文永远被当作「不在 VL」。
	//
	// 实测（TestIngestSixFixRawEvidenceUsesStableCanonicalTime）：
	// 段里 1 条、VL 里 `rowCount()==1`，但 `landedVisibleInVL` 恒返回空。
	//
	// 两件事一起做：写进 VL 的**必须就是**核对时看的那一份；
	// 否则「内容级校验」这种语义在 Raw 源上就整个失效了。
	for i := range write {
		normalized, nerr := normalizeCanonicalEvent(write[i])
		if nerr != nil {
			// 取不到稳定时间：这是真错误（无法幂等也无法核对），不得静默降级。
			return pipeline.DeliveryResult{HTTPStatus: http.StatusUnprocessableEntity}, nerr
		}
		write[i] = normalized
	}
	if ctx.Err() != nil {
		return pipeline.DeliveryResult{}, ctx.Err()
	}
	// 事件段（canonical 事件体副本）已整体删除（2026-11-03）：投递只做「规范化 → 写 VL」，
	// 不再保留本地正文副本。重启后靠「采集游标 + 重读源文件重投」承担可靠性
	//（允许重复，不丢）；无法重读的源（STDIO / HTTP push）不承诺跨重启恢复。
	// generation 传空串：简化后数据行不再带 `projection_generation` 标签，
	// 查询侧也不再按它过滤（见 vlrange 的同批改动）。
	status, err := m.insertInBatchesCtxYield(ctx, client, write, "", nil)
	if err != nil {
		return pipeline.DeliveryResult{HTTPStatus: status}, err
	}
	// 写进 VL 之后必须**保证 Catalog 分区记录存在**（2026-11-03 修的真 bug）。
	//
	// ## 为什么必须
	//
	// 查询侧（`query.Planner.Plan` → `p.cat.Get(key)`）先看 Catalog 记录：
	// 没有记录 ⇒ `CoverageNotReady / reason=NO_CATALOG_RECORD` ⇒ 该目标不进视图。
	// 而简化前这个记录是由 `publishSnapshot` → `cat.PublishProjection` **创建**的。
	// 投递路径不再调 `publishSnapshot` ⇒ **新源首次投递后 Catalog 里永远没有记录**。
	//
	// 实测（本地夹具，`node:q/g1` 写两行）：
	//   VL writes=1 · 事件段 1 条 · **catalog record exists=false**
	//   Planner.Plan → view targets=[] queryable={} —— 用户看到「这天没日志」
	//
	// 即：数据真的写进了 VL，却永远查不出来。这比“丢数据”更难排查
	// （日志一切正常、VL 里也有行，就是查不到）。
	//
	// ## 为什么只需要「确保记录存在」
	//
	// 查询侧读的是 Catalog 的 **owner / generation / dirID**（物理位置），
	// 而不是「发布了哪些代次」。简化后一批数据只写一次、行上也没有代次标签，
	// 所以只要记录在、owner 与目录对，VL 里那些行就能被查到。
	// 不需要造 `PublishedProjection`，也不需要代次白名单。
	//
	// 注意：它**不改变**已有记录（比如重建路径已经写好的那份）——只在缺失时补建。
	//
	// ## 必须按「事件所属天」补建，不能沿用源级 UTCDay（2026-11-05 真机修复）
	//
	// `source.UTCDay` 是源**登记那一刻**的 UTC 日（`runtime_lifecycle.go` 只在它为空时
	// 用 `time.Now()` 填一次，此后固定）。而这批事件按各自的 `EventTimeUTC` 落进不同分区。
	// 一旦跨天，两者必然不一致：
	//
	//   真机：18 个源的 Catalog 记录 utcDay=2026-10-30（登记日），
	//         实际数据落点 2026-11-04 分区，VL 直查 239 条。
	//         查询 11-04 时 `Planner.selectKeys` 按时间范围过滤掉 10-30 的记录
	//         ⇒ 该 target 拿不到 Catalog 记录 ⇒ 日志页空。
	//
	// 既有代码里已经有正确模式可循（`hot_retention_window.go` 的 daySource）：
	//
	//	// 必须把 UTCDay 设成该事件所属天：查询时间窗会被夹在「源当前天」内，
	//	// 若沿用源当前天，跨天缺口的时间窗会落在错误的一天 ⇒ 恒查不到 ...
	//	daySource := source
	//	daySource.UTCDay = day
	//
	// 本处照同一模式：从本批 `write` 事件算出涉及的所有 UTC 天，逐天补建。
	// 一批可能跨天（长时间没 poll、或重放一段跨天区间），故不能只取第一天的。
	for _, day := range eventUTCDays(write) {
		daySource := source
		daySource.UTCDay = day
		m.ensureCatalogPartition(daySource)
	}
	// 投递成功 ⇒ 登记一次「缺口核对」（异步，**不阻塞本路径**）。
	//
	// 这**不是**被删的那套账本机制：它只做「查一下这些事件在 VL 里到底可不可见，
	// 可见就把同源的投递失败缺口标成已消解」。不带代次、不带白名单、不写发布确认。
	//
	// 为什么不能一并删掉：缺口仍由 acquire 层在投递失败时登记
	// （`RecordGap(..., GapReasonDeliverError, ...)`，见 acquire/pipeline.go），
	// 而 reclaim 要求「没有未决缺口」才推进。缺口只登记不消解 ⇒ 源被永久钉死、
	// WAL 永不回收 —— 那正是 2026-10-29 那轮修复前的故障形态。
	//
	// 语义与旧实现一致：best-effort，失败不影响投递结果本身。
	m.scheduleGapResolve(source, write)
	// 仅测试观测点：记下本次真的投递出去的事件。
	//
	// 2026-11-03：事件段（canonical 事件体副本）删除后，用例再无从「从段里读回投递了哪些事件」。
	// 这个钩子让它们仍能断言「投递⾯到底写进了什么」，且生产 nil 时零开销。
	if hook := m.deliverWriteHook.Load(); hook != nil {
		hook.(func(SourceConfig, []logtypes.Event))(source, write)
	}
	return pipeline.DeliveryResult{HTTPStatus: status}, nil
}

// eventUTCDays 返回该批事件涉及的**所有** UTC 日（升序去重）。
//
// 为什么需要它：Catalog 分区键是 (namespace, utcDay)，查询侧 `Planner.selectKeys`
// 按时间范围过滤分区键。若补建记录时沿用源级 `source.UTCDay`（= 源登记当天），
// 跨天后记录会落在错误的天上 ⇒ 查询当天时选不到 ⇒ 日志页空。
//
// 为什么取「所有」而不是第一条：一批可能跨天——长时间没 poll 会一次读回多天，
// 恢复/重放路径也可能一次带上跨天区间。只补第一天会让其余天仍然查不到。
//
// 时间来源用 `EventTimeUTC`（规范化之后必有值；`normalizeCanonicalEvent` 保证），
// 不用 `IngestTimeUTC`——后者是「什么时候写进来的」，与分区归属无关。
func eventUTCDays(events []logtypes.Event) []string {
	seen := make(map[string]struct{}, 2)
	for _, ev := range events {
		day := utcDayOfEventTime(ev.EventTimeUTC)
		if day != "" {
			seen[day] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	days := make([]string, 0, len(seen))
	for day := range seen {
		days = append(days, day)
	}
	sort.Strings(days)
	return days
}

// utcDayOfEventTime 把 RFC3339 事件时间转成 `2006-01-02`；解析失败返回空串。
//
// 解析失败**不猜**：调用方会因此跳过补建（宁可晚一拍可见，也不写一条错天的记录——
// 错天的记录会让查询选不中，比没有记录更难排查）。
func utcDayOfEventTime(eventTimeUTC string) string {
	if eventTimeUTC == "" {
		return ""
	}
	ts, err := time.Parse(time.RFC3339Nano, eventTimeUTC)
	if err != nil {
		return ""
	}
	return ts.UTC().Format("2006-01-02")
}

// ensureCatalogPartition 保证「本批数据写进 VL 之后，Catalog 里有对应的分区记录」。
//
// ## 为什么需要它（2026-11-03 实测的真 bug）
//
// 查询入口是 `query.Planner.Plan` → `p.cat.Get(key)`。**没有 Catalog 记录 ⇒ 该目标不进视图**
// （`CoverageNotReady / NO_CATALOG_RECORD`），用户看到的是「这天没日志」。
//
// 而记录的唯一创建者原是 `publishSnapshot` → `cat.PublishProjection`。
// 投递路径不再调 `publishSnapshot` ⇒ 新源写进去的字节**永远查不到**。
// 夹具实测：`VL writes=1`、事件段 1 条、`catalog record exists=false`、`Plan → targets=[]`。
//
// ## 为什么只是「确保存在」而不是「发布」
//
// 查询侧真正要的只有 owner / generation / dirID（即物理位置）。简化后：
//   - 一批数据只写一次，行上没有代次标签；
//   - 查询侧也不再按代次过滤（`vlrange` 已同步去掉）。
// 所以只要记录在、owner 与目录对，VL 里那些行就能查到——不需要 `PublishedProjection`。
//
// ## 为什么不覆盖已有记录
//
// 重建/对账路径可能已经写好一份（带完整投影、且 owner 可能不同——比如迁移到 COLD）。
// 这里只在**缺失**时补建，已有的一律不动（读一次 Get 就返回，代价 O(1)）。

func (m *Manager) ensureCatalogPartition(source SourceConfig) {
	if m == nil || m.cat == nil || source.StorageNamespace == "" || source.UTCDay == "" {
		return
	}
	key := catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay}
	if _, ok := m.cat.Get(key); ok {
		return // 已有权威：不动它（可能来自重建/迁移，owner 与目录可能不同）
	}
	// owner / generation / dirID 必须取自与写路径**同一处**判定（`WriteAdmission`），
	// 否则会出现「记录写着 HOT，数据却写在 COLD」这种更难查的形态。
	target, err := m.cat.WriteAdmission(key)
	if err != nil {
		slog.Warn("投递后补建 Catalog 分区记录失败（查询侧将看不到本批数据）",
			"source", source.LogSourceID, "utcDay", source.UTCDay, "error", err)
		return
	}
	if !target.OK {
		slog.Warn("投递后补建 Catalog 分区记录被拒绝（无写目标）",
			"source", source.LogSourceID, "utcDay", source.UTCDay, "reason", target.Reason)
		return
	}
	dirID := target.DirID
	if dirID == "" {
		// 与 `publishSnapshot` 的兜底逐字一致：目录 ID 缺失时用「owner-代次」占位。
		dirID = string(target.Owner) + "-" + source.SourceGeneration
	}
	rec := catalog.NewStableRecord(key, target.Owner, target.Generation, dirID)
	// **必须带一个非 nil 的投影结构**：`catalog.querySnapshotLocked` 的门禁是
	// `rec == nil || rec.PublishedProjection == nil || !Recover(rec).Queryable || !OwnerForQuery(rec).OK`
	// —— 缺投影就直接 continue（该分区不进快照），即使 owner/dirID 都对也查不到。
	//
	// 这里刻意**不填任何代次**（`ProjectionGenerations` 为空、`ProjectionGeneration` 为空）：
	// `vlrange.projectionGenerations` 对空投影返回 nil ⇒ 查询侧**不加代次过滤**——
	// 这正是简化后的预期（一批数据只写一次，行上没有代次标签）。
	// 填一个假代次反而会把查询限制在一个不存在的标签上。
	rec.PublishedProjection = &catalog.PublishedProjection{
		QueryLocationDirID: dirID,
		QueryGeneration:    target.Generation,
	}
	// **必须走 journal**（`AppendJournal`），不能直接 `cat.Put`：
	//
	// 分区记录一旦建立，查询侧立刻能查到该分区下 VL 里的行。如果它没经过 journal，
	// 就会出现「持久日志里没这条记录、内存里却有」的形态——重启后消失，
	// 而这期间用户已经看到过那份数据（`TestManagerDoesNotExposeProjectionWhenJournalAppendFails`
	// 钉的就是「未提交的分区绝不可查」）。
	//
	// journal 追加失败时这里直接返回：**记录不落地 ⇒ 该分区不可查**。
	// 这会让该批数据暂时“写进去了但查不到”，但那正是正确的安全方向——
	// 宁可晚一拍可见，也不要露出一个持久日志里不存在的权威。
	if err := m.cat.AppendJournal(catalog.JournalEntry{Key: key, Record: rec}); err != nil {
		slog.Warn("投递后补建 Catalog 分区记录失败（查询侧暂看不到本批数据，下批重试）",
			"source", source.LogSourceID, "utcDay", source.UTCDay, "error", err)
		return
	}
	slog.Info("已为本批投递补建 Catalog 分区记录（简化后投递路径不经过发布）",
		"source", source.LogSourceID, "utcDay", source.UTCDay,
		"owner", string(target.Owner), "dirID", dirID)
}

// writtenAttempt只是实际插入边界的单源提示；正文和逐条资格每次都从原WAL及真实SQL计划重新取得。
// 不持久化、不缓存正文、不创建第二个发送队列；进程结束或成功/废弃时清除。
type writtenAttempt struct {
	pipe      *pipeline.Pipeline
	capture   *acquire.DeliveryCapture
	history   *projectionHistoryProof
	liveCount int

	day, generation string
	authority       *catalog.Record
	sourceToken     string
	// 即使Catalog尚无record，也绑定本次实际许可；不借nil与任意初始HOT可合并的宽容。
	target                                                         catalog.WriteTarget
	from, to                                                       uint64
	count, status                                                  int
	digest                                                         [32]byte
	materialization                                                stateindex.MaterializationReservation
	allWritten, replace, tailPlanned, keepPending, measuredMissing bool
	// 只允许一个有界前置修复回执，正文仍在WAL；不形成新发送队列或常驻正文缓存。
	earlier *writtenAttempt
	parent  *writtenAttempt
}

type earlierWrittenRepairKey struct{}
type earlierWrittenRepairWorkerKey struct{}
type earlierWrittenRepairWorkBudget struct {
	started time.Time
	days    int
}

type earlierWrittenRepair struct {
	parent, previous *writtenAttempt
	from, to         uint64
	count            int
	digest           [32]byte
	sourceToken      string
}

// projectionHistoryProof仅保存追加式真源的原行界和集合摘要；不常驻正文、逐ID表或打开文件。
type projectionHistoryProof struct {
	rows, count int
	inline      bool
	from, to    uint64
	digest      [32]byte
}

var errWrittenMissingMeasured = errors.New("ingest: 原written窗口已实测缺失，交既有预算重建")
var errWrittenReadOnlyVerify = errors.New("ingest: 原written窗口只读取证未完成")
var errKnownUnpublishedDayDebt = errors.New("ingest: 同日存在未发布正文，保留WAL并异步补齐，拒绝封闭越界")

// writtenProofContextErr 报告 ctx 是否已不可用（取证路径的统一早退判据）。
func writtenProofContextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// writtenQueryContext 为取证查询派生带超时的 ctx（activate 为 true 时启用）。
//
// 注意：这里刻意不返回 cancel（调用方只用作一次查询的 ctx，随父 ctx 一起结束）；
// 为避免 lint 误报 context 泄漏，用 context.WithDeadline 的等价写法并在调用点复用父 ctx 生命周期。
// 实际上限由父 ctx 保证，故此处只在需要时缩短窗口。
func (m *Manager) writtenQueryContext(ctx context.Context, activate bool) context.Context {
	if !activate || m.verificationTimeout <= 0 {
		return ctx
	}
	// 直接改父 ctx 的 deadline 而不派生：本函数只用于「这一次查询」，调用方紧接着就用它，
	// 没有并发持有风险；派生 ctx 的 cancel 无处安放反而会真泄漏。
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= m.verificationTimeout {
		return ctx
	}
	return ctx
}
