// 本文件是**物理代次与发布**面：代次名的保留/释放（reserveProjectionGeneration*）、
// 探测（probeProjectionGenerationWithClient）、canonical 事件集合重建
// （canonicalRecoveryEvents*），以及 publish / publishSnapshot。
//
// 代次名的「不可复用」保证是 2026-10-01 生产事故的修复，改动前请先读
// nextProjectionGeneration 附近的说明。
package ingest

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// ── #63：重放回收（rolling drain） ────────────────────────────────────────────────────
//
// 现场：`reclaim` 动了但**速率 344 位/s/源**（1.14 亿需 3.7 天 ✗✗），且**复采差逐字相同** ⇒
// "一次性微推后再度停住" ✓。机制（读码坐实）：`TryReclaim` 的单次上界 = **当前覆盖段的 `CoversTo`**
// （ledger.tryReclaimLocked 的 `target := ref.CoversTo`）；而补段闸只在"**完全无覆盖**"（哨兵）时补 ⇒
// 位置**被陈旧段覆盖**（`CoversTo ≪ delivery`）时谁都不补 ⇒ 每轮只能推一个排空批（≈3500 位 / 10s ✓）。
//
// 修法（Z3-a，父级裁定）：**探测驱动的有界小循环** —— 每轮反复「探测（必要时补段）→ 推进」，
// 直到不再推进或触到单轮上界（≤8 步 / ≤200ms ✓）。典型形态只需 ~3 步：
//
//	① 推进到陈旧段末端 → ② 位置变"无覆盖" ⇒ 助手补 **`[reclaim, delivery]` 一整段** → ③ 推进到 delivery ✓
//
// ⇒ 一次调用吃完存量；生产仍灌料时每轮覆盖"自上次以来新增的已确认区间" ⇒ **追平生成速率** ✓。
//
// 不变量：上界仍是**已确认投递水位 `delivery`**（default-deny 不放宽 ✓，`reclaim ≥ delivery` 时零动作 ✓）；
// 每段仍逐段入账本（`delivery-confirmed-<from>-<to>` ✓ 可审计）；hold 优先（每步复读 ✓）；单轮有界 ✓。
// reclaimStep0RollingOnly 是「步 0：只推进/补段滚动回收」的**共用精简入口**（#66-⑤）——
// 两条面共用同一助手，避免各写一份而语义分叉 ✗：
//
//	· **自愈面**：selfHealPausedSource 步 0（#62 起 ✓；其准入＝已暂停 或 仍有未解决缺口）；
//	· **兜底环轮转窗口**：maybeResumePausedSources 的**早退之前**（#66-⑤ 新挂 ✓）。
//
// **只做这一步**：不做投递（步 1）、不做缺口消解（步 2）、不做恢复评估（步 3）——兜底环自己已有那三步
// 的等价路径（DeliverPending / autoResolveGapsFromPublished / EvaluateResume ✓），且那三步的准入红线
// （积压/容量类暂停 ✓）**不变** ✗（本入口只放宽"谁来推进回收"，不放宽"谁能被复活" ✓）。
//
// 为什么需要它（#66-⑤ 现场）：一条"采集仍在跑、只是回收链被卡住"的源（非积压类暂停 或 未暂停，
// 且零未解决缺口）三条面全都到不了它 ⇒ 新窗零 `滚动回收` 行、全库零「本轮未推进（回收被拒）」行
// ✗✗（被拒行只在 rolling 内部才会打 ⇒ rolling 从未被调用 ✓）。
func (m *Manager) reclaimStep0RollingOnly(p *pipeline.Pipeline, source SourceConfig) {
	if m == nil || p == nil {
		return
	}
	m.rollingReclaimRepair(p, source)
}

// rollingReclaimRepair 对单个源做一次有界回收推进。
//
// ## 2026-10-29 简化：不再需要「逐跳推进」框架
//
// 旧实现是为「分段链」设计的：每次 `repairStalledReclaim` 只能跨过一个恢复段
// （`target = ref.CoversTo`），于是 reclaim 与 delivery 之间夹着 N 段就要跑 N 轮，
// 才需要 `MaxSteps`（单轮最多跳几段）与 `Budget`（单轮墙钟）两个旋钮。
//
// reclaim 改按投递证据推进后，一次 `TryReclaim` 就直接到
// `min(REQUEST_DONE 连续覆盖末端, max(read, durable))` —— **一跳到顶**。
// 两个旋钮因此失去作用对象，已连同 `ReclaimRollTuning.MaxSteps/Budget` 一并删除；
// 保留 `ReclaimRollSnapshot` 与累计计数，是因为它们是现场读数面（推进了多少、花了多久）。
//
// 调用方（`reclaimStep0RollingOnly`）只关心「这轮动了没」与「读数」，与旧行为一致。
func (m *Manager) rollingReclaimRepair(p *pipeline.Pipeline, source SourceConfig, contexts ...context.Context) {
	if m == nil || p == nil || p.Ledger() == nil {
		return
	}
	start := time.Now()
	before, ok := p.Ledger().Positions(p.Key())
	if !ok {
		return
	}
	_, err := p.TryReclaim()
	// `ErrReclaimPrecondition` 是调用方逐条 ACK 复核未通过的**正常形态**（不是错误），
	// 与旧实现同口径：只有其它错误才上报。
	if err != nil && !errors.Is(err, ledger.ErrReclaimPrecondition) {
		if m.noteSourceError(source.LogSourceID, err.Error()) {
			slog.Warn("滚动回收：推进被拒（下轮重试）", "logSourceID", source.LogSourceID, "error", err.Error())
		}
	}
	after, ok := p.Ledger().Positions(p.Key())
	if !ok {
		return
	}
	if after.Reclaim <= before.Reclaim {
		return // 无活可做：不打日志（避免刷屏）
	}
	advanced := after.Reclaim - before.Reclaim
	elapsed := time.Since(start)
	m.reclaimRollRounds.Add(1)
	m.reclaimRollSteps.Add(1)
	m.reclaimRollAdvanced.Add(int64(advanced))
	m.reclaimRollFirstAdvance.Add(int64(advanced))
	m.reclaimRollNanos.Add(int64(elapsed))
	m.reclaimRollLast.Store(ReclaimRollSnapshot{
		Reclaim: after.Reclaim, Delivery: after.Delivery,
		FirstAdvance: advanced, Steps: 1, Advanced: advanced, Elapsed: elapsed,
	})
	slog.Info("滚动回收：推进完成",
		"logSourceID", source.LogSourceID,
		"reclaim", after.Reclaim, "delivery", after.Delivery,
		"advance", advanced, "elapsedMs", elapsed.Milliseconds())
	// 静默未暂停源不会再产生采集提交；真实回收推进也须交既有消费者落库。
	m.requestPersist()
}

// defaultReclaimRollMaxSteps / defaultReclaimRollBudget 已于 2026-10-29 删除（同上：作用对象消失）。

// ReclaimRollTuning 已于 2026-10-29 删除：它只有 `MaxSteps` / `Budget` 两个字段，
// 而它们服务的是「分段链逐跳推进」（一次只能跳一段 ⇒ 需要限制单轮跳几段、花多久）。
// reclaim 改按投递证据推进后一次就到顶，两个旋钮失去作用对象。
// 配置键 `log_index.reclaim_roll.max_steps` / `.budget` 同步删除（viper 忽略未知键，
// 旧 worker.yml 里留着不会报错）。

// ReclaimRollSnapshot 是「上一轮回收」的读数（#63-Z3-c；现场读数与红/绿判据同一套数 ✓）。
//
// 2026-10-29：`Steps` 恒为 1（一次 `TryReclaim` 即可到顶，不再逐段跳），
// `FirstAdvance` 与 `Advanced` 因此总是相等。两个字段保留是为了让既有读数面与
// 累计计数器的形状不变（现场/dashboard 按它们算「位·s⁻¹」）。
type ReclaimRollSnapshot struct {
	Reclaim      uint64        // 本轮结束时的回收位置
	Delivery     uint64        // 同刻的已确认投递水位（上界）
	FirstAdvance uint64        // 首步推进量 = 陈旧覆盖段的上界缺口 ✓
	Steps        int           // 本轮步数
	Advanced     uint64        // 本轮总推进位数
	Elapsed      time.Duration // 本轮墙钟
}

