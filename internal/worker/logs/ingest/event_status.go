package ingest

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/eventstore"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 本文件实现**事件级状态可见性**（后续项③）——一份**派生态**诊断视图。
//
// ## 为什么是派生态，而不是给每条日志落一个持久化状态位
//
// 给每条事件写一个持久化状态字段（→ 落库 → 逐条更新）会毁掉单位成本成果：事件量按
// 行数增长，而状态位让每次状态跃迁都变成一次索引写入；且它与既有的
// `positions`（四水位）+ `event_id`（身份）**语义冗余**——"这条事件现在处于哪一态"
// 完全由「位置落在哪个区间 + 该区间的证据」推出，多写一份就是多一个会漂移的真源。
//
// 因此本视图只从**既有事实**派生（不新增任何持久化状态）：
//   - `positions.delivery_position`：诚实的连续前缀（其内的位置都已确认投递）；
//   - `delivery_batches`：批次级四值（NOT_SENT / UNKNOWN / REQUEST_DONE / REPLAY_REQUIRED）；
//   - `verified_runs`：逐字段可见性校验通过的连续区间（已校验）；
//   - `abandonments`：人工裁定永久丢失的区间（已放弃）；
//   - `gaps`：已登记缺口（未发区间的**原因**）。
//
// ## 五态的判据与优先级（务必按名字读）
//
//	ABANDONED    落在放弃凭据区间内 —— 最强事实：该区间**永远不会**有数据（人工裁定 + 留痕）
//	VERIFIED     落在逐字段校验通过区间内 —— 已确认落库且内容级校验通过
//	ACKED        落在 delivery_position 之内，或落在 REQUEST_DONE 批次内 —— 请求已确认完成
//	SENT_UNACKED 落在 UNKNOWN 批次内 —— 请求已发出但结果未知（可能已落库，未确认）
//	UNSENT       其余：在 WAL 里等待投递（含 durable 未确认前缀）、NOT_SENT/REPLAY_REQUIRED
//	             批次、或落在已登记缺口内（此时 Reason 给出缺口原因）
//
// 优先级说明：放弃凭据的区间本就不会与 delivery 前缀重叠（放弃只作用于未解决缺口），
// 保留其最高优先级是为了让"人工裁定"永远压过其它证据；VERIFIED 高于 ACKED 是因为
// 它是更强的证据（内容级）；批次被裁剪过的历史区间靠 delivery 前缀（判 ACKED）兜底。
//
// ## 出口
//
//   - 运行期：Manager.SourceEventStatus（账本 + 事件段，进程内只读查询）；
//   - 离线：worker CLI `log-event-status` → ExportEventStatus（索引库只读 + 事件段可选）。

// EventState 是事件级派生态的取值。
type EventState string

const (
	// EventStateUnsent 未发（尚无成功投递凭证；可能在 WAL 中等待，或落在已登记缺口内）。
	EventStateUnsent EventState = "UNSENT"
	// EventStateSentUnacked 已发未认（请求已发出，结果未知）。
	EventStateSentUnacked EventState = "SENT_UNACKED"
	// EventStateAcked 已认（请求已确认完成；落在诚实连续前缀内）。
	EventStateAcked EventState = "ACKED"
	// EventStateVerified 已校验（逐字段可见性校验通过）。
	EventStateVerified EventState = "VERIFIED"
	// EventStateAbandoned 已放弃（人工裁定永久丢失，结构化留痕）。
	EventStateAbandoned EventState = "ABANDONED"
)

// EventStatusFacts 是派生所需的事实集合（全部来自既有账本/持久化，不新增状态位）。
type EventStatusFacts struct {
	Positions logtypes.Positions
	// Batches 是批次级投递结果（可能已被裁剪：被裁的历史区间靠 Positions.Delivery 兜底）。
	Batches []ledger.DeliveryBatch
	// VerifiedRuns 是逐字段可见性校验通过的连续区间。
	VerifiedRuns []ledger.PositionRange
	// Abandonments 是人工裁定永久丢失的区间凭据。
	Abandonments []ledger.GapAbandonment
	// Gaps 是已登记缺口（用于给 UNSENT 段标注**原因**，不改变态判定）。
	Gaps []ledger.Gap
}

