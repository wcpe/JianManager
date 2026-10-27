package ingest

import (
	"fmt"
	"sort"
	"strings"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// 本文件是从 runtime.go 拆出的**只读观测面**（阶段 1 架构整理，2026-10-26）。
//
// 拆分依据：这些方法只读取 Manager 的既有字段与原子计数器，不改变任何状态、
// 不参与采集/投递/持久化的状态机流转，因此可以与主运行时文件分离而不触碰锁语义。
//
// 拆分纪律（必须保持）：
//   - 同包拆文件，不改变任何导出 API、不改可见性、不改行为。
//   - 这些方法仍直接访问 m.mu 与 m.pipes 等私有字段——同包内合法，无需导出。
//   - 任何对 Manager 字段语义的修改仍必须回到 runtime.go 的 Manager 定义处。
func (m *Manager) ResumeDiagnostics() []SourceResumeDiagnostic {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	pipes := make([]*pipeline.Pipeline, 0, len(m.pipes))
	for _, p := range m.pipes {
		if p != nil {
			pipes = append(pipes, p)
		}
	}
	m.mu.Unlock()
	out := make([]SourceResumeDiagnostic, 0, len(pipes))
	for _, p := range pipes {
		wal := p.WAL()
		led := p.Ledger()
		if wal == nil || led == nil {
			continue
		}
		entry := led.Get(p.Key())
		if entry == nil {
			continue
		}
		d := SourceResumeDiagnostic{
			LogSourceID:        p.Key().LogSourceID,
			AcquirePaused:      entry.AcquirePaused,
			PauseReason:        entry.PauseReason,
			BacklogPause:       acquire.IsBacklogPauseReason(entry.PauseReason),
			UnresolvedByReason: map[string]int{},
		}
		d.ReadPos = entry.Positions.Read
		d.ReclaimPos = entry.Positions.Reclaim
		d.DeliveryPos = entry.Positions.Delivery
		d.DurablePos = entry.Positions.Durable
		for _, g := range entry.Gaps {
			if g.Resolved {
				continue
			}
			d.Unresolved++
			d.UnresolvedByReason[g.Reason]++
			classiable := gapReasonClassifiableOnResume(g.Reason)
			if !classiable {
				d.Blocking++
			}
			d.UnresolvedGaps = append(d.UnresolvedGaps, UnresolvedGapView{
				Start: g.StartPos, End: g.EndPos, Reason: g.Reason,
				Classiable: classiable, AboveDelivery: g.StartPos > entry.Positions.Delivery,
			})
		}
		d.BacklogEntries, d.MaxEntries = wal.BacklogAndLimits()
		d.BacklogTotalEntries, _ = wal.BacklogTotal()
		d.ReplayEntries, _ = wal.ReplayInFlight()
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LogSourceID < out[j].LogSourceID })
	return out
}

// UnresolvedGapBlockedSources 返回**被「不可分类缺口」永久阻塞**的源清单（2026-10-10）。
//
// 语义：某源存在未消解缺口，且这些缺口**全部落在恢复白名单之外**（`gapReasonClassifiableOnResume`
// 为假）⇒ 它永远不会被 `maybeResumeBacklogLocked` 的分类裁定放行，也不会被
// `ResumeAcquire` 放行 ⇒ **不重启、不人工介入就永久停在暂停态**。
//
// 为什么要这个标记（生产实测 2026-10-10）：现场 65 个源持有 215 条未决缺口（DELIVER_ERROR 148、
// WAL_BODY_MISSING 64、ROTATED_SEGMENT_NOT_READY 3）。其中 WAL_BODY_MISSING 是**真实正文缺失**
// （回读造不出已丢的字节 ⇒ 必须人工裁定 ✓），而 ROTATED_SEGMENT_NOT_READY **当时**不在任何白名单里
// ——它只是"轮转段尚未就绪"的时序记录（区间恒为 [0,0]），却让 86 个源永久停在暂停上 ✗✗
// （修法：它已入白名单，见 ledger.GapReasonsHealableOnReResume ✓）。两类的共同点是：
// 它们所属的源已经「积压归零」，而日志每轮只印一句「积压已回落但暂不能恢复采集」，
// **从外部看不出「它其实永远起不来」与「它只是还在等滞回」的区别**。本函数把这一区别变成显式读数。
//
// 只读、零行为改动：不主动暂停任何源（那是行为），只报告「已经被卡住且无出路」的源。
func (m *Manager) UnresolvedGapBlockedSources() []SourceResumeDiagnostic {
	if m == nil {
		return nil
	}
	diags := m.ResumeDiagnostics()
	out := make([]SourceResumeDiagnostic, 0, len(diags))
	for _, d := range diags {
		if d.Unresolved == 0 || d.Blocking == 0 {
			continue
		}
		// 只有「**全部**未决缺口都不可分类」才判永久阻塞：只要还有一条可分类缺口，
		// 分类裁定/重投仍有出路 ⇒ 不列入（避免把"正在自愈"误报成"永久卡死"）。
		if d.Blocking != d.Unresolved {
			continue
		}
		out = append(out, d)
	}
	return out
}