// ReclaimRollingStats 返回滚动回收读数（累计 + 上一轮）⇒ 位·s⁻¹ 可直接算 ✓。
type ReclaimRollingStats struct {
	Rounds       int64
	Steps        int64
	Advanced     uint64
	FirstAdvance uint64
	Elapsed      time.Duration
	Last         ReclaimRollSnapshot
	// #66-⑤ 诊断（只读）：自愈面**限频闸**的通过/跳过累计 —— 见 Manager.selfHealDuePass 字段注释 ✓。
	// 状态闸（未暂停且零缺口 ⇒ 早退）**不**计入任何计数 ⇒ 组合读法：两值恒为 0 ⇒ 该源连状态闸都
	// 没放行（或自愈面根本没被调用）；skip 恒涨而 pass 不动 ⇒ 全被限频挡住 ✓。
	SelfHealDuePass int64
	SelfHealDueSkip int64
	// #179 第 1 步（③）：**受阻类别**（只读）——"为什么这条缝/这回补段没发生"一眼可读 ✓。
	BlockedSegmentHold    int64 // 段 hold（运维钉住的段覆盖该位置 ✓）
	BlockedConfigHold     int64 // 配置层 hold（连补段都不做 ✓）
	BlockedNoMaterial     int64 // 无"已确认但未覆盖"的区间（to <= from ✓）
	BlockedWatermarkMoved int64 // 水位在复读窗口内变化（变则弃 ✓）
	BlockedNoCopy         int64 // 责任转移被拒：该区间还没有确认副本（否则会删掉未投递数据 ✓）
	BlockedStallWarns     int64 // ② N 轮无进展的告警次数 ✓
}

// ReclaimRollingStats 读取滚动回收读数（#63-Z3-c）。
func (m *Manager) ReclaimRollingStats() ReclaimRollingStats {
	if m == nil {
		return ReclaimRollingStats{}
	}
	st := ReclaimRollingStats{
		Rounds: m.reclaimRollRounds.Load(), Steps: m.reclaimRollSteps.Load(),
		Advanced: uint64(m.reclaimRollAdvanced.Load()), FirstAdvance: uint64(m.reclaimRollFirstAdvance.Load()),
		Elapsed:         time.Duration(m.reclaimRollNanos.Load()),
		SelfHealDuePass: m.selfHealDuePass.Load(),
		SelfHealDueSkip: m.selfHealDueSkip.Load(),

		BlockedSegmentHold:    m.blockedSegHold.Load(),
		BlockedConfigHold:     m.blockedConfigHold.Load(),
		BlockedNoMaterial:     m.blockedNoMaterial.Load(),
		BlockedWatermarkMoved: m.blockedWatermark.Load(),
		BlockedNoCopy:         m.blockedNoCopy.Load(),
		BlockedStallWarns:     m.blockedStallWarns.Load(),
	}
	if last, _ := m.reclaimRollLast.Load().(ReclaimRollSnapshot); last.Steps > 0 || last.Advanced > 0 {
		st.Last = last
	}
	return st
}

// noteReclaimBlocked 按类别计一次「回收被拦下」并打一条按源去重的日志。
//
// 类别只剩 `no-confirmed-copy`（投递结果未被确认）——其余四类（seg-hold / config-hold /
// no-material / watermark-moved）随恢复分段与补段链一并删除（2026-10-29）。
func (m *Manager) noteReclaimBlocked(category string, p *pipeline.Pipeline, source SourceConfig) {
	if m == nil {
		return
	}
	switch category {
	case "no-confirmed-copy":
		m.blockedNoCopy.Add(1)
	}
	key := ""
	if p != nil {
		key = p.Key().LogSourceID + "/" + p.Key().SourceGeneration
	}
	if key == "" {
		key = source.LogSourceID
	}
	m.blockedLoggedMu.Lock()
	if m.blockedLogged == nil {
		m.blockedLogged = make(map[string]struct{})
	}
	dup := false
	if _, ok := m.blockedLogged[category+"|"+key]; ok {
		dup = true
	} else {
		m.blockedLogged[category+"|"+key] = struct{}{}
	}
	m.blockedLoggedMu.Unlock()
	if !dup {
		slog.Info("回收推进被拦下（首次可见；后续同源同类不再打点）",
			"logSourceID", source.LogSourceID, "category", category)
	}
}

// releaseRecovery 在「请求结果已确认」之后推进回收水位（2026-10-29 简化）。
//
// ## 旧实现做什么、为什么不要了
//
// 旧语义是「把 WAL 责任转移给 projection://<代次>」：建恢复分段 → 逐状态迁移 → 等到
// `Delivery >= to` 且已发布证据覆盖 → RELEASED → CLEANED → 才让 reclaim 跨过 CoversTo。
// 那一整套的前提是「WAL 里还有未投递副本，必须有人接管责任才能删 WAL 行」。
// WAL 改纯内存队列后（`source_wal` 表已删）前提消失：队列本来就不存副本，也无需别人接管。
//
// 现在这里只剩两件真实的事：
//  1. **确认这段确实落地过** —— `Delivery >= to` 且 REQUEST_DONE 证据覆盖 [reclaim, to)；
//     不满足就什么都不做（条目留在队列，由投递欠账步重投）。
//  2. **推进 reclaim** —— 账本门禁 `reclaim = min(delivery, max(read,durable))` 负责上界，
//     本函数只触发一次推进与剪枝。
//
// 第 1 条不是形式主义：`acquire.Pipeline.Ingest` 的 suppressRecursiveVL 分支为避免
// 「失败日志再写回 VL」会 `return nil`，投递失败因此被上层读成成功（2026-10-21 数据丢失事故
// 正是这个形状）。判据取 `Delivery >= to`（`RecordHTTPResult` 只在投递成功后由
// `acquire.Pipeline` 调用，故它恰好等价于「这段确实落地过」）。不满足即返回 nil 而不报错是
// 刻意的：调用点（worker 自源）正是为了避免「失败 → 记日志 → 又被自己采集」的递归才吞掉
// 投递错误的，这里再抛错会把同一条递归链重新接上。
func (m *Manager) releaseRecovery(source SourceConfig, events []logtypes.Event, contexts ...context.Context) error {
	if len(events) == 0 {
		return nil
	}
	ctx := m.deliveryLifetime()
	if len(contexts) > 0 && contexts[0] != nil {
		ctx = contexts[0]
	}
	ctx = m.writtenQueryContext(ctx, false)
	if err := writtenProofContextErr(ctx); err != nil {
		return err
	}
	key := source.LogSourceID + "/" + source.SourceGeneration
	m.mu.Lock()
	p := m.pipes[key]
	m.mu.Unlock()
	if p == nil {
		return fmt.Errorf("ingest: pipeline %s missing for reclaim proof", key)
	}
	if m.deferredDelivery {
		defer m.pruneCommittedReceipt(key, p)
	}
	var to uint64
	for _, event := range events {
		to = max(to, event.Record.End)
	}
	positions, ok := p.Positions()
	if !ok {
		return fmt.Errorf("ingest: missing ledger positions for %s", key)
	}
	entry := p.Ledger().Get(p.Key())
	if positions.Delivery < to || entry == nil || !ledger.CoveredByPositionRanges(ledger.RequestDoneRangesOf(entry), positions.Reclaim, to) {
		m.noteReclaimBlocked("no-confirmed-copy", p, source)
		return nil
	}
	// 推进回收并按当前水位剪枝。失败不置死：下一轮投递/维护环会自动重试。
	var reclaimErr error
	if m.deferredDelivery {
		_, reclaimErr = p.TryReclaimThrough(to)
	} else {
		_, reclaimErr = p.TryReclaim()
	}
	if reclaimErr != nil && !errors.Is(reclaimErr, ledger.ErrReclaimPrecondition) {
		return reclaimErr
	}
	return nil
}

func nextProjectionGeneration(current string) string {
	if current == "" {
		return "projection-1"
	}
	var n int
	if _, err := fmt.Sscanf(current, "projection-%d", &n); err == nil && n > 0 {
		return fmt.Sprintf("projection-%d", n+1)
	}
	return current + "-rebuild"
}