// EventStatusSegment 是一段同态区间（闭区间 [From, To]，位置口径 = record_start/end 字节位）。
type EventStatusSegment struct {
	From  uint64     `json:"from"`
	To    uint64     `json:"to"`
	State EventState `json:"state"`
	// Basis 是判据来源（delivery_prefix / delivery_batch / verified_run / abandonment / gap / no_evidence）。
	Basis string `json:"basis"`
	// BatchState 是该段命中批次时的原始四值（未命中批次时为空）。
	BatchState string `json:"batch_state,omitempty"`
	// Reason 是该段命中缺口时的缺口原因。
	Reason string `json:"reason,omitempty"`
	// AbandonmentReason/Operator/AtUTC 是该段命中放弃凭据时的留痕（谁、何时、为什么）。
	AbandonmentReason   string `json:"abandonment_reason,omitempty"`
	AbandonmentOperator string `json:"abandonment_operator,omitempty"`
	AbandonmentAtUTC    string `json:"abandonment_at_utc,omitempty"`
}

// EventStatusSummary 是按态聚合的段数与覆盖字节数（读数面）。
type EventStatusSummary struct {
	State    EventState `json:"state"`
	Segments int        `json:"segments"`
	Bytes    uint64     `json:"bytes"`
}

// EventStatusReport 是一次事件级状态查询的结论。
type EventStatusReport struct {
	Source         string               `json:"source"`
	From           uint64               `json:"from"`
	To             uint64               `json:"to"`
	Positions      logtypes.Positions   `json:"positions"`
	Segments       []EventStatusSegment `json:"segments"`
	Summary        []EventStatusSummary `json:"summary"`
	AtUTC          string               `json:"at_utc,omitempty"`
	LocatedEventID string               `json:"located_event_id,omitempty"`
	LocatedAt      string               `json:"located_at,omitempty"`
	GeneratedAt    time.Time            `json:"generated_at"`
	Notes          []string             `json:"notes,omitempty"`
}

// DeriveEventStatus 把事实集合派生为「区间 → 态」的段落序列（纯函数，可独立测试）。
//
// from/to 为闭区间；两者都为 0 时按 [0, 全水位] 解释（见 eventStatusBounds）。
func DeriveEventStatus(source string, facts EventStatusFacts, from, to uint64) EventStatusReport {
	from, to = eventStatusBounds(facts, from, to)
	report := EventStatusReport{
		Source: source, From: from, To: to, Positions: facts.Positions,
		GeneratedAt: time.Now().UTC(),
	}
	if to < from {
		from, to = to, from
		report.From, report.To = from, to
	}
	points := map[uint64]struct{}{from: {}, to + 1: {}}
	addPoint := func(value uint64, upper uint64) {
		if value < from || value > upper {
			return
		}
		points[value] = struct{}{}
	}
	// 水位边界必须参与切分：delivery_position 是「诚实连续前缀」的判据边界，
	// 少了它，跨过前缀边界的子区间会整段被判成未发（前缀内的已认部分被淹没）。
	if facts.Positions.Delivery < ^uint64(0) {
		addPoint(facts.Positions.Delivery+1, to+1)
	}
	for _, batch := range facts.Batches {
		addPoint(batch.Start, to+1)
		if batch.End < ^uint64(0) {
			addPoint(batch.End+1, to+1)
		}
	}
	for _, run := range facts.VerifiedRuns {
		addPoint(run.From, to+1)
		if run.To < ^uint64(0) {
			addPoint(run.To+1, to+1)
		}
	}
	for _, ab := range facts.Abandonments {
		addPoint(ab.From, to+1)
		if ab.To < ^uint64(0) {
			addPoint(ab.To+1, to+1)
		}
	}
	for _, gap := range facts.Gaps {
		addPoint(gap.StartPos, to+1)
		if gap.EndPos < ^uint64(0) {
			addPoint(gap.EndPos+1, to+1)
		}
	}
	ordered := make([]uint64, 0, len(points))
	for point := range points {
		ordered = append(ordered, point)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })

	segments := make([]EventStatusSegment, 0, len(ordered))
	for index := 0; index+1 < len(ordered); index++ {
		segFrom, segTo := ordered[index], ordered[index+1]-1
		if segTo < segFrom {
			continue
		}
		segment := classifyEventStatus(facts, segFrom, segTo)
		if len(segments) > 0 {
			last := &segments[len(segments)-1]
			if sameEventStatusSegment(last, &segment) && last.To+1 == segment.From {
				last.To = segment.To
				continue
			}
		}
		segments = append(segments, segment)
	}
	report.Segments = segments
	report.Summary = summarizeEventStatus(segments)
	return report
}

