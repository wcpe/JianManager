package ingest

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// publishedEvidence 将逐字段查询结果绑定到查询时的完整逐源权威，避免拿旧代次证明新白名单。
type publishedEvidence struct {
	events      []logtypes.Event
	authorities map[string]*catalog.Record
}

func (m *Manager) publishedEvidenceClient(source SourceConfig, rec *catalog.Record) (*vlsup.Client, error) {
	if m.vlReadRoute != nil {
		return m.vlReadRoute(source, rec)
	}
	client, _, err := m.clientForSource(source)
	return client, err
}

func publishedEvidenceClosed(rec *catalog.Record, source SourceConfig) uint64 {
	if rec == nil || rec.PublishedProjection == nil || !rec.PublishedProjection.CoverageComplete {
		return 0
	}
	proj := rec.PublishedProjection
	if len(proj.SourceProjections) > 0 {
		for _, scope := range proj.SourceProjections {
			if scope.LogSourceID == source.LogSourceID && scope.SourceGeneration == source.SourceGeneration {
				return scope.ClosedVisibleSeq
			}
		}
		return 0
	}
	if len(publishedSourceProjectionGenerations(proj, source)) == 0 {
		return 0
	}
	if closed := proj.ClosedVisibleSeq[source.LogSourceID+"/"+source.SourceGeneration]; closed > 0 {
		return closed
	}
	if closed := proj.ClosedVisibleSeq[rec.Key.String()]; closed > 0 {
		return closed
	}
	return proj.ClosedVisibleSeq["default"]
}

func (m *Manager) capturePublishedEvidence(source SourceConfig, events []logtypes.Event, contexts ...context.Context) publishedEvidence {
	proof := publishedEvidence{authorities: make(map[string]*catalog.Record)}
	if m == nil || m.cat == nil {
		return proof
	}
	for _, ev := range events {
		day, err := eventUTCDay(source, ev)
		if err != nil {
			return publishedEvidence{}
		}
		if _, exists := proof.authorities[day]; !exists {
			rec, _ := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: day})
			proof.authorities[day] = rec
		}
	}
	ctx := context.Background()
	if len(contexts) > 0 && contexts[0] != nil {
		ctx = contexts[0]
	}
	proof.events = m.landedVisibleInVLCtx(ctx, source, events)
	return proof
}

type publishedPrefixQueryKey struct{}

// 只属于本次已捕获前缀；跨日、字节子片、候选探测与完整证明都消费同一物理查询计数。
type publishedPrefixQueryBudget struct {
	mu        sync.Mutex
	remaining int
}

func (budget *publishedPrefixQueryBudget) take() bool {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if budget.remaining <= 0 {
		return false
	}
	budget.remaining--
	return true
}

func (budget *publishedPrefixQueryBudget) available() int {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return budget.remaining
}

// capturePublishedPrefixEvidence只对实际候选取得一次完整RO证明；不先按memoryVerified截掉缺采尾部。
// 返回原正文与查询时逐源权威，登记方必须先重验原WAL窗口，再在短临界区复核pipe、配置和token。
func (m *Manager) capturePublishedPrefixEvidence(parent context.Context, source SourceConfig, events []logtypes.Event) (publishedEvidence, error) {
	if err := parent.Err(); err != nil {
		return publishedEvidence{}, err
	}
	if m.cat == nil || len(events) == 0 {
		return publishedEvidence{}, nil
	}
	deadline := time.Now().Add(m.verificationTimeout)
	if attempt, _ := parent.Value(verifyAttemptKey{}).(*verifyAttemptBudget); attempt != nil {
		attempt.once.Do(func() {
			if attempt.state.deadlineNanos.Load() <= 0 {
				attempt.state.deadlineNanos.Store(time.Now().Add(m.verifyBudgetOfEvents(len(events))).UnixNano())
			}
		})
		if nanos := attempt.state.deadlineNanos.Load(); nanos > 0 && time.Unix(0, nanos).Before(deadline) {
			deadline = time.Unix(0, nanos)
		}
	}
	if !time.Now().Before(deadline) {
		return publishedEvidence{}, ErrVerifyDeferred
	}
	ctx, cancel := context.WithDeadline(parent, deadline)
	defer cancel()
	budget, _ := ctx.Value(publishedPrefixQueryKey{}).(*publishedPrefixQueryBudget)
	if budget == nil {
		limit := m.verifyMaxQueriesPerChunk
		if limit <= 0 {
			limit = defaultVerifyMaxQueriesPerChunk
		}
		budget = &publishedPrefixQueryBudget{remaining: limit}
		ctx = context.WithValue(ctx, publishedPrefixQueryKey{}, budget)
	}
	canonical := make([]logtypes.Event, 0, len(events))
	for _, event := range events {
		normalized, err := normalizeCanonicalEvent(event)
		if err != nil {
			return publishedEvidence{}, err
		}
		canonical = append(canonical, normalized)
	}
	grouped, days, err := groupEventsByUTCDay(source, canonical)
	if err != nil {
		return publishedEvidence{}, err
	}
	proof := publishedEvidence{events: events, authorities: make(map[string]*catalog.Record, len(days))}
	needed := 0
	for _, day := range days {
		daySource := source
		daySource.UTCDay = day
		rec, _ := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: day})
		proof.authorities[day] = rec
		if rec == nil || rec.PublishedProjection == nil || !rec.PublishedProjection.CoverageComplete {
			return publishedEvidence{}, nil
		}
		generations := publishedSourceProjectionGenerations(rec.PublishedProjection, daySource)
		if len(generations) == 0 {
			return publishedEvidence{}, nil
		}
		for _, event := range grouped[day] {
			if event.Record.End > publishedEvidenceClosed(rec, daySource) {
				return publishedEvidence{}, nil
			}
		}
		spans, planErr := planScopedVerifySpans(daySource, generations, grouped[day])
		if planErr != nil {
			return publishedEvidence{}, planErr
		}
		needed += len(spans)
	}
	if needed > budget.available() {
		return publishedEvidence{}, ErrVerifyDeferred
	}
	for _, day := range days {
		daySource := source
		daySource.UTCDay = day
		rec := proof.authorities[day]
		client, routeErr := m.publishedEvidenceClient(daySource, rec)
		if routeErr != nil || client == nil {
			return publishedEvidence{}, fmt.Errorf("ingest: 回收取证目标不可读: %w", routeErr)
		}
		generations := publishedSourceProjectionGenerations(rec.PublishedProjection, daySource)
		if _, err := m.verifyProjectionChunk(ctx, client, daySource, generations[0], grouped[day], nil, generations); err != nil {
			return publishedEvidence{}, err
		}
	}
	return proof, nil
}