// 物理代次名的「不可复用」保证（2026-10-01 生产事故修复）。
//
// 背景：投影的隔离完全落在物理代次名上——写入按名字打标、校验按名字过滤、查询侧白名单也按
// 名字列举。而名字来自本机状态的单调计数器（nextProjectionGeneration）。状态一旦重生
// （旧 JSON→SQLite 迁移、scripts/rollback-log-index.sh 把归档 JSON 改回、state 被重置），
// 计数器就从头开始，新名字与 VL 里**上一轮生命周期**留下的行重名。VL 只追加不删除
// （「取代」只改查询侧白名单，物理行保留到 retention 到期），于是校验查询把旧行读成本次写入
// 的内容 → `unexpected or duplicate projection event_id` → 校验永不通过 → 运行时创建失败、
// 采集停摆；重试若仍从同一状态出发，还会反复写同一个名字，把 VL 越写越脏。
// 生产实测（2026-10-01 05:20）：projection-88 撞上当天 03:22 那一轮同名整窗重发，三次重试
// 全写 88，27 分钟无进展，最终只能空库重建恢复。
//
// 修复：选代次名前先问 VL「这个名字在该源上是否已有行」，已有就按 1/2/4/8… 递增跳到未占用的
// 名字。探测失败不阻断投递（写入自身失败由既有语义处理），只告警后沿用旧行为；探测覆盖本次
// 写入路由所在的 VL 目标（跨 VL 目标的极端情形仍按旧行为，不会比修复前更差）。
const (
	// maxProjectionGenerationProbes 是单次选名的探测上限。按 2 的幂递增，16 次可越过约 65535 个
	// 已占名字（真实事故只越过 7 个：88→94），同时保证极端情形下不会无限探测。
	maxProjectionGenerationProbes = 16
	// projectionProbeTimeout 是单次占用探测的超时。探测只是「避免重名」的保障，不允许把投递卡在
	// 网络上，故用远小于校验窗口（默认 5 分钟）的短超时。
	projectionProbeTimeout = 5 * time.Second
	// 探测窗取「HOT retention 全期」而非仅近期。
	//
	// **2026-10-10 扩窗（本项修复的根因之一，生产铁证）**：原值 3d 让这道防重名守卫对
	// 「事件日早于 3 天」的重放**结构性失明**——探测按 `_time`（事件时间）过滤，而重放写的
	// 正是历史日（现场 `2026-10-05`，距 now 已 5 天）。于是探测恒报「该名字未占用」，
	// 同一个物理代次名被反复写进 VL：
	//   · `inst:220/file` 的 `projection-130` 在 10-05 这天落了 **2,294,248** 行
	//     （= 573562 × 4，即同名写了 4 遍）；
	//   · 逐字段校验按 `projection_generation:=<同名>` 过滤 ⇒ 每个 event_id 都有多份 ⇒
	//     `unexpected or duplicate projection event_id`（**确定性**失败）⇒ 永不发布 ⇒
	//     下一轮再判缺失 ⇒ 无界重发（生产 407 次）。
	// 实测对照（同一 VL、同一天窗）：3d 窗 `count()=0`（误判未占用）；满窗 `count()=2294248`。
	//
	// 取 31d 的依据：它等于 VL 侧硬上限 `-search.maxQueryTimeRange=31d`（再宽会被 **400**
	// 拒绝，那正是 2026-10-08 那次「探测静默失效」的成因），且 HOT 层 retention=30d ⇒ 这个窗口
	// **足以覆盖 HOT 中一切可能造成碰撞的行**（更早的行已被 retention 删除）。
	// 注意：本常量是**总跨度**上限，窗口两端的构造必须一起守住它（见 projectionProbeSpan
	// 与 projectionProbeFutureSlack 的说明——上界前移 1 天得以起点后移 1 天为代价）。
	// 诚实边界：COLD 层 retention=730d，其 31d 以外的同名旧行仍探测不到；那些行既不可查询、
	// 也会被 COLD 的 retention 逐步清掉，且 COLD 写入走独立客户端（本函数按写路由取客户端），
	// 因此不构成本次现场形态。此边界记录于此，不假装已覆盖。
	//
	// 成本：>3d 窗口要读盘（5s 级）。但本探测只在**分配新代次时**发生（每源每天每次重建一次），
	// 且已并入 `verifyLimiter` 天花板；与之对照的是「同名反复重写 57 万条 × N 次」的 VL 体积
	// （生产已累积数百 GB）。这里明确选择正确性。
	// projectionProbeSpan 是探测窗的**总跨度上限**（不是「起点回看量」）。
	//
	// **必须 ≤ VL 侧 `-search.maxQueryTimeRange`（本仓启的是 31d），否则整条重名防护静默失效。**
	// VL 按**区间长度**判定且比较符是**严格大于**：实测 744h 返回 200、744h+10ns 返回 400
	// `too big time range selected`（见 vlsup/cmdline.go 的实测记录与不变式注释）。
	// 一旦超限，`projectionGenerationUsed` 直接报错 ⇒ `probeProjectionGenerationWithClient`
	// 走「探测失败按未占用继续」⇒ VL 侧占用判据被整条跳过，只剩进程内 CAS；
	// 状态回滚/重置/迁移让计数器回退时，新名即撞上上一生命周期的同名行 ⇒
	// `unexpected or duplicate projection event_id` ⇒ 永不发布 ⇒ 采集停摆。
	// 这正是 2026-10-08 那次事故的成因（探测被 400 拒绝），也是 FR-496 的停摆形态。
	//
	// 因此窗口两端**必须一起**受本常量约束：`end - start ≤ projectionProbeSpan`。
	// 取 31d 的依据见下（等于 VL 硬上限，落在被接受的一侧）。
	projectionProbeSpan = 31 * 24 * time.Hour
	// projectionProbeSlackDays 是探测窗在保留期之外多留的余量（天）。
	//
	// 为什么需要余量：本仓历史上把「31d」当作 retention 30d + 1d，那 1 天用于吸收
	// 「边界那一天的整天跨度」——保留期按时刻算，而碰撞探测按天算，两者差一天。
	projectionProbeSlackDays = 1
	// projectionProbeFutureSlack 把探测窗的上界从 now 再向后放一天。
	//
	// **为什么上界不能取 now（2026-10-12 修复）**：日志行多为「只有时刻、没有日期」的形态
	// （`[12:00:01] ...`），其事件时间由 normalize 的 applyClock 以 base 为锚就近归日
	// （±12h）。于是同一行的时间轴**可以合法落在未来**——例如 base 锚在当日 UTC 00:30 时，
	// `[12:00:01]` 会解析成当日 12:00:01Z，而此刻 now 才 01:45。实时解析走
	// `effectiveBase()` 的 `time.Now()` 兜底（pipeline 只在**归档日**导入时才显式给
	// BaseTime=该日中午），所以这不是假设情形。
	//
	// 若窗口上界取 now，这类未来行落进盲区 ⇒ 探测恒报「该名字未占用」⇒ 绕过
	// `claimGenerationLocked` 直接占住该名 ⇒ 撞上既有同名行 ⇒ 正是本函数要消灭的
	// `unexpected or duplicate projection event_id`（FR-496 停摆形态）。
	// 实测（本仓 generation_reuse 回归）：夹具行 `[12:00:01]` 落成当日 12:00:01Z，
	// 探测在 `end=now` 下报未占用、重放写回同名 `projection-3`；仅把上界推到 now+1d
	// 即进位到 `projection-4`（探测量 2→4）——因果由此确认。
	//
	// 取 1 天而非更多：applyClock 把时刻约束在 base 的 ±12h 内，而 base 在生产不晚于
	// now，故事件时间最多超前 now 约 12h；1 天覆盖该上界并留足边界裕量。
	//
	// **代价与守恒**：上界前移 1 天，起点就**必须**相应后移 1 天（见下表），
	// 以守住总跨度 ≤ projectionProbeSpan —— 上界前移而不收起点会得到 32d > 31d，
	// 触发上面描述的整条防护失效。故实际窗口是 [now-30d, now+1d]，总跨度恰 31d。
	projectionProbeFutureSlack = 24 * time.Hour
)

// projectionProbeWindow 返回代次占用探测窗的**总跨度**。
//
// **必须由热层保留期派生，不能是常量**（2026-10-22 复审 P1-3）。
//
// 这个窗口的职责是：找出「同名代次在该保留期内的任意一天是否已有旧行」。因为
// 「整窗重发」的校验会把新代次的行与旧代次混在一起，只要旧行还可能在保留期内被查到，
// 就必须能探测到，否则 `projectionGenerationUsed` 判「未占用」⇒ VL 侧拒绝重名行
// （`unexpected or duplicate projection event_id`）⇒ 永不发布 ⇒ 采集停摆。
// 这正是 2026-10-08 那次事故的形态（当时窗口 1d，硬编码 31d 修的）。
//
// 但 31d 是**常量**，而保留期是可配置的：`DefaultRetentionPeriod` 在 2026-10-22
// 提到 120d、默认热窗口 90d 之后，31d 的探测窗只覆盖保留期的前 31 天 ——
// 第 31～90 天的同名旧行探测不到，重名防护在原地方重新失效。
// 也就是说「把上限从 31d 改成由 retention 派生」这件事只做了一半就停了。
//
// 故改为：`max(常量下限, hotRetention + 余量)`。取 max 而非直接替换，是为了
// 兼容 hotRetention 未被配置（0 值）的测试与旧调用方 —— 那时退回历史验证过的 31d。
func (m *Manager) projectionProbeWindow() time.Duration {
	// **窗口必须覆盖整个热层保留期**：探测的职责是「该名字在保留期内任意一天是否已有旧行」，
	// 覆盖不到就判「未占用」⇒ 代次重名 ⇒ VL 拒绝重名行 ⇒ 永不发布（2026-10-08 事故形态）。
	//
	// 2026-10-22 复审 P1-2 纠正了此前的一个**假两难**：先前担心「派生值超过 VL 硬上限会被
	// 400 拒绝」，于是加了「超限就钳回 31d」。但 `-search.maxQueryTimeRange` 已由
	// `QueryTimeRangeLimit()` 派生为 `retention + 1d`，而 `m.hotRetention` 与它读的是
	// **同一个配置字段**（apps/worker/main.go 两处都取 cfg.LogVL.RetentionPeriod）。
	// 于是：
	//     派生窗 = hotRetention + 1d = retention + 1d = 服务端上限   （恒等）
	// 请求恰好落在 VL「严格大于才拒」的**被接受一侧**，不会 400。所以钳制没有任何保护作用，
	// 只会在默认 retention=120d 的部署上留下「第 31～120 天探测不到」的原始盲区 ——
	// 即本次改造声称要修掉的那个缺口原样保留。
	//
	// **配置了就一律用派生值，不与常量取大**：取大会让「保留期很短」的部署也去扫 31d，
	// 而 VL 侧的 `-search.maxQueryTimeRange` 是按同一个 retention 派生的（retention+1d），
	// 于是 31d > 8d 的窗口会被 400 拒 ⇒ 探测整条失效（比覆盖不全更糟）。
	// 这个 bug 是本轮夹具（hotRetention=7d）当场抓到的。
	//
	// 常量 `projectionProbeSpan` 如今只剩一个用途：**兜住未配置的调用方**
	// （`hotRetention == 0`，即测试夹具与不关心保留期的旧调用路径），
	// 那时退回历史上验证过的 31d（对应 30d retention，生产现役值）。
	// 它不再是「配置值的下限」，也不再参与任何取大运算。
	if m == nil || m.hotRetention <= 0 {
		return projectionProbeSpan
	}
	return m.hotRetention + projectionProbeSlackDays*24*time.Hour
}

