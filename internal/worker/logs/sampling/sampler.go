package sampling

import (
	"fmt"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// Signal 是风暴降级的触发读数，由调用方从既有读数填充（容量门禁的磁盘使用率、
// WAL 积压字节）。本包不自己采数：读数来源各自已有真源，重复采一遍只会制造分叉。
type Signal struct {
	// DiskPercent 节点磁盘使用率（0..100）。
	DiskPercent float64
	// BacklogBytes WAL 积压字节。
	BacklogBytes uint64
}

// decision 是单条事件的处理结论：放行原文，或折叠进汇总段。
type decision struct {
	event logtypes.Event
	fold  bool
	rule  string
}

// sigState 是单个模式签名的滑动窗口计数。
type sigState struct {
	windowStart time.Time
	count       int
}

// budgetState 是每源预算的窗口状态。
type budgetState struct {
	windowStart time.Time
	kept        int
	sinceSample int
}

// degradeState 是风暴降级状态。
type degradeState struct {
	active    bool
	reason    string
	clearFrom time.Time
}

// Stats 是采样器的累计观测读数（写日志与指标的唯一来源）。
type Stats struct {
	// Seen 进入判定的事件总数。
	Seen int64
	// Passed 原文放行数。
	Passed int64
	// Suppressed 被折叠的原文条数（注意：不等于落库条数）。
	Suppressed int64
	// Aggregates 产出的汇总事件条数（这才是真实落库条数）。
	Aggregates int64
	// ByRule 按规则的折叠条数。
	ByRule map[string]int64
	// Signatures 当前模式表大小。
	Signatures int
	// Degraded 当前是否处于风暴降级。
	Degraded bool
	// DegradeReason 降级原因（未降级为空）。
	DegradeReason string
	// DegradeTransitions 降级状态切换次数（进/出各计一次）。
	DegradeTransitions int64
}

// SavedRatio 返回被折叠条数占进入判定条数的比例（0 表示没有折叠）。
// 这是「采样实际省了多少」的直接读数——比配置值更能说明现场发生了什么。
func (s Stats) SavedRatio() float64 {
	if s.Seen <= 0 {
		return 0
	}
	return float64(s.Suppressed) / float64(s.Seen)
}

// RowMultiplier 返回「进入判定的事件数 / 实际落库条数」的比值。
// 1.0 = 未压缩；N 表示每 N 条原文压成 1 条。
func (s Stats) RowMultiplier() float64 {
	stored := s.Passed + s.Aggregates
	if stored <= 0 {
		return 1
	}
	return float64(s.Passed+s.Suppressed) / float64(stored)
}

// Sampler 是**有状态**的采集侧采样器：每个日志源一个实例。
//
// 并发口径与既有 pipeline/acquire 一致：由采集轮对单个源串行调用，故内部不加锁。
// 状态跨批保留（「N 次/s」是速率判定，单批粒度算不出来），但**只保留计数与窗口起点**，
// 不缓存事件正文——缓存事件正文正是本项目多次记载的无界内存增长形态。
type Sampler struct {
	policy Policy
	norm   Policy

	sigs  map[string]*sigState
	order []string

	budget  budgetState
	degrade degradeState

	stats Stats
}

// New 创建采样器。policy 会先 Normalize；非法取值不在此处拒绝
// （由配置层在启动阶段调用 Policy.Validate 拒绝），以便测试能构造极端夹具。
func New(policy Policy) *Sampler {
	s := &Sampler{policy: policy}
	s.SetPolicy(policy)
	return s
}

// SetPolicy 替换策略（保留既有窗口状态；切换规则不该清零速率读数）。
func (s *Sampler) SetPolicy(policy Policy) {
	if s == nil {
		return
	}
	s.policy = policy
	s.norm = policy.Normalize()
	if !s.policy.Enabled {
		// 关闭即彻底回到恒等：不留下会污染下次启用的陈旧窗口状态。
		s.sigs = nil
		s.order = nil
		s.budget = budgetState{}
		s.degrade = degradeState{}
	}
}

// Policy 返回当前策略。
func (s *Sampler) Policy() Policy {
	if s == nil {
		return Policy{}
	}
	return s.policy
}

// Degraded 返回当前降级状态与原因。
func (s *Sampler) Degraded() (bool, string) {
	if s == nil {
		return false, ""
	}
	return s.degrade.active, s.degrade.reason
}

// UpdateSignal 按当前读数推进降级/恢复状态机，返回 (状态是否发生变化, 当前是否降级)。
//
// 触发：任一阈值达到 ⇒ 立即降级（「先到先触发」，不等待另一个读数）。
// 恢复：所有阈值都不再达到、且连续保持 Hold 时长 ⇒ 恢复。
// 恢复必须带保持期：风暴的读数本来就是抖动的，一达到阈值就进出会让策略本身变成噪声源。
func (s *Sampler) UpdateSignal(sig Signal, now time.Time) (bool, bool) {
	if s == nil || !s.policy.Enabled {
		return false, false
	}
	d := s.norm.Degrade
	if !d.Enabled {
		return false, false
	}
	triggered, reason := d.evaluate(sig)
	if triggered {
		if s.degrade.active && s.degrade.reason == reason {
			return false, true
		}
		s.degrade = degradeState{active: true, reason: reason}
		s.stats.DegradeTransitions++
		s.stats.Degraded = true
		s.stats.DegradeReason = reason
		return true, true
	}
	if !s.degrade.active {
		return false, false
	}
	if s.degrade.clearFrom.IsZero() {
		// 读数已回落，开始计时；本次仍是降级态。
		s.degrade.clearFrom = now
		return false, true
	}
	if now.Sub(s.degrade.clearFrom) < d.Hold {
		return false, true
	}
	s.degrade = degradeState{}
	s.stats.DegradeTransitions++
	s.stats.Degraded = false
	s.stats.DegradeReason = ""
	return true, false
}

// evaluate 判断读数是否触发降级。
func (d Degrade) evaluate(sig Signal) (bool, string) {
	if d.DiskPercent > 0 && sig.DiskPercent >= d.DiskPercent {
		return true, fmt.Sprintf("disk %.1f%% >= %.1f%%", sig.DiskPercent, d.DiskPercent)
	}
	if d.BacklogBytes > 0 && sig.BacklogBytes >= d.BacklogBytes {
		return true, fmt.Sprintf("backlog %d bytes >= %d", sig.BacklogBytes, d.BacklogBytes)
	}
	return false, ""
}

// Stats 返回累计读数的副本。
func (s *Sampler) Stats() Stats {
	if s == nil {
		return Stats{}
	}
	out := s.stats
	out.ByRule = make(map[string]int64, len(s.stats.ByRule))
	for k, v := range s.stats.ByRule {
		out.ByRule[k] = v
	}
	out.Signatures = len(s.sigs)
	out.Degraded = s.degrade.active
	out.DegradeReason = s.degrade.reason
	return out
}

// ResetStats 清零累计读数（窗口状态与降级状态保留）。
func (s *Sampler) ResetStats() {
	if s == nil {
		return
	}
	s.stats = Stats{}
	if s.degrade.active {
		s.stats.Degraded = true
		s.stats.DegradeReason = s.degrade.reason
	}
}

// Process 是采样器的唯一入口：返回本批**实际应当落库**的事件序列。
//
// 关键性质（不变量 R，见 doc.go）：
//
//	MergePositionRanges(Process(in)) == MergePositionRanges(in)
//
// 即输出的 Record 区间并集与输入逐段相等——抑制只在段内做替换，绝不在连续覆盖上留空洞。
// 这不是「顺手保持」，而是让既有的缺口自动消解判据（连续覆盖）在开启采样后仍然成立的前提。
//
// 策略未启用、或事件为空时恒等返回入参（连切片都不复制），保证零行为变化。
func (s *Sampler) Process(events []logtypes.Event) []logtypes.Event {
	if s == nil || len(events) == 0 || !s.policy.Enabled {
		return events
	}
	decisions := make([]decision, 0, len(events))
	for _, event := range events {
		s.stats.Seen++
		rule := s.classify(event)
		if rule == "" {
			s.stats.Passed++
			decisions = append(decisions, decision{event: event})
			continue
		}
		s.stats.Suppressed++
		if s.stats.ByRule == nil {
			s.stats.ByRule = make(map[string]int64, 4)
		}
		s.stats.ByRule[rule]++
		decisions = append(decisions, decision{event: event, fold: true, rule: rule})
	}
	return s.emit(decisions)
}

// classify 返回 "" 表示放行原文，否则返回折叠规则名。
//
// 判定顺序即优先级：等级过滤（含降级抬高）→ 高频抑制 → 每源预算。
// 粗筛在前是有意的：已经被等级过滤掉的行不该再占用高频抑制的模式表与预算窗口，
// 否则噪声会先把有用的窗口额度吃光。
func (s *Sampler) classify(event logtypes.Event) string {
	if effective, byDegrade := s.effectiveMinLevel(); LevelBelow(event.Level, effective) {
		if byDegrade {
			return RuleStormDegrade
		}
		return RuleLevelFilter
	}
	if s.norm.Burst.Window > 0 && s.burstExceeded(event) {
		return RuleBurstSuppress
	}
	if !s.budgetAllows(event) {
		return RuleBudgetSample
	}
	return ""
}

// effectiveMinLevel 返回当前生效的最低保留级别，以及它是否由降级抬高而来。
// 降级只抬高、不降低：运维显式配的 min_level 比降级目标更严时，以运维配置为准。
func (s *Sampler) effectiveMinLevel() (string, bool) {
	base := s.norm.Level.MinLevel
	if !s.degrade.active {
		return base, false
	}
	target := s.norm.Degrade.TargetLevel
	if base == "" {
		return target, true
	}
	if Rank(target) > Rank(base) {
		return target, true
	}
	return base, false
}

// burstExceeded 判定该事件是否超出「同源同消息」窗口额度。
//
// 时间不可解析时**不抑制**：算不出「N 次/s」就不该假装算得出。这条底线保证采样
// 永远不会因为时间字段异常而变成静默丢弃。
func (s *Sampler) burstExceeded(event logtypes.Event) bool {
	when, ok := eventTime(event)
	if !ok {
		return false
	}
	sig := SignatureOf(event.Level, event.Stream, event.Message)
	st := s.sigState(sig)
	if st.windowStart.IsZero() || when.Before(st.windowStart) || when.Sub(st.windowStart) >= s.norm.Burst.Window {
		st.windowStart = when
		st.count = 0
	}
	st.count++
	return st.count > s.norm.Burst.Threshold
}

// budgetAllows 判定该事件是否落在每源预算之内。
//
// 预算用尽后按 KeepEvery 采样（每 N 条保留 1 条原文，其余折叠为汇总），
// 于是「quota → 采样比例」是确定性可复算的，不依赖随机数——重投必须得到同一结论。
func (s *Sampler) budgetAllows(event logtypes.Event) bool {
	if s.norm.Budget.MaxEventsPerWindow <= 0 {
		return true
	}
	when, ok := eventTime(event)
	if !ok {
		return true
	}
	b := &s.budget
	if b.windowStart.IsZero() || when.Before(b.windowStart) || when.Sub(b.windowStart) >= s.norm.Budget.Window {
		b.windowStart = when
		b.kept = 0
		b.sinceSample = 0
	}
	if b.kept < s.norm.Budget.MaxEventsPerWindow {
		b.kept++
		return true
	}
	b.sinceSample++
	if b.sinceSample >= s.norm.Budget.KeepEvery {
		b.sinceSample = 0
		b.kept++
		return true
	}
	return false
}

// sigState 取（按需创建）签名对应的窗口状态，并保证模式表有界。
func (s *Sampler) sigState(sig Signature) *sigState {
	if s.sigs == nil {
		s.sigs = make(map[string]*sigState, 64)
	}
	if st, ok := s.sigs[sig.Key]; ok {
		return st
	}
	max := s.norm.Burst.MaxSignatures
	if max <= 0 {
		max = DefaultMaxSignatures
	}
	if len(s.sigs) >= max {
		s.evictSignatures()
	}
	st := &sigState{}
	s.sigs[sig.Key] = st
	s.order = append(s.order, sig.Key)
	return st
}

// evictSignatures 先清过期窗口，仍超限再按插入顺序淘汰最旧（近似 FIFO）。
//
// 有界性是硬要求：模式表按「不同消息模板数」增长，而真实刷屏日志的模板数可以很大；
// 让采样自身成为无界增长点，等于用一个新的内存问题换掉一个成本问题。
func (s *Sampler) evictSignatures() {
	window := s.norm.Burst.Window
	if window <= 0 {
		window = DefaultBurstWindow
	}
	kept := s.order[:0]
	// 以最近写入的窗口起点为基准，过期即回收；不引入挂钟，保持与判定同一时间基准。
	var latest time.Time
	for _, st := range s.sigs {
		if st.windowStart.After(latest) {
			latest = st.windowStart
		}
	}
	for _, key := range s.order {
		st, ok := s.sigs[key]
		if !ok {
			continue
		}
		if !latest.IsZero() && latest.Sub(st.windowStart) >= window {
			delete(s.sigs, key)
			continue
		}
		kept = append(kept, key)
	}
	s.order = kept
	// 仍超限：淘汰最旧的一批（FIFO），确保调用方拿到空间。
	for len(s.sigs) >= s.norm.Burst.MaxSignatures && len(s.order) > 0 {
		key := s.order[0]
		s.order = s.order[1:]
		delete(s.sigs, key)
	}
}

// runKeyFor 返回折叠段的归并键。
//
// 归并粒度**按规则而定**，这不是可选的细节：
//   - 等级过滤/风暴降级要的正是「整批按级别折叠」，若还把消息模式算进归并键，
//     一百条互不相同的 DEBUG 行就会产出一百条各自只有一条的汇总——体积没省下来，
//     还额外多了一层包装。所以这两条规则只按 (级别, 流) 归并。
//   - 高频抑制/预算采样的语义本身就是「同一个模板重复」，故 (级别, 流, 模式) 三者都要。
//
// 无论哪种粒度，**级别与流都必须在键内**：跨级别的行不得混入同一汇总，
// 否则汇总的 level 字段会撒谎，按级别的保留期也对不上它声称的级别。
func runKeyFor(rule string, sig Signature) string {
	switch rule {
	case RuleLevelFilter, RuleStormDegrade:
		return rule + "\x00" + sig.Level + "\x00" + sig.Stream
	default:
		return rule + "\x00" + sig.Key
	}
}

// emit 把判定序列折成输出序列：连续的同类折叠事件合并为一条汇总事件。
//
// 段内必须同规则、同归并键（见 runKeyFor）、**区间相邻**。三个条件缺一不可：
//   - 同规则：否则汇总无法如实回答「为什么被折叠」；
//   - 同归并键：否则汇总的级别/流字段会撒谎，跨级别的行也会混进同一条汇总；
//   - 区间相邻：否则首尾跨度会把中间**未被抑制**的事件一并圈进来，那就是凸包——
//     正是缺口判据明令否掉的形态，且会让汇总声称覆盖了它其实没覆盖的字节。
//
// 段长超过 MaxAggregateEvents 时提前收段：新段与原段区间相邻，故不变量 R 不受影响。
func (s *Sampler) emit(decisions []decision) []logtypes.Event {
	out := make([]logtypes.Event, 0, len(decisions))
	maxRun := s.norm.MaxAggregateEvents
	var (
		run    SuppressedRun
		runKey string
	)
	flush := func() {
		if len(run.Events) == 0 {
			return
		}
		out = append(out, BuildAggregate(run, s.degrade.active))
		s.stats.Aggregates++
		run.Events = nil
		runKey = ""
	}
	for _, d := range decisions {
		if !d.fold {
			flush()
			out = append(out, d.event)
			continue
		}
		sig := SignatureOf(d.event.Level, d.event.Stream, d.event.Message)
		key := runKeyFor(d.rule, sig)
		if len(run.Events) > 0 {
			last := run.Events[len(run.Events)-1]
			adjacent := d.event.Record.Start <= last.Record.End+1
			// 同源是隐含前提而非假设：批次本应按源切分，但一旦上游出现问题把两个源的
			// 事件混进同一批，汇总会以首条的 Source 给整段盖章——那是**伪造归属**。
			// 这里显式挡住：宁可多产出一条汇总，也不能让事件的源信息说谎。
			sameSource := d.event.Source == last.Source
			if run.Rule != d.rule || runKey != key || !adjacent || !sameSource || len(run.Events) >= maxRun {
				flush()
			}
		}
		if len(run.Events) == 0 {
			run = SuppressedRun{Rule: d.rule}
			runKey = key
		}
		run.Events = append(run.Events, d.event)
	}
	flush()
	return out
}

// eventTime 解析事件语义时间。解析失败返回 false（调用方据此放弃速率类判定）。
func eventTime(event logtypes.Event) (time.Time, bool) {
	raw := event.EventTimeUTC
	if raw == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}