// allPublishedEventsCurrent 是ACK的全批门：任一缺失即拒绝，不为取得部分证据逐ID发起数百次查询。
func (m *Manager) allPublishedEventsCurrent(ctx context.Context, source SourceConfig, events []logtypes.Event) (bool, error) {
	if m == nil || m.cat == nil || len(events) == 0 {
		return false, nil
	}
	grouped, days, err := groupEventsByUTCDay(source, events)
	if err != nil {
		return false, err
	}
	proof := publishedEvidence{events: events, authorities: make(map[string]*catalog.Record, len(days))}
	for _, day := range days {
		daySource := source
		daySource.UTCDay = day
		rec, _ := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: day})
		proof.authorities[day] = rec
		if rec == nil || rec.PublishedProjection == nil || !rec.PublishedProjection.CoverageComplete {
			return false, nil
		}
		generations := publishedSourceProjectionGenerations(rec.PublishedProjection, daySource)
		closed := publishedEvidenceClosed(rec, daySource)
		if len(generations) == 0 {
			return false, nil
		}
		for _, event := range grouped[day] {
			if event.Record.End > closed {
				return false, nil
			}
		}
		client, err := m.publishedEvidenceClient(daySource, rec)
		if err != nil || client == nil {
			return false, fmt.Errorf("ingest: ACK取证目标不可读: %w", err)
		}
		complete, _, err := m.verifyProjectionQuery(ctx, client, daySource, generations[0], grouped[day], nil, generations)
		if err != nil || !complete {
			return false, err
		}
	}
	if err := m.applyPublishedEvidence(source, proof, func([]ledger.PositionRange) error { return nil }); err != nil {
		return false, err
	}
	return true, nil
}

// hasResidualUnpublishedDebt 回答「除本批已写入并校验的这些事件外，是否还有未获发布证据的持久正文」。
//
// 为什么需要它（2026-10-22 生产 r5 直接阻塞）：`PublicationPending` 的含义是「上次发布未获确认」，
// 而尾采快路径的写法是 `KeepPending = pendingTail`——只要进来时 pending 为真，
// 本次真实写入 + 逐字段校验 + Catalog 发布**全部成功之后仍然把 pending 写回 true**。
// 于是稳态源一旦置过 pending 就再也清不掉：Catalog 里 PP 完整、封闭水位与 Durable 相等、
// WAL 与 SQLite 均零残留，唯独 `publication_pending=true`，闸门持续报 not_ready。
//
// 判据必须按「是否真有欠账」而不是「进来时是否 pending」：本函数逐条扫有界 WAL，
// 把任何「已持久但未落在其所属日已发布封闭前缀 + 已验证区间之内」的正文视为残留责任。
// 有任何残留 ⇒ 保留 pending（保守方向）；全部已被发布证据覆盖 ⇒ 允许本次成功清掉 pending。
func (m *Manager) hasResidualUnpublishedDebt(ctx context.Context, source SourceConfig, written []logtypes.Event) (bool, error) {
	key := source.LogSourceID + "/" + source.SourceGeneration
	m.mu.Lock()
	p := m.pipes[key]
	m.mu.Unlock()
	if p == nil {
		return false, nil
	}
	writtenRanges := gapRangesOfEvents(written)
	verified := m.verifiedRuns(key)
	closedByDay := make(map[string]uint64)
	completeByDay := make(map[string]bool)
	for _, entry := range p.WAL().Snapshot() {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if !entry.Durable {
			continue
		}
		if ledger.CoveredByPositionRanges(writtenRanges, entry.Event.Record.Start, entry.Event.Record.End) {
			continue
		}
		day, err := eventUTCDay(source, entry.Event)
		if err != nil {
			return false, err
		}
		if _, seen := closedByDay[day]; !seen {
			rec, _ := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: day})
			daySource := source
			daySource.UTCDay = day
			closedByDay[day] = publishedEvidenceClosed(rec, daySource)
			completeByDay[day] = rec != nil && rec.PublishedProjection != nil && rec.PublishedProjection.CoverageComplete
		}
		if !completeByDay[day] {
			return true, nil
		}
		if entry.Event.Record.End > closedByDay[day] ||
			!ledger.CoveredByPositionRanges(verified, entry.Event.Record.Start, entry.Event.Record.End) {
			return true, nil
		}
	}
	return false, nil
}