// GapBlockedDiagnosticLines 把「永久阻塞」清单折成可日志行（供分钟打点消费，见
// sampleResumeDiagnostics）。与 ResumeDiagnosticLines 同型：只打异常项，避免刷屏。
func (m *Manager) GapBlockedDiagnosticLines() []string {
	blocked := m.UnresolvedGapBlockedSources()
	lines := make([]string, 0, len(blocked))
	for _, d := range blocked {
		reasons := make([]string, 0, len(d.UnresolvedByReason))
		for reason, count := range d.UnresolvedByReason {
			reasons = append(reasons, fmt.Sprintf("%s=%d", reason, count))
		}
		sort.Strings(reasons)
		lines = append(lines, fmt.Sprintf(
			"source=%s paused=%v backlog=%d/%d unresolved=%d blocking=%d reasons=[%s]",
			d.LogSourceID, d.AcquirePaused, d.BacklogEntries, d.MaxEntries,
			d.Unresolved, d.Blocking, strings.Join(reasons, " ")))
	}
	sort.Strings(lines)
	return lines
}

// ResumeDiagnosticLines 把诊断面折成**只含异常项**的可日志行（暂停 / 有未消解缺口 / 有回放在飞 ✓）。
//
// 为什么必须"可达"（2026-10-05 用户点名的教训 ✗✓）：`ResumeDiagnostics` 此前只有定义与测试引用 ⇒
// **没有生产可达面** ✓ —— 正是我们一路在抓的"实现存在但不可达"，这次中招的是自己的新工具 ✗。
// 本函数供**每分钟打点**（与采集索引持久化耗时采样同一 sampler ✓）与 CLI 使用；只打非零项，
// 避免 60 源刷屏 ✓。`BacklogEntries>0` 属正常态，**不打**（噪声 ✗）。
func (m *Manager) ResumeDiagnosticLines() []string {
	m.readingsConsumedDiagnostics.Add(1) // 消费计数（见字段注释 ✓）
	diags := m.ResumeDiagnostics()
	lines := make([]string, 0, len(diags))
	for _, d := range diags {
		if !d.AcquirePaused && d.Unresolved == 0 && d.ReplayEntries == 0 {
			continue
		}
		lines = append(lines, fmt.Sprintf(
			"source=%s paused=%v reason=%q backlog_pause=%v backlog=%d/%d total=%d replay=%d unresolved=%d blocking=%d delivery_pos=%d",
			d.LogSourceID, d.AcquirePaused, d.PauseReason, d.BacklogPause,
			d.BacklogEntries, d.MaxEntries, d.BacklogTotalEntries, d.ReplayEntries,
			d.Unresolved, d.Blocking, d.DeliveryPos)+
			fmt.Sprintf(" consumed_diag=%d consumed_stats=%d consumed_inv=%d read_pos=%d reclaim_pos=%d invariant_ok=%v paused_exempt=%v",
				m.readingsConsumedDiagnostics.Load(), m.readingsConsumedStats.Load(), m.readingsConsumedInvariants.Load(),
				d.ReadPos, d.ReclaimPos,
				d.ReclaimPos <= d.ReadPos || d.AcquirePaused, // 暂停源豁免 ✓（见 ledger 侧同口径注释）
				d.AcquirePaused && d.ReclaimPos > d.ReadPos))
	}
	sort.Strings(lines)
	return lines
}