// nextFreeProjectionGenerationWithClient 是探测的实现（显式收客户端，便于按天路由与用例注入）。
//
// **刻意不预留**（reserve=false）：本函数只回答「下一个未占用的名字是什么」，不写预留表。
// 生产写入路径一律走 reserveProjectionGeneration（探测 + 预留原子完成）。
// 为什么必须保留这个「不预留」的形态：它同时供测试与只读诊断使用，而「**同一状态重复询问
// 必须得到同一结论**」是既有回归守住的契约（TestProjectionGenerationProbeIsCachedAndInvalidatedOnWrite）；
// 若本入口也登记预留，第二次询问就会把自己刚登记的条目当成"被占用"而进位 ⇒ 结论漂移。
func (m *Manager) nextFreeProjectionGenerationWithClient(client *vlsup.Client, source SourceConfig, current string) string {
	generation, _ := m.probeProjectionGenerationWithClient(client, source, current, false, "", 0)
	return generation
}

// reserveProjectionGeneration 为一次**即将写入**的投递原子地取一个未占用的物理代次名。
//
// 为什么必须有它（2026-10-10 生产实测，本缺陷的机制本体）：代次名原先由
// nextFreeProjectionGeneration 在**取 publishMu 之前**分配，判据是「实探 VL 未占用」＋
// 「缓存该名可用」。两条同源投递各自实探「未占用」即双双算出**同一个名字**，随后各写一份
// 完整副本——生产 inst:172/file 实测：08:00:47.093/.101/.102 三条「投影写入开始 gen=projection-1
// events=60000」，VL 侧同一 (源, 代次) 60000 唯一 event_id / 180000 行（3×）。
//
// 语义（与缓存共用 m.mu，无新增锁 ⇒ 无新增锁序边）：
//   - 整个「探测 → 预留」在 **m.mu 内**完成，故「同源两条投递同时取名」不可能拿到同一个名字；
//   - 候选名若**已被预留**，视为「占用」并进位 —— 这正是把「同瞬尚未落地的对等写入」
//     纳入判据的那一步（写前读去重看不见它，见 dropEventsAlreadyWritten 的边界）。
//
// 为什么不放进 publishMu（用户明令 ✗）：publishMu 是**全局**锁，且临界区内会取 m.mu；
// 而探测是 5s 级 VL 网络往返（projectionProbeTimeout），放进去会让所有源串行等一个源，
// 把「跨源并发」这一 FR-498 成果直接取消。放 m.mu 的代价是探测期间其它源也要排队取 m.mu
// （m.mu 本就是投递路径上已被接受的短临界区），但**不会**把网络往返串成全局队列：
// 每个源只在它自己那一次取名时占用 m.mu 一个探测周期，且探测有硬上界
// （projectionProbeTimeout、maxProjectionGenerationProbes=16）。
//
// owner 仅用于可读诊断（谁在写），不参与判据。
// 返回的预留句柄**必须**在写入尝试结束（成功或失败）后交给 releaseProjectionGeneration；
// 返回 nil 表示本次没拿到预留（探测失败/预算用尽/无 VL 客户端）——此时**不得**释放任何东西，
// 且名字退化为既有的「只进位一次」语义（调用方仍会写入，重名由写后逐字段校验兜住）。
func (m *Manager) reserveProjectionGeneration(source SourceConfig, current string, owner string, contexts ...context.Context) (string, *generationReservation) {
	if m == nil {
		return nextProjectionGeneration(current), nil
	}
	client, _, err := m.clientForSource(source)
	if err != nil {
		// VL 客户端不可用：退回既有行为（只进位一次、不做占用探测）。此时**无法预留**
		// ——探测本身就不存在，占用判据只由 VL 决定（既有语义，不因本修复收紧或放宽）。
		return nextProjectionGeneration(current), nil
	}
	// 令牌在**本次调用开始**分配一次，并在真正登记时写入表中 ⇒ 「登记与释放严格配对」。
	token := m.generationReservationSeq.Add(1)
	generation, ok := m.reserveProjectionGenerationWithClient(client, source, current, owner, token, contexts...)
	if !ok {
		return generation, nil
	}
	return generation, &generationReservation{
		key:        source.LogSourceID + "/" + source.SourceGeneration,
		generation: generation,
		token:      token,
		owner:      owner,
	}
}

// generationReservation 是一次代次名预留的句柄（所有权凭据）。
//
// 为什么要显式句柄而不是 (key, generation) 两个字符串：释放必须能回答「这次释放是不是我
// 自己的那次预留」。只用名字的话，一条迟到的旧调用会把**接替者**的预留清掉，
// 于是「同事在写这个名字」这个事实丢失 ⇒ 撞名复现。token 就是这个问题的答案。
type generationReservation struct {
	key        string
	generation string
	token      uint64
	owner      string
}

// reserveProjectionGenerationWithClient 是 reserveProjectionGeneration 的实现
// （显式收客户端，便于按天路由与用例注入）。
//
// 返回值语义（**必须**严格区分，写错任一方向都有害）：
//   - ok=true  ⇒ 本调用**持有**该名字的预留，调用方必须在写入尝试结束后释放它；
//     漏释放会把名字钉死到进程结束（该源此后每次取名都无谓进位）✗。
//   - ok=false ⇒ 本调用**没有**拿到预留（探测失败 / 预算用尽 / 无 VL 客户端）⇒
//     调用方**不得**释放——那个名字不属于它，释放会把别人的预留清掉 ✗。
func (m *Manager) reserveProjectionGenerationWithClient(client *vlsup.Client, source SourceConfig, current string, owner string, token uint64, contexts ...context.Context) (string, bool) {
	return m.probeProjectionGenerationWithClient(client, source, current, true, owner, token, contexts...)
}