// hasKnownUnpublishedDayDebt 只在首次、慢支或已知欠账源检查有界WAL；正常已确认尾部不做全段遍历。
// 检查按事件UTC日隔离：其他日的未交付正文不阻塞本日，但同日已知洞不能由新尾maxEnd越过。
//
// **返回被卡住的那条记录**（2026-11-02 真机缺陷修复）：只回答 `true` 而不告诉调用方「是谁卡住的」，
// 调用方就无法为它补登记可见性证据 —— 那正是「同一批旧记录被反复判欠账、却永远拿不到自己的
// verified」的成因（详见 verifyProjectionCtxYieldRuns 里那段说明）。第二个返回值为 nil 表示无欠账。
func (m *Manager) hasKnownUnpublishedDayDebt(ctx context.Context, source SourceConfig, events []logtypes.Event, observed *catalog.Record, checkWAL bool) (bool, *logtypes.Event, error) {
	key := source.LogSourceID + "/" + source.SourceGeneration
	m.mu.Lock()
	p := m.pipes[key]
	_, retrying := m.durableDeliveryRetry[key]
	m.mu.Unlock()
	closed := publishedEvidenceClosed(observed, source)
	if p == nil || (!checkWAL && !retrying && closed > 0) {
		return false, nil, nil
	}
	var candidate uint64
	for _, event := range events {
		candidate = max(candidate, event.Record.End)
	}
	written := gapRangesOfEvents(events)
	verified := m.verifiedRuns(key)
	for _, entry := range p.WAL().Snapshot() {
		if err := ctx.Err(); err != nil {
			return false, nil, err
		}
		if !entry.Durable || entry.Event.Record.End > candidate {
			continue
		}
		day, err := eventUTCDay(source, entry.Event)
		if err != nil {
			return false, nil, err
		}
		if day != source.UTCDay || ledger.CoveredByPositionRanges(written, entry.Event.Record.Start, entry.Event.Record.End) {
			continue
		}
		if entry.Event.Record.End <= closed && ledger.CoveredByPositionRanges(verified, entry.Event.Record.Start, entry.Event.Record.End) {
			continue
		}
		blocking := entry.Event
		return true, &blocking, nil
	}
	return false, nil, nil
}

// applyPublishedEvidence 的短临界区只复核权威与修改账本，不跨网络或扫盘。
func (m *Manager) applyPublishedEvidence(source SourceConfig, proof publishedEvidence, apply func([]ledger.PositionRange) error) error {
	if len(proof.events) == 0 {
		return nil
	}
	m.publishMu.Lock()
	defer m.publishMu.Unlock()
	for day, observed := range proof.authorities {
		current, _ := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: day})
		if !sameCatalogAuthority(observed, current) || sourcePublicationToken(observed, source) != sourcePublicationToken(current, source) {
			return errPublicationStale
		}
	}
	return apply(gapRangesOfEvents(proof.events))
}

// currentPublishedRuns 只返回当前权威中逐字段查得到且属于既有验证区间的精确证据。
func (m *Manager) currentPublishedRuns(ctx context.Context, source SourceConfig, saved persistedSource) (publishedEvidence, error) {
	if err := ctx.Err(); err != nil {
		return publishedEvidence{}, err
	}
	key := source.LogSourceID + "/" + source.SourceGeneration
	events, err := m.canonicalRecoveryEvents(key, saved)
	if err != nil {
		return publishedEvidence{}, fmt.Errorf("ingest: 缺口取权威正文失败: %w", err)
	}
	runs := m.verifiedRuns(key)
	candidates := make([]logtypes.Event, 0, len(events))
	for _, ev := range events {
		if err := ctx.Err(); err != nil {
			return publishedEvidence{}, err
		}
		if ledger.CoveredByPositionRanges(runs, ev.Record.Start, ev.Record.End) {
			candidates = append(candidates, ev)
		}
	}
	return m.capturePublishedEvidence(source, candidates, ctx), nil
}