// gapReasonClassifiableOnResume 报告某原因是否属于"恢复后回读可愈合"的白名单。
//
// 判定**不再在本包内重复一份名单**（2026-10-10）：白名单的唯一真源是
// `ledger.IsGapReasonHealableOnReResume`，acquire 侧的分类裁定与这里的阻塞诊断共用它 ✓。
// 此前本函数与 acquire 侧各持一份按原因字面量写死的清单，两处漏改其一的表现是
// 「某原因永远不被自动消解、源永久停在暂停上且无任何报错」（无声失效 ✗）——
// 生产实测正是 `ROTATED_SEGMENT_NOT_READY` 落在两份名单之外 ⇒ 86 源永久阻塞 ✓。
func gapReasonClassifiableOnResume(reason string) bool {
	return ledger.IsGapReasonHealableOnReResume(reason)
}

// defaultSweepBackpressureStreak 是窗口化背压的默认阈值（连续 3 轮净涨才跳过 ✓）。
const defaultSweepBackpressureStreak = 3

func (m *Manager) sweepBackpressureStreakOf() int {
	if m == nil || m.sweepBackpressureStreak <= 0 {
		return defaultSweepBackpressureStreak
	}
	return m.sweepBackpressureStreak
}

// PositionInvariantViolations 转发账本层的"回收 ≤ 读"越界清单（**给分钟打点/诊断面消费** ✓）。
//
// 用户 2026-10-05：这已是第三次"新工具没有消费面" ✗✗（ResumeDiagnostics → ResumeSweepStats →
// PositionInvariantViolations）⇒ 本转发 + main 的分钟打点同时落地 ✓；流程纪律 = **任何新增导出
// 必须同批带可达面与可达性红证** ✓。
func (m *Manager) PositionInvariantViolations() []string {
	if m == nil {
		return nil
	}
	m.readingsConsumedInvariants.Add(1) // 消费计数（见字段注释 ✓）
	m.mu.Lock()
	pipes := make([]*pipeline.Pipeline, 0, len(m.pipes))
	for _, p := range m.pipes {
		if p != nil {
			pipes = append(pipes, p)
		}
	}
	m.mu.Unlock()
	var out []string
	for _, p := range pipes {
		if l := p.Ledger(); l != nil {
			out = append(out, l.PositionInvariantViolations()...)
		}
	}
	sort.Strings(out)
	return out
}

// SelfFailureReasons 汇总各管道**最近的自愈失败原因**（新→旧、有界 ✓），供分钟打点消费 ✓。
func (m *Manager) SelfFailureReasons() []string {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	pipes := make([]*pipeline.Pipeline, 0, len(m.pipes))
	for _, p := range m.pipes {
		if p != nil {
			pipes = append(pipes, p)
		}
	}
	m.mu.Unlock()
	var out []string
	for _, p := range pipes {
		for _, r := range p.SelfFailureReasons() {
			out = append(out, p.Key().LogSourceID+": "+r)
			if len(out) >= 8 {
				return out
			}
		}
	}
	return out
}