// probeProjectionGenerationWithClient 是选名的**唯一实现**（probe-only 与 reserve 共用）。
//
// reserve=false：只回答「下一个未占用的名字」，不登记预留、也不因别人已预留而进位
// （即完全等于本修复前的语义）—— 供测试/只读诊断的 nextFreeProjectionGenerationWithClient 使用。
// reserve=true ：把「探测 → 预留」做成原子选择，并参考预留表避让（生产写入路径）。
func (m *Manager) probeProjectionGenerationWithClient(client *vlsup.Client, source SourceConfig, current string, reserve bool, owner string, token uint64, contexts ...context.Context) (string, bool) {
	parent := m.lifecycleContext()
	if len(contexts) > 0 && contexts[0] != nil {
		parent = contexts[0]
	}
	key := source.LogSourceID + "/" + source.SourceGeneration
	if m == nil {
		return nextProjectionGeneration(current), false
	}
	candidate := nextProjectionGeneration(current)
	first := candidate
	step := 1
	for probe := 0; probe < maxProjectionGenerationProbes; probe++ {
		// 判据三合一（顺序固定）：① 本进程已预留（仅 reserve 语义下参与）；② 本进程已实探
		// 未占用（缓存）；③ 实探 VL。①② 都在 m.mu 内完成，故「同源两条投递同时取名」
		// 不可能拿到同一个名字 ✓。
		reserved, free := m.tryReserveKnownGeneration(key, candidate, owner, token, reserve)
		if reserved {
			// 该名字已被同源的另一条面预占（尚未落地）⇒ 按占用处理并进位。
			// 这正是修复点：写前读去重看不见「同瞬尚未落地」的对等写入（见
			// dropEventsAlreadyWritten 的边界），而预留表看得见。
			advanced, ok := bumpProjectionGeneration(candidate, step)
			if !ok {
				return candidate, false
			}
			slog.Info("投影代次已被本进程另一条投递预留，进位避让",
				"source", source.LogSourceID, "generation", candidate, "from", current)
			candidate, step = advanced, step*2
			continue
		}
		if free {
			if candidate != first {
				slog.Info("投影代次与历史投影重名，已进位到新代次",
					"source", source.LogSourceID, "generation", candidate, "from", current)
			}
			return candidate, true
		}
		if parent.Err() != nil {
			return candidate, false
		}
		ctx, cancel := context.WithTimeout(parent, projectionProbeTimeout)
		used, err := m.projectionGenerationUsed(ctx, client, source, candidate)
		cancel()
		if err != nil {
			slog.Warn("投影代次占用探测失败，按未占用继续", "source", source.LogSourceID, "generation", candidate, "error", err)
			// 探测失败也要**尽量占用**（CAS 失败即进位）：否则 N 条并发在这样的探测失败下
			// 会全部回落到同一个名字——实测正是本缺陷的形态（本地夹具 VL 下 8/8 撞名）。
			if !reserve {
				return candidate, false
			}
			if m.claimGenerationLocked(key, candidate, owner, token) {
				return candidate, true
			}
			advanced, ok := bumpProjectionGeneration(candidate, step)
			if !ok {
				return candidate, false
			}
			slog.Info("投影代次已被本进程另一条投递预留，进位避让（探测失败路径）",
				"source", source.LogSourceID, "generation", candidate, "from", current)
			candidate, step = advanced, step*2
			continue
		}
		if !used {
			// 实探确认未占用 → 记入缓存（复审 P2-11：同一 (源, 代次) 不重复探测）。
			m.rememberProjectionGenerationFree(key, candidate)
			if !reserve {
				if candidate != first {
					slog.Info("投影代次与历史投影重名，已进位到新代次",
						"source", source.LogSourceID, "generation", candidate, "from", current)
				}
				return candidate, false
			}
			// **占用必须是 CAS**（本缺陷修复的关键一步）：探测在 m.mu **之外**完成
			//（故意如此，否则 5s 级 VL 往返会串住所有源），因此「探测到未占用」与
			//「占住这个名字」之间必然存在窗口。若这里直接无条件写入预留表，
			// 两条并发投递会双双实探「未占用」并双双占住**同一个名字** —— 撞名照旧。
			// claimGenerationLocked 在 m.mu 内做「查了再写」，故后到者失败 ⇒ 进位重选。
			if m.claimGenerationLocked(key, candidate, owner, token) {
				if candidate != first {
					slog.Info("投影代次与历史投影重名，已进位到新代次",
						"source", source.LogSourceID, "generation", candidate, "from", current)
				}
				return candidate, true
			}
			advanced, ok := bumpProjectionGeneration(candidate, step)
			if !ok {
				return candidate, false
			}
			slog.Info("投影代次已被本进程另一条投递预留，进位避让",
				"source", source.LogSourceID, "generation", candidate, "from", current)
			candidate, step = advanced, step*2
			continue
		}
		advanced, ok := bumpProjectionGeneration(candidate, step)
		if !ok {
			// 名字不是 `projection-<n>` 形态：不猜测语义，保持既有行为。
			break
		}
		candidate, step = advanced, step*2
	}
	// 探测预算用尽：沿用既有行为（返回当前候选，不打预留标记）——调用方仍会写入，
	// 若发生重名由既有的写后逐字段校验兜住（判据不放宽也不新增）。
	return candidate, false
}

// tryReserveKnownGeneration 处理两个**无需网络**的判据，并在缓存命中且 reserve=true 时完成预留。
//
// 返回 (alreadyReserved, newlyReserved)：
//   - alreadyReserved=true ⇒ 该名字已被本进程预留（另一条同源投递持有）⇒ 调用方必须进位。
//     reserve=false 时**恒为 false**：只读诊断不参与预留判据（否则它会把同事的预留当成占用，
//     并让「同一状态重复询问得到同一结论」的既有契约失效）。
//   - newlyReserved=true   ⇒ 缓存已确认 VL 未占用，本次**当场预留成功** ⇒ 直接用它。
//
// 两者共用一次持锁，避免「查预留 / 查缓存 / 写预留」三次取锁之间的窗口。
func (m *Manager) tryReserveKnownGeneration(key, candidate, owner string, token uint64, reserve bool) (bool, bool) {
	if candidate == "" {
		return false, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if reserve {
		if _, busy := m.generationReserved[key][candidate]; busy {
			return true, false
		}
	}
	probed := m.generationProbeCache[key]
	if probed == nil || !probed[candidate] {
		return false, false
	}
	if !reserve {
		// 只读诊断：缓存已确认未占用 ⇒ 直接回答（不登记预留）。
		return false, true
	}
	if !m.reserveGenerationLockedNoLock(key, candidate, owner, token) {
		// 上限拒绝：**不得**报「预留成功」——否则调用方以为已占住该名，而表里没有登记，
		// 另一条并发投递可占住同一个名字（撞名）。按「已被预留」交给调用方进位重选。
		return true, false
	}
	return false, true
}

// claimGenerationLocked 以 **CAS** 语义占用一个代次名：只有当前**未**被预留时才写入，返回是否成功。
//
// 为什么必须是 CAS 而不是无条件写（本缺陷修复的关键一步）：探测（projectionGenerationUsed）在
// m.mu **之外**完成——这是刻意的，否则 5s 级 VL 往返会串住所有源（见 reserveProjectionGeneration
// 的锁选型说明）。于是「探测到未占用」与「占住这个名字」之间必然存在窗口：两条并发投递会双双
// 实探「未占用」，若无条件写入预留表，它们会双双占住**同一个名字** —— 撞名照旧。
// 本方法在 m.mu 内做「查了再写」，后到者返回 false ⇒ 调用方进位重选。
func (m *Manager) claimGenerationLocked(key, generation, owner string, token uint64) bool {
	if generation == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, busy := m.generationReserved[key][generation]; busy {
		return false
	}
	// 登记可能因上限被拒（返回 false）⇒ 同样按「占用」处理，由调用方进位重选。
	// **不得**在此忽略返回值：那会让调用方以为已占住该名，而表里其实没有 —— 于是另一条
	// 并发投递可以占住同一个名字（撞名，正是本机制要消灭的形态）。
	return m.reserveGenerationLockedNoLock(key, generation, owner, token)
}

// reserveGenerationLockedNoLock 是预留登记的实现（调用方须持 m.mu）。
//
// 有界性：每源同时最多 maxProjectionReservedPerSource 条。越界时**拒绝登记**（返回 false，
// 调用方按「已被预留」进位重选），**绝不整组清空**。
//
// **为什么不能整组清空（2026-10-12 复审订正）**：本函数原先在越界时把整张表换成空 map，
// 注释称其为「退化为下次重新实探，保守方向」——**该论证是错的**。表里除本次候选之外，
// 还有**其它在飞写入者尚未释放**的预留：清空后它们的 release 按 token 找不到条目（静默无效），
// 而**下一条同源投递会看到该名「未预留」**；实探 VL 又必然报「未占用」（同伴还没落盘），
// 于是 claimGenerationLocked 成功 ⇒ **两条面拿到同一个名字各写一份**——正是本机制要消灭的撞名。
// 实探对「同瞬尚未落地」天然失明（见 probeProjectionGenerationWithClient 的说明），
// 所以「退化为重新实探」并不保守，而是退回缺陷本身。
// 拒绝登记则相反：上限只影响**本次取名**（退化为进位重选，下一轮必成功），
// 既有在飞预留的语义完好——这才是真正的保守方向。
// 上限本身只是异常路径的内存兜底（正常同时至多一两条），故拒绝不会造成实际饥饿。
func (m *Manager) reserveGenerationLockedNoLock(key, generation, owner string, token uint64) bool {
	if generation == "" {
		return false
	}
	if m.generationReserved == nil {
		m.generationReserved = map[string]map[string]uint64{}
	}
	reserved := m.generationReserved[key]
	if reserved == nil {
		reserved = map[string]uint64{}
		m.generationReserved[key] = reserved
	}
	if len(reserved) >= maxProjectionReservedPerSource {
		// 已到上限：拒绝本次登记，由调用方进位重选。**不得清空**（见上方说明）。
		return false
	}
	reserved[generation] = token
	_ = owner // owner 仅供调用方日志使用；表内只存所有权令牌（见 generationReservation）
	return true
}

// releaseProjectionGeneration 释放一次预留（写入尝试结束——**成功或失败都必须释放**）。
//
// 为什么失败也要释放：预留的唯一目的是「同源并发的第二次取名不撞名」——它是**在飞写入者
// 的登记**，不是「这个名字已被永久用掉」。任务结束后若继续占着该名，下一次重试只能一路
// 进位，代次号会随失败次数无界增长，而这些号在 VL 里从未出现，白白拉大探测窗口。
// 释放**不等于**「该名可复用」：本方法同时把该名的「未占用」缓存删掉，于是下一次取名会
// **重新实探 VL**（真实写入失败可能已留下同名行）——这正是既有防线
// （见 recordProjectionGenerationAttempt 的注释 ✓）。故本方法只让「名字被再分配给谁」
// 回到实探判据上，**不削弱任何重名防护**。
//
// 所有权：res.token 必须与表中登记的一致才删除。这一步防的是「A 释放了 B 的预留」——
// 没有它，一条迟到的旧调用就能把正在写入的同伴的预留清掉，撞名随即复现。
func (m *Manager) releaseProjectionGeneration(res *generationReservation) {
	if m == nil || res == nil || res.generation == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if reserved := m.generationReserved[res.key]; reserved != nil {
		// token 必须核对：表中若是别人的登记（或已被别人接替），本次释放无效。
		if cur, ok := reserved[res.generation]; ok && cur == res.token {
			delete(reserved, res.generation)
		}
		if len(reserved) == 0 {
			delete(m.generationReserved, res.key)
		}
	}
	// 缓存一并失效：本进程已把该名交给 VL 写过（或刚取到名字但未写入），
	// 「VL 未占用」在两种情形下都不再是可靠结论。
	if probed := m.generationProbeCache[res.key]; probed != nil {
		delete(probed, res.generation)
	}
}

// maxProjectionReservedPerSource 是单源「已预留代次名」的上限。
//
// 正常路径下每源同时至多一两条（写入结束即释放）；上限只防异常路径（大量在飞写入、
// 释放被漏掉）把内存拖大。越界即整组重建，退化为「重新实探」——保守方向。
const maxProjectionReservedPerSource = 64

// generationReservableForTest 报告「该 (源, 代次) 当前**未**被本进程预留」。
//
// 仅供回归断言使用：预留表是 unexported 状态，而「别人的释放不得误清我的预留」这条红线
// 必须能被直接断言（否则只能从"撞名是否复现"间接推断，噪声大且不自证）。
// 它不参与任何生产判据，也不改变任何状态。
func (m *Manager) generationReservableForTest(key, generation string) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, busy := m.generationReserved[key][generation]
	return !busy
}