// sameEventStatusSegment 判定两段是否同态同据（合并相邻段用）。
func sameEventStatusSegment(a, b *EventStatusSegment) bool {
	return a.State == b.State && a.Basis == b.Basis && a.BatchState == b.BatchState &&
		a.Reason == b.Reason && a.AbandonmentOperator == b.AbandonmentOperator
}

// eventStatusBounds 归一化查询区间：from/to 全零表示「整源」（上界取全部证据与水位的最大值）。
func eventStatusBounds(facts EventStatusFacts, from, to uint64) (uint64, uint64) {
	if from != 0 || to != 0 {
		return from, to
	}
	ceiling := facts.Positions.Read
	for _, candidate := range []uint64{facts.Positions.Durable, facts.Positions.Delivery, facts.Positions.Reclaim} {
		if candidate > ceiling {
			ceiling = candidate
		}
	}
	for _, batch := range facts.Batches {
		if batch.End > ceiling {
			ceiling = batch.End
		}
	}
	for _, run := range facts.VerifiedRuns {
		if run.To > ceiling {
			ceiling = run.To
		}
	}
	for _, ab := range facts.Abandonments {
		if ab.To > ceiling {
			ceiling = ab.To
		}
	}
	for _, gap := range facts.Gaps {
		if gap.EndPos > ceiling {
			ceiling = gap.EndPos
		}
	}
	return 0, ceiling
}

// classifyEventStatus 对一个**已被证据端点切分**的子区间判态（子区间与证据区间要么包含、
// 要么不相交，故此处只需按优先级取第一个覆盖者）。
func classifyEventStatus(facts EventStatusFacts, from, to uint64) EventStatusSegment {
	if ab, ok := coveringAbandonment(facts.Abandonments, from, to); ok {
		return EventStatusSegment{
			From: from, To: to, State: EventStateAbandoned, Basis: "abandonment",
			AbandonmentReason: ab.ReasonCode, AbandonmentOperator: ab.Operator, AbandonmentAtUTC: ab.AtUTC,
		}
	}
	if coveringRanges(facts.VerifiedRuns, from, to) {
		return EventStatusSegment{From: from, To: to, State: EventStateVerified, Basis: "verified_run"}
	}
	if to <= facts.Positions.Delivery {
		// 诚实的连续前缀：其内位置都已确认投递（批次可能已被裁剪，故这是兜底证据）。
		return EventStatusSegment{From: from, To: to, State: EventStateAcked, Basis: "delivery_prefix"}
	}
	if batch, ok := coveringBatch(facts.Batches, from, to); ok {
		segment := EventStatusSegment{From: from, To: to, Basis: "delivery_batch", BatchState: string(batch.State)}
		switch batch.State {
		case logtypes.DeliveryRequestDone:
			segment.State = EventStateAcked
		case logtypes.DeliveryUnknown:
			segment.State = EventStateSentUnacked
		default:
			// NOT_SENT / REPLAY_REQUIRED / 未知值：尚无成功凭证 → 未发（BatchState 保留原始值）。
			segment.State = EventStateUnsent
		}
		return segment
	}
	if gap, ok := coveringGap(facts.Gaps, from, to); ok {
		return EventStatusSegment{
			From: from, To: to, State: EventStateUnsent, Basis: "gap",
			Reason: strings.TrimSpace(gap.Reason + " " + gap.Detail),
		}
	}
	return EventStatusSegment{From: from, To: to, State: EventStateUnsent, Basis: "no_evidence"}
}

func coveringRanges(runs []ledger.PositionRange, from, to uint64) bool {
	for _, run := range runs {
		if run.From <= from && to <= run.To {
			return true
		}
	}
	return false
}

func coveringAbandonment(items []ledger.GapAbandonment, from, to uint64) (ledger.GapAbandonment, bool) {
	for _, item := range items {
		if item.From <= from && to <= item.To {
			return item, true
		}
	}
	return ledger.GapAbandonment{}, false
}

func coveringBatch(items []ledger.DeliveryBatch, from, to uint64) (ledger.DeliveryBatch, bool) {
	for _, item := range items {
		if item.Start <= from && to <= item.End {
			return item, true
		}
	}
	return ledger.DeliveryBatch{}, false
}