// ResumeSweepStats 返回兜底环的**跳过原因分类读数**（现场"41 家为何不动"的直接答案 ✓）。
type ResumeSweepStats struct {
	Candidates       int64 // 进入本轮容量内的候选源数
	Revived          int64 // 本轮真正复活的源数
	SkipBackpressure int64
	SkipHysteresis   int64
	SkipEnumeration  int64
	RisingStreak     int64
	// SweepTicks / SweepPanics：**心跳与崩溃计数**（现场据此判"环活着吗" ✓）。
	SweepTicks  int64
	SweepPanics int64
	// EligibleNow 是**合格暂停源总数（gauge，不是累计 ✓）**；与 Candidates（累计）并读即知覆盖进度 ✓。
	EligibleNow int64
	// SkipBlocked：因**有界等待超时**而跳过的步数（现场据此判"是否被 m.mu 之类的长持有挡住" ✓）。
	SkipBlocked int64
	// **耗时读数**：三步各自累计 + 每轮总耗时 + 上轮处理源数 ⇒ 平均每源一眼可算 ✓。
	NsDelivery       int64
	NsResolve        int64
	NsEvaluate       int64
	NsRoundTotal     int64
	LastRoundSources int64
	// **在飞定界读数**（新账 2 ✓）：当前在飞（含被放弃）+ 因到上限而拒发的步数 ⇒ 现场一眼判叠加压力 ✓。
	InFlight        int64
	SkipInflightCap int64
	// **四相读数**（见字段注释 ✓）。
	NsScanElig     int64
	NsBacklogTotal int64
	NsSyncQuota    int64
	// 逐相"最长单次 m.mu 等待"（ns ✓；支 A 直接点名该相 ✓）。
	MuWaitBacklogMax int64
	MuWaitQuotaMax   int64
	MuWaitSourcesMax int64
	MuWaitResumeMax  int64
	// RollingProbes / NsRolling（#66-⑤ 只读）：兜底环轮转窗口里**实际发起**的"只滚动/补段"自愈次数
	// 与耗时（前置为 O(1) 有欠账判据 ⇒ 健康源不发起 ⇒ 本读数即成本定界证明 ✓）。
	RollingProbes int64
	NsRolling     int64
	// DeliveryProbes / NsRollingDelivery（#66-⑥ 只读）：同上窗口里**实际发起**的"有界投递步"
	// 次数与耗时（前置为 O(1) 投递欠账判据 ⇒ 无欠账不发起 ⇒ 本读数即成本定界证明 ✓）。
	DeliveryProbes    int64
	NsRollingDelivery int64
	// DirectDeliverProbes / NsRollingDirectDeliver（#179-② / INGEST-12 只读）：同上窗口里
	// **实际发起**的"破环直投"步次数与耗时。该步含 Ledger 深克隆 + WAL 全量快照 + 同步 VL 写
	// （本文件自述"把单轮拖到 48.5 分钟"的 O(n) 重活）；旧形态它不经有界执行器、也不产读数。
	DirectDeliverProbes    int64
	NsRollingDirectDeliver int64
}

func (m *Manager) ResumeSweepStats() ResumeSweepStats {
	if m == nil {
		return ResumeSweepStats{}
	}
	m.readingsConsumedStats.Add(1) // 消费计数（见字段注释 ✓）
	return ResumeSweepStats{
		Candidates:       m.sweepCandidates.Load(),
		Revived:          m.sweepRevived.Load(),
		SkipBackpressure: m.skipBackpressure.Load(),
		SkipHysteresis:   m.skipHysteresis.Load(),
		SkipEnumeration:  m.skipEnumeration.Load(),
		RisingStreak:     m.risingStreak.Load(),
		SweepTicks:       m.sweepTicks.Load(),
		SweepPanics:      m.sweepPanics.Load(),
		EligibleNow:      m.sweepEligibleNow.Load(),
		SkipBlocked:      m.skipBlocked.Load(),
		NsDelivery:       m.nsDelivery.Load(),
		NsResolve:        m.nsResolve.Load(),
		NsEvaluate:       m.nsEvaluate.Load(),
		NsRoundTotal:     m.nsRoundTotal.Load(),
		LastRoundSources: m.lastRoundSources.Load(),
		InFlight:         m.sweepInflight.Load(),
		SkipInflightCap:  m.skipInflightCap.Load(),
		NsScanElig:       m.nsScanElig.Load(),
		NsBacklogTotal:   m.nsBacklogTotal.Load(),
		NsSyncQuota:      m.nsSyncQuota.Load(),
		MuWaitBacklogMax: m.muWaitBacklog.Load(),
		MuWaitQuotaMax:   m.muWaitQuota.Load(),
		MuWaitSourcesMax: m.muWaitSources.Load(),
		MuWaitResumeMax:  m.muWaitResume.Load(),
		RollingProbes:    m.sweepRollingProbes.Load(),
		NsRolling:        m.nsRolling.Load(),

		DeliveryProbes:         m.sweepDeliveryProbes.Load(),
		NsRollingDelivery:      m.nsRollingDelivery.Load(),
		DirectDeliverProbes:    m.sweepDirectDeliverProbes.Load(),
		NsRollingDirectDeliver: m.nsRollingDirectDeliver.Load(),
	}
}