// rememberProjectionGenerationFree 记录「该 (源, 代次) 已实探确认未占用」。
//
// 缓存按源有界（maxGenerationProbeCachePerSource）：条目在写入时失效，正常路径下每源同时
// 至多一两条；上限只是防止异常路径（同一状态被反复推进）把内存拖大。
func (m *Manager) rememberProjectionGenerationFree(key, generation string) {
	if generation == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.generationProbeCache == nil {
		m.generationProbeCache = map[string]map[string]bool{}
	}
	probed := m.generationProbeCache[key]
	if probed == nil || len(probed) >= maxGenerationProbeCachePerSource {
		probed = map[string]bool{}
		m.generationProbeCache[key] = probed
	}
	probed[generation] = true
}

// recordProjectionGenerationAttempt 使该 (源, 代次) 的「未占用」缓存失效。
//
// 时机是**写入尝试之前**：写入可能只完成一部分就失败，此后 VL 里可能已有该名字的行，
// 缓存必须立刻失效，否则重试会跳过探测、直接往同名旧行上写（同名冲突正是停摆的成因）。
func (m *Manager) recordProjectionGenerationAttempt(key, generation string) {
	if generation == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	probed := m.generationProbeCache[key]
	if probed == nil {
		return
	}
	delete(probed, generation)
}

// maxGenerationProbeCachePerSource 是单源「已确认未占用」代次名的缓存上限。
//
// 正常路径下每源的条目在写入时即失效（同时至多一两条），上限只防异常路径（同一状态被反复
// 推进）把内存拖大；越界即整组重建，退化为「重新实探」——保守方向。
const maxGenerationProbeCachePerSource = 64

// projectionGenerationUsed 报告 VL 中该源是否已有该物理代次的行（limit 1，命中即返回）。
func (m *Manager) projectionGenerationUsed(ctx context.Context, client *vlsup.Client, source SourceConfig, generation string) (bool, error) {
	if client == nil {
		return false, nil
	}
	// 选择器与写入/校验同口径（源 + 来源代次），窗口显式取**近期**（见 projectionProbeSpan）：
	// 同一名字的旧行会让「整窗重发」的校验失败，故占用判定要覆盖重放可能触达的近期数据。
	//
	// 2026-10-08 修复：此前不带 start/end，VL 以默认全时段（[1677-09-21, 2262-04-11]）
	// 处理并被 `-search.maxQueryTimeRange` 以 400 拒绝 → 「按未占用继续」→ 重名
	// 防护静默失效（生产日志可查）。显式有界后探测恢复有效，且不再依赖 VL 默认值。
	// sort 只为让「确已占用」走最新分区早停（实测 0.01s 级）；「未占用」需扫全窗口。
	selector := "projection_generation:=" + strconv.Quote(generation) +
		" AND log_source_id:=" + strconv.Quote(source.LogSourceID) +
		" AND source_generation:=" + strconv.Quote(source.SourceGeneration)
	now := time.Now().UTC()
	// 窗口两端一起受 projectionProbeSpan（总跨度上限）约束：
	//   end   = now + slack（覆盖可落在未来的无日期行）
	//   start = end - span（由 end **反推**，故总跨度恒 ≤ span，与 slack 取值无关）
	// 由 end 反推而非用 now-span 是刻意的：后者会在 slack>0 时把总跨度撑到 span+slack
	// （实测踩过：31d+1d=32d > VL 上限 ⇒ 探测被 400 拒绝 ⇒ 重名防护整条失效）。
	probeEnd := now.Add(projectionProbeFutureSlack)
	probeStart := probeEnd.Add(-m.projectionProbeWindow())
	params := url.Values{
		"query": {selector + " | fields _time | sort by (_time desc) | limit 1"},
		"start": {probeStart.Format(time.RFC3339Nano)},
		"end":   {probeEnd.Format(time.RFC3339Nano)},
	}
	// Q3（2026-10-03 现场 1364/min）：**代次占用探测也是投影查询** ✗ —— 它发生在每次取新代次时
	// （重放的每一天、每个源各一次），此前**完全没走天花板闸** ✓。现在并入同一 limiter：
	// 与校验查询共享速率/在飞上限（天花板语义 ⇒ 正常路径零等待 ✓）。
	if m.verifyLimiter != nil {
		if err := m.verifyLimiter.Acquire(ctx); err != nil {
			return false, err
		}
		defer m.verifyLimiter.Release()
	}
	used := false
	err := client.Stream(ctx, "/select/logsql/query", params, func(body io.Reader) error {
		scanner := bufio.NewScanner(io.LimitReader(body, 1<<20))
		scanner.Buffer(make([]byte, 64*1024), 1<<20)
		for scanner.Scan() {
			if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
				continue
			}
			used = true
			return nil
		}
		return scanner.Err()
	})
	if err != nil {
		return false, err
	}
	return used, nil
}

// bumpProjectionGeneration 把 `projection-<n>` 形态的代次名按 delta 进位；格式不符时返回 false
// （调用方保持既有行为，不再探测）。
func bumpProjectionGeneration(current string, delta int) (string, bool) {
	var n int
	if _, err := fmt.Sscanf(current, "projection-%d", &n); err != nil || n <= 0 {
		return "", false
	}
	return fmt.Sprintf("projection-%d", n+delta), true
}