func coveringGap(items []ledger.Gap, from, to uint64) (ledger.Gap, bool) {
	for _, item := range items {
		if item.StartPos <= from && to <= item.EndPos {
			return item, true
		}
	}
	return ledger.Gap{}, false
}

func summarizeEventStatus(segments []EventStatusSegment) []EventStatusSummary {
	counts := map[EventState]*EventStatusSummary{}
	for _, segment := range segments {
		entry, ok := counts[segment.State]
		if !ok {
			entry = &EventStatusSummary{State: segment.State}
			counts[segment.State] = entry
		}
		entry.Segments++
		entry.Bytes += segment.To - segment.From + 1
	}
	out := make([]EventStatusSummary, 0, len(counts))
	for _, entry := range counts {
		out = append(out, *entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].State < out[j].State })
	return out
}

// ---------------------------------------------------------------------------
// 运行期查询
// ---------------------------------------------------------------------------

// EventStatusQuery 是一次运行期事件级状态查询。
type EventStatusQuery struct {
	LogSourceID      string
	SourceGeneration string
	// From/To 是源位置闭区间；两者为 0 时表示「整源」。
	From uint64
	To   uint64
	// AtUTC 非空时按**时间点**查询：定位到该时刻（或之后最近）的事件，再按其位置区间判态。
	AtUTC string
}

// SourceEventStatus 运行期事件级状态查询（只读：不改账本、不写 VL、不推进任何水位）。
func (m *Manager) SourceEventStatus(query EventStatusQuery) (EventStatusReport, error) {
	if m == nil {
		return EventStatusReport{}, fmt.Errorf("ingest: manager unavailable")
	}
	sourceID := strings.TrimSpace(query.LogSourceID)
	if sourceID == "" {
		return EventStatusReport{}, fmt.Errorf("ingest: 事件级状态查询必须指定源")
	}
	key, _, err := m.resolveEventStatusSource(sourceID, query.SourceGeneration)
	if err != nil {
		return EventStatusReport{}, err
	}
	m.mu.Lock()
	p := m.pipes[key]
	saved := m.state.Sources[key]
	m.mu.Unlock()
	if p == nil {
		return EventStatusReport{}, fmt.Errorf("ingest: 源 %s 未登记采集管道", key)
	}
	facts := EventStatusFacts{VerifiedRuns: append([]ledger.PositionRange(nil), saved.VerifiedRuns...)}
	if entry := p.Ledger().Get(p.Key()); entry != nil {
		facts.Positions = entry.Positions
		facts.Batches = append([]ledger.DeliveryBatch(nil), entry.DeliveryBatches...)
		facts.Abandonments = append([]ledger.GapAbandonment(nil), entry.Abandonments...)
		facts.Gaps = append([]ledger.Gap(nil), entry.Gaps...)
	}
	report := DeriveEventStatus(key, facts, query.From, query.To)
	if at := strings.TrimSpace(query.AtUTC); at != "" {
		moment, err := time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return EventStatusReport{}, fmt.Errorf("ingest: 时间点必须为 RFC3339 格式: %w", err)
		}
		located, ok, err := locateEventAtOrAfter(m.events, key, moment.UTC())
		if err != nil {
			return EventStatusReport{}, err
		}
		if !ok {
			report.Notes = append(report.Notes, "该源事件段内没有可定位的事件（时间点查询无结论）")
			return report, nil
		}
		report = DeriveEventStatus(key, facts, located.Record.Start, located.Record.End)
		report.AtUTC = at
		report.LocatedEventID = located.EventID
		report.LocatedAt = located.EventTimeUTC
	}
	return report, nil
}

// resolveEventStatusSource 解析源键：支持精确 `<id>/<generation>` 与「仅 id（须唯一）」两种写法。
func (m *Manager) resolveEventStatusSource(sourceID, generation string) (string, SourceConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if generation != "" {
		key := sourceID + "/" + generation
		source, ok := m.sources[key]
		if !ok {
			return "", SourceConfig{}, fmt.Errorf("ingest: 源 %s 不存在", key)
		}
		return key, source, nil
	}
	matches := make([]string, 0, 2)
	for key, source := range m.sources {
		if source.LogSourceID == sourceID || key == sourceID {
			matches = append(matches, key)
		}
	}
	sort.Strings(matches)
	switch len(matches) {
	case 0:
		return "", SourceConfig{}, fmt.Errorf("ingest: 源 %s 不存在", sourceID)
	case 1:
		return matches[0], m.sources[matches[0]], nil
	default:
		return "", SourceConfig{}, fmt.Errorf("ingest: 源 %s 有多个代次（%s），请用 <id>/<generation> 精确指定",
			sourceID, strings.Join(matches, ", "))
	}
}