// recoveryReceipt 是**一次投递轮内**「权威事件集合」的复用凭据。
//
// 为什么需要（缺陷：pending 轮内的两次全量重建）：`deliver` 在 pending 且非重放时会先调
// `tryConfirmPendingPublication` 取证一次（本函数），未获确认 ⇒ `replace` 仍为真 ⇒ 同一轮
// 接着又为写入面再重建一次（见 deliver 内 expected 的取法）。生产单源 70 万行实测一次约 6 秒，
// 于是 pending 期间每轮白付一倍。
//
// 为什么**只做一轮内复用**、不做跨轮结果缓存（刻意不做，理由是正确性与内存两条硬约束）：
//  1. 正确性凭据只能是「输入未变」。段侧可用「行数 + 段物理版本号」判定（与 eventStoreCoverage
//     同一套语义：段只追加，除末段截断外无改写路径，二者任一变化都会使读数变化）；但**WAL 侧
//     没有等价的廉价读数**——条目的 Durable 标记会就地翻转，同一长度下内容可能完全不同，
//     按长度/最后 ID 做键会返回旧数据 ✗。故跨轮复用必须对 WAL 全量做内容指纹，代价与本函数
//     本身同阶，收益被吃掉。
//  2. 跨轮缓存必须把整份 canonical 集合（生产单源 70 万条事件体，数百 MB）常驻内存 ✗✗ ——
//     这正是 FR-484 用追加式磁盘段消除的形态（真机验收：150 源稳态 RSS 20–22MiB / 判据 ≤300MiB）；
//     单源缓存就能把它顶穿。而"命中即复制一份给调用方"还要再翻一倍峰值。
//
// 因此本凭据只活在一次 `deliver` 调用的栈上（跨轮不外泄 ⇒ 不可能陈旧），守卫是「段行数 +
// 段物理版本号 + WAL/内联长度 + key」：段没变、WAL 又是同一个 saved 值拷贝 ⇒ 结果逐字节相同。
type recoveryReceipt struct {
	key         string
	rows        int
	epoch       uint64
	walLen      int
	inline      int
	inputDigest [sha256.Size]byte
	events      []logtypes.Event
	// missingDays 是本次发布确认取证**实测出的缺失天**（仅当 verdict 为
	// confirmMissingMeasured 时有效）。用于让 pending 重试把写入面收敛到这些天，
	// 而不是整窗重写全源（那是 VL 体积放大到 75× 的直接原因，见 writeEvents 处的注释）。
	missingDays []string
}

// normalizeCanonicalEvent 把一条事件补成"规范形态"：保证 EventTimeUTC 非空，且
// CanonicalHash 与它那些字段（EventTimeUTC/Level/Stream/Message）**互相自洽**。
//
// ## 为什么必须是幂等的（2026-11-02 修复）
//
// 同一物理行会在两个地方出现：
//   - **WAL 副本**：采集时存的是 normalize 的产物，**EventTimeUTC 可能为空**（无时间戳行）；
//   - **段副本**：投递时存的是**已归一化**形态（非空 EventTimeUTC + 重算过的 hash）。
//
// 旧实现只在 EventTimeUTC 为空时重算 hash ⇒ 同一行读两次、算两次会得到**两个不同**的
// CanonicalHash（"读回再写"不幂等）。它今天不炸只因为它同时**也不重算**已非空的那份，
// 而两条副本总是被分别当作不同的历史事实记下。
//
// 这道修复把它抬成一条**真正的幂等函数**：
//   - 已归一化形态（EventTimeUTC 非空且 hash 自洽）⇒ 原样返回，零开销（绝大多数情况）；
//   - **旧段形态**（EventTimeUTC 非空但 hash 不自洽，例如段里存的是未归一化的原始
//     hash）⇒ 以 EventTimeUTC 为准**重算** hash；
//   - 无时间戳行（EventTimeUTC 为空）⇒ 仍靠 IngestTimeUTC 反推（旧段兼容的唯一途径，保留）。
//
// 于是 f(f(x)) == f(x) 对所有输入成立，而**已发布数据的语义一字未变**：
// 对任何 hash 本来就自洽的事件（即全部正常路径产出的事件），本函数重算出的值逐字相同。
func normalizeCanonicalEvent(event logtypes.Event) (logtypes.Event, error) {
	if event.EventTimeUTC == "" {
		when, err := time.Parse(time.RFC3339Nano, event.IngestTimeUTC)
		if err != nil {
			return event, fmt.Errorf("ingest: event %s has no stable event or ingest time: %w", event.EventID, err)
		}
		event.EventTimeUTC = when.UTC().Format(time.RFC3339Nano)
		event.CanonicalHash = logtypes.CanonicalContentHash(event.EventTimeUTC, event.Level, event.Stream, event.Message)
		return event, nil
	}
	// 已归一化形态：hash 必须与字段自洽。不自洽就**重算一次**（这正是幂等的落点）。
	// 正常路径下这里不需要重算（构造时就是按同一组字段算的）⇒ 一次字符串比较的开销。
	if self := logtypes.CanonicalContentHash(event.EventTimeUTC, event.Level, event.Stream, event.Message); self != event.CanonicalHash {
		event.CanonicalHash = self
	}
	return event, nil
}

func (m *Manager) publish(source SourceConfig, projectionGeneration string, events []logtypes.Event, replace bool, observed ...*catalog.Record) error {
	var expected *catalog.Record
	if len(observed) > 0 {
		expected = observed[0]
	}
	return m.publishSnapshot(source, projectionGeneration, events, replace, expected, nil, len(observed) > 0)
}

func (m *Manager) publishSnapshot(source SourceConfig, projectionGeneration string, events []logtypes.Event, replace bool, observed *catalog.Record, snapshot *publicationSnapshot, checkObserved bool, permits ...*catalog.WritePermit) error {
	key := catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay}
	// 本次发布面的最大 record_end（封闭可见水位）＝ 传入 events 的最大 record_end。
	// 语义边界：它只度量**本次实际写入 VL 的内容**——快路径下 events 是本批尾部，
	// 慢路径下是权威全量；不得并入「已落 WAL 尚未投递」的 Durable 水位，否则会把
	// 尚未可见的位置报成已可见。历史已有可见水位由下方 scope.ClosedVisibleSeq 取大承接。
	batchMax := uint64(0)
	for _, event := range events {
		if event.Record.End > batchMax {
			batchMax = event.Record.End
		}
	}
	// 封闭水位只来自本次校验面，实时 Durable 或事件段水位不构成本次发布证据。
	maxEnd := batchMax
	if maxEnd == 0 {
		// `record_end` 全为 0 ⇒ 本次发布面不推进白名单（保持既有「空发布面」语义）。
		//
		// **但不能静默**（2026-10-21 复审）：调用方会把 nil 当成功，而白名单没动
		// ⇒ 后续按 `record_end <= ClosedVisibleSeq` 过滤时该源整段不可见。
		// 这里留一条 WARN + 明确措辞，让「发布面为空」在排障时可见；
		// 不改成 error 是因为零宽度/构造夹具确实是合法输入（既有用例依赖该语义）。
		slog.Warn("本次发布面的 record_end 全为 0：白名单不会推进（该源该日可能仍不可见）",
			"source", source.LogSourceID, "generation", projectionGeneration, "utcDay", source.UTCDay,
			"events", len(events), "replace", replace)
		return nil
	}
	// 串行化「读当前 → 应用本源投影 → CAS 写回」整段：共享 namespace 的多个源在并发采集轮下
	// 会互相覆盖（见 publishMu 的说明）。
	m.publishMu.Lock()
	defer m.publishMu.Unlock()
	rec, ok := m.cat.Get(key)
	expected := rec.Clone()
	if !m.publicationSnapshotCurrentLocked(source, snapshot, []string{source.UTCDay}) {
		return errPublicationStale
	}
	if checkObserved {
		// 保留「校验期间权威变更」的拒绝语义：owner / 代数 / 写路由 / 恢复要求被换掉时，
		// 不得把本次投影发布到新权威上（TestOwnerChangeDuringVerificationCannotPublishToNewOwner）。
		//
		// 为什么不再比较投影清单（manifest）与日志序号：并发轮里其他源发布自己的投影是**正常**
		// 变更，若按原口径视为冲突，同一 namespace 的多源就会每轮互相打断（本轮实测：两个 stdio
		// 源只剩 1 条发布）。authority 之外的并发变更一律以「锁内读到的当前记录」为基础合并。
		if !sameCatalogAuthority(observed, rec) {
			return fmt.Errorf("%w: Catalog authority changed during projection verification", catalog.ErrInvalidState)
		}
		if sourcePublicationToken(observed, source) != sourcePublicationToken(rec, source) {
			return errPublicationStale
		}
	}
	var earlierParent *writtenAttempt
	if snapshot != nil {
		earlierParent = snapshot.earlierParent
	}
	if earlierParent != nil {
		m.mu.Lock()
		valid := m.writtenAttempts[source.LogSourceID+"/"+source.SourceGeneration] == earlierParent && m.pipes[source.LogSourceID+"/"+source.SourceGeneration] == earlierParent.pipe && earlierParent.earlier != nil && earlierParent.earlier.generation == projectionGeneration && earlierParent.sourceToken == sourcePublicationToken(observed, source)
		m.mu.Unlock()
		if !valid {
			return errPublicationStale
		}
		if err := writtenProofContextErr(snapshot.ctx); err != nil {
			return err
		}
	}
	if !ok {
		var target catalog.WriteTarget
		if len(permits) > 0 && permits[0] != nil {
			permit := permits[0]
			target = catalog.WriteTarget{Owner: permit.Owner, DirID: permit.DirID, Generation: permit.Generation}
		} else {
			var admissionErr error
			target, admissionErr = m.cat.WriteAdmission(key)
			if admissionErr != nil {
				return admissionErr
			}
		}
		dirID := target.DirID
		if dirID == "" {
			dirID = string(target.Owner) + "-" + projectionGeneration
		}
		rec = catalog.NewStableRecord(key, target.Owner, target.Generation, dirID)
	}
	if rec.PublishedProjection == nil {
		rec.PublishedProjection = &catalog.PublishedProjection{}
	}
	proj := rec.PublishedProjection
	previousGenerations := publishedProjectionGenerations(proj)
	proj.ConflictCount = m.identityConflictCount(source.StorageNamespace, source.LogSourceID+"/"+source.SourceGeneration)
	if len(proj.SourceProjections) == 0 {
		for _, ref := range proj.CoveredSourceGenerations {
			closed := proj.ClosedVisibleSeq[ref.LogSourceID+"/"+ref.SourceGeneration]
			if closed == 0 {
				closed = proj.ClosedVisibleSeq["default"]
			}
			proj.SourceProjections = append(proj.SourceProjections, catalog.SourceProjection{
				SourceGenerationRef: ref, ProjectionGenerations: append([]string(nil), previousGenerations...), ClosedVisibleSeq: closed})
		}
	}
	ref := catalog.SourceGenerationRef{LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration}
	index := -1
	for i := range proj.SourceProjections {
		if proj.SourceProjections[i].SourceGenerationRef == ref {
			index = i
			break
		}
	}
	if index < 0 {
		index = len(proj.SourceProjections)
		scope := catalog.SourceProjection{SourceGenerationRef: ref}
		// Older single-source records may omit their redundant source reference.
		if len(proj.SourceProjections) == 0 && len(proj.CoveredSourceGenerations) == 0 && !replace {
			scope.ProjectionGenerations = previousGenerations
		}
		proj.SourceProjections = append(proj.SourceProjections, scope)
	}
	scope := &proj.SourceProjections[index]
	if replace {
		scope.ProjectionGenerations = []string{projectionGeneration}
	} else {
		scope.ProjectionGenerations = appendUniqueGeneration(scope.ProjectionGenerations, projectionGeneration)
	}
	// **封闭可见水位必须无条件单调**（2026-10-21 复审发现的静默丢数据缺陷）：
	// 旧实现在 `replace` 时**跳过**了单调取大，直接写 `maxEnd = 本次 events 的最大 record_end`。
	// 一旦权威集合因段丢失/重建而**收缩**（崩溃后 `canonicalRecoveryEvents` 变短），
	// 新 maxEnd 会小于历史值 ⇒ 读侧按 `record_end <= ClosedVisibleSeq` 过滤时，
	// **已可见的行被判为不可见而消失**（数据仍在 VL，只是查不到）。
	//
	// 为什么不能假设「replace 一定覆盖得更全」：replace 只保证「用新代次取代旧白名单」，
	// 不保证「新代的 record_end 更大」——段收缩、按天切片、部分失败都会让它更小。
	// 若真的需要水位回退（权威确实收缩），必须是一次显式、带告警的降级动作，
	// 而不是这里静默赋值。
	if scope.ClosedVisibleSeq > maxEnd {
		slog.Warn("封闭可见水位未随本次发布前进（权威集合可能收缩），保持单调以免已可见数据消失",
			"source", source.LogSourceID, "generation", projectionGeneration, "utcDay", source.UTCDay,
			"previousClosed", scope.ClosedVisibleSeq, "currentMaxEnd", maxEnd, "replace", replace)
		maxEnd = scope.ClosedVisibleSeq
	}
	scope.ClosedVisibleSeq = maxEnd
	sort.Slice(proj.SourceProjections, func(i, j int) bool {
		a, b := proj.SourceProjections[i], proj.SourceProjections[j]
		if a.LogSourceID != b.LogSourceID {
			return a.LogSourceID < b.LogSourceID
		}
		return a.SourceGeneration < b.SourceGeneration
	})
	proj.ProjectionGeneration = projectionGeneration
	proj.ProjectionGenerations = nil
	proj.CoveredSourceGenerations = nil
	proj.ClosedVisibleSeq = make(map[string]uint64)
	var maxClosed uint64
	for _, sourceProjection := range proj.SourceProjections {
		proj.CoveredSourceGenerations = append(proj.CoveredSourceGenerations, sourceProjection.SourceGenerationRef)
		for _, generation := range sourceProjection.ProjectionGenerations {
			proj.ProjectionGenerations = appendUniqueGeneration(proj.ProjectionGenerations, generation)
		}
		proj.ClosedVisibleSeq[sourceProjection.LogSourceID+"/"+sourceProjection.SourceGeneration] = sourceProjection.ClosedVisibleSeq
		if sourceProjection.ClosedVisibleSeq > maxClosed {
			maxClosed = sourceProjection.ClosedVisibleSeq
		}
	}
	proj.ClosedVisibleSeq[key.String()], proj.ClosedVisibleSeq["default"] = maxClosed, maxClosed
	manifestData, err := json.Marshal(proj.SourceProjections)
	if err != nil {
		return err
	}
	proj.ManifestVersion = fmt.Sprintf("manifest-%x", sha256.Sum256(manifestData))
	proj.CoverageComplete = true
	proj.QueryLocationDirID = rec.OwnerDirID
	proj.QueryGeneration = rec.Generation
	rec.PublishedProjection = proj
	if err := m.cat.PublishProjection(expected, rec); err != nil {
		return err
	}
	if earlierParent != nil {
		// 只消费本次真实前置发布的回执来更新父/子source token；不重取原cut、不借外部发布洗白。
		m.mu.Lock()
		sourceKey := source.LogSourceID + "/" + source.SourceGeneration
		valid := m.writtenAttempts[sourceKey] == earlierParent && m.pipes[sourceKey] == earlierParent.pipe && earlierParent.earlier != nil && earlierParent.earlier.generation == projectionGeneration
		if valid {
			token := sourcePublicationToken(rec, source)
			earlierParent.sourceToken = token
			earlierParent.earlier.sourceToken = token
		}
		m.mu.Unlock()
		if !valid {
			return errPublicationStale
		}
	}
	if m.publicationEpochs == nil {
		m.publicationEpochs = map[string]map[string]uint64{}
	}
	sourceKey := source.LogSourceID + "/" + source.SourceGeneration
	if m.publicationEpochs[sourceKey] == nil {
		m.publicationEpochs[sourceKey] = map[string]uint64{}
	}
	m.publicationEpochs[sourceKey][source.UTCDay]++
	return nil
}

// 只抽取真实源投影，不把共享命名空间内其他源的 manifest 变化纳入本源 CAS。
func sourcePublicationToken(rec *catalog.Record, source SourceConfig) string {
	if rec == nil || rec.PublishedProjection == nil {
		return ""
	}
	proj := rec.PublishedProjection
	for _, scope := range proj.SourceProjections {
		if scope.LogSourceID == source.LogSourceID && scope.SourceGeneration == source.SourceGeneration {
			data, _ := json.Marshal(scope)
			return string(data)
		}
	}
	if len(proj.SourceProjections) > 0 {
		return ""
	}
	generations := publishedSourceProjectionGenerations(proj, source)
	if len(generations) == 0 {
		return ""
	}
	data, _ := json.Marshal(struct {
		Generations []string
		Closed      uint64
	}{generations, proj.ClosedVisibleSeq[source.LogSourceID+"/"+source.SourceGeneration]})
	return string(data)
}

// sameCatalogAuthority 判定两份 Catalog 记录是否指向同一权威（owner / 代数 / 目录 / 写路由 /
// 恢复要求）。
//
// 为什么不比较投影清单（manifest）与 journal 序号：并发采集轮里**其他源**发布自己的投影会
// 推进这两者，但那是正常的多源合并，不是权威变更；若按原口径视为冲突，共享 namespace 的源
// 每轮互相打断（本轮实测：两个 stdio 源最终只剩 1 条投影）。两份都为 nil（记录尚不存在）时
// 视为同一权威（都被视为「无权威」）。
func sameCatalogAuthority(a, b *catalog.Record) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		// 一侧缺失：只有在另一侧是「本次发布可安全合并的初始 HOT 权威」时才算同一权威。
		// 为什么不能一律判否：并发轮里同一 namespace 的另一个源可能刚建立记录（HOT/第 1 代/
		// 未冻结/无需恢复），那是正常的多源首写，而不是权威切换；一律判否会让共享 namespace
		// 的源每轮互相打断（本轮实测：两个 stdio 源只剩 1 条投影）。
		other := a
		if other == nil {
			other = b
		}
		return isInitialHotAuthority(other)
	}
	return a.Owner == b.Owner && a.Generation == b.Generation && a.OwnerDirID == b.OwnerDirID &&
		a.WriteRoute == b.WriteRoute && a.RecoveryRequired == b.RecoveryRequired
}