// locateEventAtOrAfter 在事件段里定位「时间 >= at 的最近事件」；该时刻晚于全部事件时
// 回退到最后一个事件（并如实标注——调用方据 LocatedAt 与 at 的差异可见）。
//
// 只用于**显式的时间点查询**：它要遍历该源事件段（O(段行数)），因此绝不出现在周期路径上。
func locateEventAtOrAfter(store *eventstore.Store, key string, at time.Time) (logtypes.Event, bool, error) {
	if store == nil {
		return logtypes.Event{}, false, fmt.Errorf("ingest: 事件段存储未打开，无法按时间点定位")
	}
	var best *logtypes.Event
	var last *logtypes.Event
	err := store.Iterate(key, func(event logtypes.Event) error {
		current := event
		if last == nil || current.Record.End >= last.Record.End {
			last = &current
		}
		moment, ok := parseEventTimeUTC(event.EventTimeUTC)
		if !ok || moment.Before(at) {
			return nil
		}
		if best == nil || current.Record.Start < best.Record.Start {
			best = &current
		}
		return nil
	})
	if err != nil {
		return logtypes.Event{}, false, err
	}
	if best != nil {
		return *best, true, nil
	}
	if last != nil {
		return *last, true, nil
	}
	return logtypes.Event{}, false, nil
}

// parseEventTimeUTC 解析事件语义时间（与 eventUTCDay 的时间策略一致：RFC3339Nano）。
func parseEventTimeUTC(raw string) (time.Time, bool) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, false
	}
	moment, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, false
	}
	return moment.UTC(), true
}

// ---------------------------------------------------------------------------
// 离线导出（worker CLI `log-event-status`）
// ---------------------------------------------------------------------------

// eventStatusIndexPath 是采集索引库的相对路径（与 ExportIndexJSON 同一口径）。
func eventStatusIndexPath(root string) string {
	return filepath.Join(root, "var", "log", "ingest.index.db")
}

// eventStatusEventsRoot 是事件段（canonical 事件体）的相对目录（与 ingest.New 同一口径）。
func eventStatusEventsRoot(root string) string {
	return filepath.Join(root, "var", "log", "events")
}

// ExportEventStatus 从**索引库**只读派生事件级状态视图（停机/现场取证用；不打开采集运行时）。
//
// 与运行期查询的差别与取舍（如实说明）：
//   - 本路径完全离线（不连 VL、不起采集轮），代价是只能看到**已持久化**的证据；
//   - 按时间点查询（--at）需要事件段（canonical 事件体）：目录不存在时**明确报错**，
//     而不是静默退化成一个空结论（"永远为空"正是本项验收要排除的形态）。
func ExportEventStatus(root string, args []string, out io.Writer) error {
	if strings.TrimSpace(root) == "" {
		return fmt.Errorf("ingest: 导出需要数据根目录")
	}
	options, err := parseEventStatusArgs(args)
	if err != nil {
		return err
	}
	store, err := stateindex.OpenReadOnly(eventStatusIndexPath(root))
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	rows, err := store.Load()
	if err != nil {
		return fmt.Errorf("ingest: 读取索引失败: %w", err)
	}
	state, err := indexStateToState(rows)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(state.Sources))
	for key := range state.Sources {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	selected, err := selectEventStatusKeys(state, keys, options.source)
	if err != nil {
		return err
	}
	var eventsStore *eventstore.Store
	if options.at != "" {
		eventsRoot := eventStatusEventsRoot(root)
		if info, statErr := os.Stat(eventsRoot); statErr != nil || !info.IsDir() {
			return fmt.Errorf("ingest: --at 需要事件段目录 %s（不存在或不可读），请改用 --from/--to 位置区间", eventsRoot)
		}
		eventsStore, err = eventstore.Open(eventsRoot)
		if err != nil {
			return err
		}
		defer func() { _ = eventsStore.Close() }()
	}
	reports := make([]EventStatusReport, 0, len(selected))
	for _, key := range selected {
		facts := eventStatusFactsOfPersisted(state.Sources[key])
		report := DeriveEventStatus(key, facts, options.from, options.to)
		if options.at != "" {
			moment, _ := time.Parse(time.RFC3339Nano, options.at) // 参数解析已校验格式
			located, ok, err := locateEventAtOrAfter(eventsStore, key, moment.UTC())
			if err != nil {
				return err
			}
			if !ok {
				report.Notes = append(report.Notes, "事件段里没有可定位的事件（时间点查询无结论）")
			} else {
				report = DeriveEventStatus(key, facts, located.Record.Start, located.Record.End)
				report.AtUTC = options.at
				report.LocatedEventID = located.EventID
				report.LocatedAt = located.EventTimeUTC
			}
		}
		// 离线视图只含已持久化证据（不含事件段正文与运行期内存态）：如实标注口径。
		report.Notes = append(report.Notes, "离线视图取自索引库（仅已持久化证据）")
		reports = append(reports, report)
	}
	payload := struct {
		Root    string              `json:"root"`
		Reports []EventStatusReport `json:"reports"`
	}{Root: root, Reports: reports}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(payload)
}

// eventStatusFactsOfPersisted 从持久化状态取派生事实（只读）。
func eventStatusFactsOfPersisted(saved persistedSource) EventStatusFacts {
	facts := EventStatusFacts{VerifiedRuns: append([]ledger.PositionRange(nil), saved.VerifiedRuns...)}
	for index, entry := range saved.Ledger {
		if index == 0 {
			facts.Positions = entry.Positions
		}
		facts.Batches = append(facts.Batches, entry.DeliveryBatches...)
		facts.Abandonments = append(facts.Abandonments, entry.Abandonments...)
		facts.Gaps = append(facts.Gaps, entry.Gaps...)
	}
	return facts
}

type eventStatusArgs struct {
	source string
	from   uint64
	to     uint64
	at     string
}

func parseEventStatusArgs(args []string) (eventStatusArgs, error) {
	var out eventStatusArgs
	for _, arg := range args {
		switch {
		case strings.HasPrefix(arg, "--source="):
			out.source = strings.TrimSpace(strings.TrimPrefix(arg, "--source="))
		case strings.HasPrefix(arg, "--from="):
			parsed, err := strconv.ParseUint(strings.TrimPrefix(arg, "--from="), 10, 64)
			if err != nil {
				return out, fmt.Errorf("ingest: --from 必须为非负整数: %w", err)
			}
			out.from = parsed
		case strings.HasPrefix(arg, "--to="):
			parsed, err := strconv.ParseUint(strings.TrimPrefix(arg, "--to="), 10, 64)
			if err != nil {
				return out, fmt.Errorf("ingest: --to 必须为非负整数: %w", err)
			}
			out.to = parsed
		case strings.HasPrefix(arg, "--at="):
			out.at = strings.TrimSpace(strings.TrimPrefix(arg, "--at="))
		}
	}
	if out.at != "" {
		if _, err := time.Parse(time.RFC3339Nano, out.at); err != nil {
			return out, fmt.Errorf("ingest: --at 必须为 RFC3339 时间: %w", err)
		}
	}
	return out, nil
}

// selectEventStatusKeys 解析 --source 选择器：精确 `<id>/<generation>` 或「仅 id（须唯一）」。空 = 全部源。
func selectEventStatusKeys(state *persistedState, keys []string, selector string) ([]string, error) {
	if selector == "" {
		return keys, nil
	}
	if strings.Contains(selector, "/") {
		if _, ok := state.Sources[selector]; !ok {
			return nil, fmt.Errorf("ingest: 源 %s 不存在", selector)
		}
		return []string{selector}, nil
	}
	matches := make([]string, 0, 2)
	for _, key := range keys {
		if key == selector || strings.HasPrefix(key, selector+"/") {
			matches = append(matches, key)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("ingest: 源 %s 不存在", selector)
	case 1:
		return matches, nil
	default:
		return nil, fmt.Errorf("ingest: 源 %s 有多个代次（%s），请用 <id>/<generation> 精确指定",
			selector, strings.Join(matches, ", "))
	}
}
