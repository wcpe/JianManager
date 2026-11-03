// 本文件是**投影写入**面：writeProjection{,Plan,Day} 这条「重建/补写」路径，
// 以及为它服务的去重与事件段落盘（appendEvents / missingFromEventStore /
// migrateInlineEvents）。
//
// 注意：这条路径与 runtime_delivery_pipeline.go 的投递路径**共享底层**
// （appendEvents、insertInBatches），但语义契约不同（投递是「写进去就算」，
// 重建是「写进去还要逐字段校验并发布」）。改动其中一侧时必须检查另一侧。
package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/archive"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// earlierRepairAllowedLocked仅接受同owner中、对原hint之前已完整RO确证缺失的有界能力；调用方已持m.mu。
func (m *Manager) earlierRepairAllowedLocked(key string, permit *earlierWrittenRepair) bool {
	if permit == nil || permit.parent == nil || permit.parent.parent != nil || m.writtenAttempts[key] != permit.parent || permit.parent.earlier != permit.previous || m.pipes[key] != permit.parent.pipe || permit.parent.sourceToken != permit.sourceToken || permit.count < 1 || permit.count > 500 || permit.to > permit.parent.from {
		return false
	}
	return permit.previous == nil || permit.previous.parent == permit.parent && permit.previous.measuredMissing
}

// repairEarlierWritten复用原预算写入/校验/发布链；父hint仍保留，未知修复响应只挂一份前置元数据回执。
func (m *Manager) repairEarlierWritten(ctx context.Context, source SourceConfig, parent, previous *writtenAttempt, events []logtypes.Event, capture *acquire.DeliveryCapture) error {
	if err := writtenProofContextErr(ctx); err != nil {
		return err
	}
	work, _ := ctx.Value(earlierWrittenRepairWorkerKey{}).(*earlierWrittenRepairWorkBudget)
	if work == nil || work.days >= m.replayBudget.MaxDays || time.Since(work.started) >= m.replayBudget.MaxDuration {
		return ErrVerifyDeferred
	}
	if m.resourceBudget != nil {
		if err := m.resourceBudget.Admit(vlsup.WorkBackground); err != nil {
			return err
		}
	}
	work.days++
	from, to, digest, err := writtenAttemptFingerprint(events)
	if err != nil || len(events) == 0 || len(events) > 500 || to > parent.from {
		return errPublicationStale
	}
	permit := &earlierWrittenRepair{parent: parent, previous: previous, from: from, to: to, count: len(events), digest: digest, sourceToken: parent.sourceToken}
	key := source.LogSourceID + "/" + source.SourceGeneration
	m.mu.Lock()
	valid := m.earlierRepairAllowedLocked(key, permit)
	saved := m.state.Sources[key]
	ownerSource := m.sources[key]
	m.mu.Unlock()
	if !valid {
		return errPublicationStale
	}
	ctx = context.WithValue(acquire.WithDeliveryCapture(ctx, capture), earlierWrittenRepairKey{}, permit)
	generation, reservation := m.reserveProjectionGeneration(source, saved.ProjectionGeneration, "earlier_missing", ctx)
	defer m.releaseProjectionGeneration(reservation)
	stored, _ := m.cachedStoredRows(key)
	snapshot := m.capturePublicationSnapshot(source, ctx)
	snapshot.earlierParent = parent
	result, written, err := m.writeProjectionPlan(source, events, generation, projectionWritePlan{Write: events, Days: []string{source.UTCDay}, KeepPending: true, Tail: &deliveryTailPlan{Events: events, StoredRows: stored}, WrittenRepair: previous, Snapshot: snapshot, Yield: m.rebuildYieldFn()})
	if err != nil {
		return err
	}
	m.mu.Lock()
	child := parent.earlier
	m.mu.Unlock()
	if child == nil || child.generation != generation || !child.allWritten || len(written) != len(events) {
		return errPublicationStale
	}
	release, err := parent.pipe.WAL().AcquireCapturedWindow(written, true, nil, false, child.capture)
	if err != nil {
		return err
	}
	defer release()
	current, _ := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay})
	if !sameCatalogAuthority(child.authority, current) || child.sourceToken != sourcePublicationToken(current, source) {
		return errPublicationStale
	}
	proof := publishedEvidence{events: written, authorities: map[string]*catalog.Record{source.UTCDay: current}}
	if err := m.applyPublishedEvidence(source, proof, func([]ledger.PositionRange) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		if err := writtenProofContextErr(ctx); err != nil {
			return err
		}
		if m.pipes[key] != parent.pipe || m.writtenAttempts[key] != parent || parent.earlier != child || !sameSourceConfig(ownerSource, m.sources[key]) {
			return errPublicationStale
		}
		return parent.pipe.RecordDeliveryResult(acquire.WithDeliveryCapture(ctx, child.capture), written, result)
	}); err != nil {
		return err
	}
	m.clearWrittenAttempt(key, child)
	m.scheduleConfirmedReplay(key)
	m.requestPersist()
	m.rollingReclaimRepair(parent.pipe, source, ctx)
	return nil
}

// repairWrittenEarlierDebt只在已有重建worker内评估前置窗口；有效完整响应以外的任何失败都不算missing。
func (m *Manager) repairWrittenEarlierDebt(ctx context.Context, source SourceConfig, hint *writtenAttempt) error {
	events, capture, err := m.earlierWrittenCandidates(ctx, hint)
	if err != nil || len(events) == 0 {
		if err == nil {
			err = ErrVerifyDeferred
		}
		return err
	}
	key := source.LogSourceID + "/" + source.SourceGeneration
	current, _ := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay})
	if !sameCatalogAuthority(hint.authority, current) || hint.sourceToken != sourcePublicationToken(current, source) {
		return errPublicationStale
	}
	var generations []string
	if current != nil {
		generations = publishedSourceProjectionGenerations(current.PublishedProjection, source)
	}
	generations = appendUniqueGeneration(generations, hint.generation)
	client, err := m.publishedEvidenceClient(source, current)
	if err != nil || client == nil || len(generations) == 0 {
		return ErrVerifyDeferred
	}
	evidence := &scopedVerifyEvidence{seen: make(map[string]struct{})}
	proofCtx := context.WithValue(ctx, scopedVerifyEvidenceKey{}, evidence)
	_, _, err = m.verifyProjectionQuery(proofCtx, client, source, generations[0], events, nil, generations)
	if err != nil {
		return err
	}
	if err := writtenProofContextErr(ctx); err != nil {
		return err
	}
	// 原窗口与原权威在线程内复核后才授有界repair能力；从不逐ID补探或改父hint.measuredMissing。
	release, err := hint.pipe.WAL().AcquireCapturedWindow(events, true, nil, false, capture)
	if err != nil {
		return err
	}
	currentAgain, _ := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay})
	m.mu.Lock()
	valid := m.pipes[key] == hint.pipe && m.writtenAttempts[key] == hint && hint.earlier == nil
	m.mu.Unlock()
	stable := sameCatalogAuthority(current, currentAgain) && sourcePublicationToken(current, source) == sourcePublicationToken(currentAgain, source)
	evidence.mu.Lock()
	present := len(evidence.seen)
	evidence.mu.Unlock()
	release()
	if !valid || !stable {
		return errPublicationStale
	}
	if present != 0 {
		return ErrVerifyDeferred
	}
	return m.repairEarlierWritten(ctx, source, hint, nil, events, capture)
}

func (m *Manager) confirmWrittenAttempt(ctx context.Context, source SourceConfig, incoming []logtypes.Event) (bool, pipeline.DeliveryResult, error) {
	if !m.deferredDelivery {
		return false, pipeline.DeliveryResult{}, nil
	}
	key := source.LogSourceID + "/" + source.SourceGeneration
	m.mu.Lock()
	hint := m.activeWrittenAttemptLocked(key)
	pipe := m.pipes[key]
	m.mu.Unlock()
	if hint == nil {
		return false, pipeline.DeliveryResult{}, nil
	}
	daySource := source
	daySource.UTCDay = hint.day
	fail := func(err error) (bool, pipeline.DeliveryResult, error) {
		if ctx.Err() != nil {
			return true, pipeline.DeliveryResult{}, ctx.Err()
		}
		if !verifyErrorIsSemantic(err) && !vlsup.IsPermanent(err) {
			m.scheduleRebuildConfirmPendingAsync(source, []string{hint.day}, "真实written窗口只读确认未完成")
		}
		return true, pipeline.DeliveryResult{}, err
	}
	current, _ := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: hint.day})
	if pipe != hint.pipe || !sameCatalogAuthority(hint.authority, current) || hint.sourceToken != sourcePublicationToken(current, daySource) {
		m.clearWrittenAttempt(key, hint)
		return fail(errPublicationStale)
	}
	events, ids, err := m.snapshotWrittenAttempt(ctx, daySource, hint)
	if err != nil {
		m.clearWrittenAttempt(key, hint)
		return fail(err)
	}
	if err := m.persistCommittedBeforeDelivery(ctx, key, pipe, ids); err != nil {
		return fail(err)
	}
	if hint.parent != nil {
		ctx = m.writtenQueryContext(ctx, true)
	}
	if positions, ok := pipe.Ledger().Positions(pipe.Key()); hint.parent == nil && ok && positions.Reclaim < hint.from {
		ctx = m.writtenQueryContext(ctx, true)
		progressed, earlierErr := m.confirmPublishedEarlier(ctx, daySource, hint)
		if earlierErr != nil {
			return fail(earlierErr)
		}
		if progressed {
			m.scheduleConfirmedReplay(key)
			return true, pipeline.DeliveryResult{}, ErrVerifyDeferred
		}
	}
	if hint.parent != nil && hint.measuredMissing {
		if work, _ := ctx.Value(earlierWrittenRepairWorkerKey{}).(*earlierWrittenRepairWorkBudget); work != nil {
			err := m.repairEarlierWritten(ctx, daySource, hint.parent, hint, events, hint.capture)
			if err != nil {
				return fail(err)
			}
			return true, pipeline.DeliveryResult{}, ErrVerifyDeferred
		}
	}
	if hint.measuredMissing {
		m.scheduleRebuildAsync(source, []string{hint.day}, "真实written窗口实测缺失")
		return true, pipeline.DeliveryResult{}, ErrVerifyDeferred
	}
	ctx = acquire.WithDeliveryCapture(ctx, hint.capture)
	snapshot := m.capturePublicationSnapshot(source, ctx)
	snapshot.earlierParent = hint.parent
	plan := projectionWritePlan{Write: events, Days: []string{hint.day}, Replace: hint.replace, Written: hint, Snapshot: snapshot, KeepPending: hint.keepPending}
	if hint.tailPlanned {
		plan.Tail = &deliveryTailPlan{Events: events}
	}
	result, written, err := m.writeProjectionPlan(source, events, hint.generation, plan)
	if err != nil {
		if errors.Is(err, errKnownUnpublishedDayDebt) && hint.parent == nil {
			if work, _ := ctx.Value(earlierWrittenRepairWorkerKey{}).(*earlierWrittenRepairWorkBudget); work != nil {
				if repairErr := m.repairWrittenEarlierDebt(ctx, daySource, hint); repairErr != nil {
					return fail(repairErr)
				}
				return true, pipeline.DeliveryResult{}, ErrVerifyDeferred
			}
			m.scheduleRebuildAsync(source, []string{hint.day}, "真实written可见但前置责任待修复")
			return true, pipeline.DeliveryResult{}, ErrVerifyDeferred
		}
		if errors.Is(err, errWrittenMissingMeasured) {
			copyHint := *hint
			copyHint.measuredMissing = true
			m.mu.Lock()
			if m.writtenAttempts[key] == hint {
				m.writtenAttempts[key] = &copyHint
			} else if hint.parent != nil && m.writtenAttempts[key] == hint.parent && hint.parent.earlier == hint {
				hint.parent.earlier = &copyHint
			}
			m.mu.Unlock()
			m.scheduleRebuildAsync(source, []string{hint.day}, "真实written窗口实测缺失")
			return true, pipeline.DeliveryResult{}, ErrVerifyDeferred
		}
		if errors.Is(err, errPublicationStale) || errors.Is(err, acquire.ErrWALWindowChanged) {
			m.clearWrittenAttempt(key, hint)
		}
		return fail(err)
	}
	m.clearWrittenAttempt(key, hint)
	m.scheduleGapResolve(source, written)
	_, _, incomingDigest, incomingErr := writtenAttemptFingerprint(incoming)
	active := events[:len(ids)]
	_, _, activeDigest, activeErr := writtenAttemptFingerprint(active)
	if len(incoming) > 0 && incomingErr == nil && (len(incoming) == hint.count && incomingDigest == hint.digest || activeErr == nil && len(incoming) == len(active) && incomingDigest == activeDigest) {
		result.HTTPStatus = hint.status
		result.AckLost = !hint.allWritten || os.Getenv("JIANMANAGER_LOG_INJECT_ACK_LOSS") == "1"
		return true, result, nil
	}
	// 确认的是旧窗口，不能把它的成功借给调用方的另一批；只为原身份登记真实响应。
	if err := pipe.RecordDeliveryResult(ctx, active, pipeline.DeliveryResult{HTTPStatus: hint.status, AckLost: !hint.allWritten || os.Getenv("JIANMANAGER_LOG_INJECT_ACK_LOSS") == "1"}); err != nil {
		return true, pipeline.DeliveryResult{}, err
	}
	m.scheduleConfirmedReplay(key)
	if err := m.releaseRecovery(source, written, ctx); err != nil {
		return true, pipeline.DeliveryResult{}, err
	}
	if len(incoming) == 0 {
		return true, result, nil
	}
	return true, pipeline.DeliveryResult{}, ErrVerifyDeferred
}

// 部分插入/响应未知只有完成有效全字段查询后才可称实测missing；错误、限流、预算耗尽均保持未知。
// projectionWritePlan 描述一次投影写入的「写入面」与「发布语义」（FR-497 spec §2.2）。
//
// 为什么要拆开：重启恢复原先只有「整窗重发」一种形态——Replace=true 时把写入面**扩为
// 权威全量**。启动增量对账需要「只写缺失天，但发布语义仍是取代」（新代包含该天全量 →
// 取代安全），于是写入面与发布语义必须能分别指定。
type projectionWritePlan struct {
	// Write 是本次实际写入 VL 的事件集合。
	Write []logtypes.Event
	// Archive 是同时落归档的权威事件集合（现状恢复路径为 nil）。
	Archive []logtypes.Event
	// Replace 为 true 表示发布语义是「新代取代旧代白名单」（该天）；false 表示累积微代次。
	Replace bool
	// 正常尾采追加不得清掉较早历史窗口的待发布责任。
	KeepPending bool
	// 正常必要压缩与失败重建分别计copies，但共享持久source/day总字节硬顶。
	MaterializationClass string
	// Days 非空表示**限定写入面**为这些 UTC 天（增量补天）：此时 Replace 不再把写入面扩为
	// 权威全量，且 Days 必须与 Write 的天集合完全一致——错位会「标记为已发布却没写数据」，
	// 故按硬失败处理（宁可启动失败，不接受静默丢数据）。
	Days []string
	// Tail 非 nil 表示本次是「权威集合的增量尾部」：段存储按增量追加、发布水位走缓存，
	// 不需要（也没有）权威全量（判据与安全论证见 deliveryTailPlan）。
	Tail *deliveryTailPlan
	// Written仅允许从原WAL重新捕获的实际written窗口走只读确认，不能成为新的插入面。
	Written *writtenAttempt
	// 仅既有预算worker对同一实测missing窗口可携此能力；普通重放/其他日不能覆盖未知hint。
	WrittenRepair *writtenAttempt
	// Snapshot 必须在读取 canonical 集合之前取得，跨源更新不使它失效。
	Snapshot *publicationSnapshot
	// Yield 非 nil 时是**批间让出**钩子：每完成一个插入批、以及每个校验簇开始之前调用一次。
	//
	// 为什么放在计划里而不是全局开关：让出是**调用方**的节奏选择，同一份写入代码在采集快路径
	// （走 `deliver`，不该被额外 sleep 拖慢）与重建慢路径（必须给采集让路）上要有不同行为。
	// 生产采集路径恒为 nil（零开销）；只有重建调度器会填它。
	//
	// **安全边界**：让出只是"插入批与批之间、校验簇与簇之间"的主动退让，**不改变**任何写入面、
	// 校验判据、发布判据与错误处理——即它无法把失败变成成功，也无法把成功变成失败。
	Yield func()
}

// publicationSnapshot 绑定源/日的发布版本，与物理代次尝试号独立。
type publicationSnapshot struct {
	epochs               map[string]uint64
	ctx                  context.Context
	materializationClass string
	checkPendingWAL      bool
	written              *writtenAttempt
	keepPending          bool
	earlierParent        *writtenAttempt
}

var errPublicationStale = errors.New("ingest: 本源发布快照已失效，需重新规划")

func (m *Manager) capturePublicationSnapshot(source SourceConfig, contexts ...context.Context) *publicationSnapshot {
	m.publishMu.Lock()
	defer m.publishMu.Unlock()
	// 关停 Flush 发生在 lifecycleCancel 之后，正常投递保留既有独立完成语义。
	ctx := context.Background()
	if len(contexts) > 0 {
		ctx = contexts[0]
	}
	if ctx == nil {
		ctx = context.Background()
	}
	out := &publicationSnapshot{epochs: map[string]uint64{}, ctx: ctx}
	for day, epoch := range m.publicationEpochs[source.LogSourceID+"/"+source.SourceGeneration] {
		out.epochs[day] = epoch
	}
	return out
}

// 此检查只比较同源同日版本，禁止把其他源正常发布当成冲突。
func (m *Manager) publicationSnapshotCurrentLocked(source SourceConfig, snapshot *publicationSnapshot, days []string) bool {
	if snapshot == nil {
		return true
	}
	current := m.publicationEpochs[source.LogSourceID+"/"+source.SourceGeneration]
	if days == nil {
		for day, epoch := range current {
			if snapshot.epochs[day] != epoch {
				return false
			}
		}
		for day, epoch := range snapshot.epochs {
			if current[day] != epoch {
				return false
			}
		}
		return true
	}
	for _, day := range days {
		if snapshot.epochs[day] != current[day] {
			return false
		}
	}
	return true
}

// verifyProjectionChunkSize 是「发布确认快路径」的分批规模：与既有投影校验的批形一致（2000/批），
// 既避免单次查询过大，也避免一次性把整份证据塞进单条查询。
const verifyProjectionChunkSize = 2000

// tryConfirmPendingPublication 在「上次发布未获确认（PublicationPending）且本次不是存量重放」时，
// 先按**最近已发布代次**（saved.ProjectionGeneration）在查询面逐字段复核本源当前事件是否已完整可见：
//   - 全部批次 complete ⇒ 发布事实上已完成 ⇒ 清 pending（持久化，同 L4019 终态）＋记 republishSkipped
//     ⇒ (true, nil) —— 调用方据此跳过整窗重发（并放行快路径）。
//   - 任一批不 complete / 查询出错 / 取证不稳 / 空集 ⇒ (false, nil)，**维持整窗重发原语义**（判据不放宽 ✗）。
//
// 成本：仅在 pending 且非重放时触发（确认后 pending 即被清）；O(段行数) 的取证与写路径同源
// （canonicalRecoveryEvents），它换掉的是**每一批都整窗重写**的代价 ✓。
// 语义安全：pending 的真义是"上次发布未获确认"，而这里用查询面拿到了**已确认可见**的证据；
// 清 pending 的终态与「写成功后清 pending」完全一致，只是证据来源不同（查询面 vs 写入面）✓。
func (m *Manager) tryConfirmPendingPublication(source SourceConfig, key string, saved persistedSource) (bool, error) {
	return m.tryConfirmPendingPublicationCtx(context.Background(), source, key, saved, nil)
}

// tryConfirmPendingPublicationCtx 是面向旧调用点的两态包装（仅回答「是否已确认完整可见」）。
//
// **新代码请用 `tryConfirmPendingPublicationVerdict`**：两态会把「取证不稳定」与
// 「无需确认」混成同一个 false，从而丢掉「已测出缺失天」这一可信结论 —— 那正是
// 重建饥饿的成因。此处保留仅为兼容既有调用点，`missingDays` 出参仍如实回传。
func (m *Manager) tryConfirmPendingPublicationCtx(ctx context.Context, source SourceConfig, key string, saved persistedSource, receipt *recoveryReceipt, missingDays ...*[]string) (bool, error) {
	var out *[]string
	if len(missingDays) > 0 {
		out = missingDays[0]
	}
	verdict, err := m.tryConfirmPendingPublicationVerdict(ctx, source, key, saved, receipt, out)
	return verdict == confirmConfirmed, err
}

// tryConfirmPendingPublicationCtx 返回**三态结论**（见 confirmVerdict 的注释）：
//   - confirmConfirmed：VL 已完整可见，pending 已清；
//   - confirmMissingMeasured：取证完成，`missingDays` 出参是可信的实测缺失天；
//   - confirmInconclusive：取证没跑完，调用方**不得**据此写任何天。
//
// 为什么必须三态而不是 (bool, error)：三态区分的是「证据不可用」与「已测出缺失」，
// 而旧的两态把它们混成同一个 false —— 上层因此退回登记全量、按升序每轮重写最老一天，
// 形成永久饥饿（详见 confirmVerdict 的注释）。
func (m *Manager) tryConfirmPendingPublicationVerdict(ctx context.Context, source SourceConfig, key string, saved persistedSource, receipt *recoveryReceipt, missingDays *[]string) (confirmVerdict, error) {
	if !saved.PublicationPending {
		// 本就没有待确认的发布：不是「不可用」，而是无需确认。
		return confirmConfirmed, nil
	}
	ctx, release, guardErr := m.acquireSourceDelivery(ctx, source)
	if guardErr != nil {
		return confirmInconclusive, guardErr
	}
	defer release()
	snapshot := m.capturePublicationSnapshot(source, ctx)
	m.mu.Lock()
	attempt := m.publicationAttempts[key]
	inflight := m.publicationInflight[key]
	m.mu.Unlock()
	if inflight != 0 {
		// 有在飞写入：此刻取证不可信（可能读到写一半的中间态）。
		return confirmInconclusive, nil
	}
	// 取证与写路径同源：canonicalRecoveryEvents 同时覆盖「段已落」与「内联旧格式」两条路。
	// 成本 O(段行数) 只在 pending 且非重放时付一次（写完/确认后 pending 即被清），
	// 而它换掉的是**每一批都整窗重写**的代价（量级远大于此）✓。
	want, werr := m.canonicalRecoveryEventsReusing(key, saved, receipt)
	if werr != nil || len(want) == 0 {
		return confirmInconclusive, nil
	}
	// 取证集合已在手：留给同一轮的写入面构建复用（判据与安全性见 recoveryReceipt）。
	// 放在这里（而非函数出口）是因为下面的早退分支都会丢掉 want，届时复用本就无意义。
	// receipt 已由同一不可变段快照构建，不能事后重取 Count 冒充原快照版本。
	// 必须**按事件归属的 UTC 日**分组校验：校验窗口由「源.UTCDay × 事件时间」共同决定，
	// 若沿用源的当天日期而事件属于历史日，窗口会成像 start>end 的倒置区间 ⇒ 恒 0 行 ⇒ 误判不完整 ✗
	//（本次开发中真实踩到，故此处显式按日拆分，与 reconcile 用例设 verifySource.UTCDay 同法）。
	grouped, _, gerr := groupEventsByUTCDay(source, want)
	if gerr != nil || len(grouped) == 0 {
		return confirmInconclusive, nil
	}
	missing := []string{}
	observed := map[string]*catalog.Record{}
	for day, dayEvents := range grouped {
		if err := ctx.Err(); err != nil {
			return confirmInconclusive, err
		}
		vs := source
		vs.UTCDay = day
		rec, _ := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: day})
		observed[day] = rec
		client, cerr := m.publishedEvidenceClient(vs, rec)
		if cerr != nil || client == nil {
			return confirmInconclusive, cerr
		}
		// 旧判据是一条三层链：已发布代次白名单 → `publishedScope.ClosedVisibleSeq` 水位 →
		// `countVLDay` 条数 → 逐字段比对。三层全部以「多代次重写 + 发布确认」为前提；
		// 简化后一批数据只写一次、行上也没有 `projection_generation` 标签，
		// 于是任何一层按代次过滤都只会得到 0 条 ⇒ 连**已经写成功的天**也被判缺失。
		//
		// 后果实测（TestReplaySlicesAndResumesWithoutLoss，预算 1 天/轮）：
		// 每轮都把同一个「被误判缺失」的天重写一遍，另一天永远排不上（`rows` 在涨但数据永远不齐）。
		//
		// 新判据直接回答「这批 event_id 在不在」——与缺口核对同源（`eventIDsVisibleInVL`）。
		visible, qerr := m.eventIDsVisibleInVL(ctx, client, vs, dayEvents)
		if qerr != nil {
			if verifyErrorIsSemantic(qerr) || vlsup.IsPermanent(qerr) {
				return confirmInconclusive, qerr
			}
			return confirmInconclusive, nil
		}
		if len(visible) < len(dayEvents) {
			missing = append(missing, day)
		}
	}
	if len(missing) > 0 {
		if m.deferredDelivery {
			m.publishMu.Lock()
			stable := m.publicationSnapshotCurrentLocked(source, snapshot, nil)
			for day, record := range observed {
				vs := source
				vs.UTCDay = day
				current, _ := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: day})
				stable = stable && sameCatalogAuthority(record, current) && sourcePublicationToken(record, vs) == sourcePublicationToken(current, vs)
			}
			m.publishMu.Unlock()
			if !stable {
				return confirmInconclusive, nil
			}
		}
		// **已测出缺失天 ⇒ 这是可信结论，必须如实回传**（不再被下面的稳定性校验丢弃）。
		// 旧实现在这里 `return false, nil` 后还会被三道「证据稳定性」校验拦掉，
		// 而活跃源那三道几乎必然失败 ⇒ 上层拿不到 missing ⇒ 退回登记全量 ⇒ 饥饿。
		sort.Strings(missing)
		if missingDays != nil {
			*missingDays = missing
		}
		return confirmMissingMeasured, nil
	}
	m.publishMu.Lock()
	if !m.publicationSnapshotCurrentLocked(source, snapshot, nil) {
		m.publishMu.Unlock()
		// 取证期间本源有新的发布 ⇒ 「已完整可见」这个结论的时点已失效。
		// 注意：此时 missing 为空，但**并未证明所有天都可见**（证据是过期快照）⇒ 不可据此写。
		return confirmInconclusive, nil
	}
	for day, record := range observed {
		vs := source
		vs.UTCDay = day
		current, _ := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: day})
		if !sameCatalogAuthority(record, current) || sourcePublicationToken(record, vs) != sourcePublicationToken(current, vs) {
			m.publishMu.Unlock()
			return confirmInconclusive, nil
		}
	}
	m.mu.Lock()
	cur := m.state.Sources[key]
	if m.publicationAttempts[key] != attempt || m.publicationInflight[key] != 0 || cur.ProjectionGeneration != saved.ProjectionGeneration {
		m.mu.Unlock()
		m.publishMu.Unlock()
		return confirmInconclusive, nil
	}
	cur.PublicationPending = false
	m.state.Sources[key] = cur
	m.mu.Unlock()
	m.publishMu.Unlock()
	m.republishSkipped.Add(1)
	// 实战可核验（#5 复盘缺口）：跳过整窗重发必须有日志，否则外部只见"pending 消了"而无从判是否真的走了快路径。
	slog.Info("发布确认快路径：VL 已完整，跳过整窗重发",
		"source", source.LogSourceID, "generation", saved.ProjectionGeneration, "events", len(want))
	// 持久化：内存已清、落库未跟上时，下次启动仍会整窗重发（安全方向 ✓），故持久化失败不影响本次放行。
	_ = m.persistHot()
	return confirmConfirmed, nil
}

// RepublishSkipped 返回「发布确认快路径」累计跳过整窗重发的次数（观测用，单位=次）。
func (m *Manager) RepublishSkipped() int64 { return m.republishSkipped.Load() }

func (m *Manager) writeProjection(source SourceConfig, events []logtypes.Event, generation string, writeEvents, archiveEvents []logtypes.Event, replace bool) (pipeline.DeliveryResult, error) {
	res, _, err := m.writeProjectionPlan(source, events, generation, projectionWritePlan{
		Write: writeEvents, Archive: archiveEvents, Replace: replace,
	})
	return res, err
}

// writeProjectionPlan 是 writeProjection 的完整形态：按计划写入 VL、校验、发布、落段。
//
// events 恒为权威全量（用于段存储补齐与 persist）；实际写入面由 plan 决定。
// writeProjectionPlan 返回（投递结果, **真正写入并逐字段校验通过的事件**, error）。
//
// 第三个返回值是「证据面」的唯一可靠来源：`plan.Write` 是**计划**写的，而本函数内部
// 按天循环、且会把超出热层保留期的天整段跳过 —— 计划面与落库面可能不等。调用方若用
// 计划面去消解缺口，会把从未落库的区间标成「已确认投递」（2026-10-21 真机缺陷）。
//
// 为什么不能用入参 `plan` 回填：Go 按值传参，函数内对 plan 的字段写入传不回调用方。
func (m *Manager) writeProjectionPlan(source SourceConfig, events []logtypes.Event, generation string, plan projectionWritePlan) (pipeline.DeliveryResult, []logtypes.Event, error) {
	ctx := m.lifecycleContext()
	if plan.Snapshot != nil && plan.Snapshot.ctx != nil {
		ctx = plan.Snapshot.ctx
	}
	ctx, release, guardErr := m.acquireSourceDelivery(ctx, source)
	if guardErr != nil {
		return pipeline.DeliveryResult{}, nil, guardErr
	}
	defer release()
	if m.deferredDelivery && plan.Written == nil {
		key := source.LogSourceID + "/" + source.SourceGeneration
		permit, _ := ctx.Value(earlierWrittenRepairKey{}).(*earlierWrittenRepair)
		validEarlier := false
		if permit != nil && !plan.Replace && plan.KeepPending && plan.Tail != nil && len(plan.Days) == 1 && plan.Days[0] == permit.parent.day {
			from, to, digest, err := writtenAttemptFingerprint(plan.Write)
			if err == nil && from == permit.from && to == permit.to && len(plan.Write) == permit.count && digest == permit.digest {
				m.mu.Lock()
				validEarlier = m.earlierRepairAllowedLocked(key, permit)
				m.mu.Unlock()
			}
		}
		m.mu.Lock()
		hint := m.activeWrittenAttemptLocked(key)
		m.mu.Unlock()
		if hint != nil && !validEarlier && (plan.WrittenRepair != hint || !hint.measuredMissing || !plan.Replace || len(plan.Days) != 1 || plan.Days[0] != hint.day) {
			m.scheduleRebuildConfirmPendingAsync(source, []string{hint.day}, "先只读解决原written窗口，其他入口不得换代")
			return pipeline.DeliveryResult{}, nil, ErrVerifyDeferred
		}
		if hint != nil {
			vs := source
			vs.UTCDay = hint.day
			current, _ := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: hint.day})
			_, ids, captureErr := m.snapshotWrittenAttempt(ctx, vs, hint)
			if captureErr != nil || !sameCatalogAuthority(hint.authority, current) || sourcePublicationToken(current, vs) != hint.sourceToken {
				m.clearWrittenAttempt(key, hint)
				return pipeline.DeliveryResult{}, nil, errPublicationStale
			}
			if err := m.persistCommittedBeforeDelivery(ctx, key, hint.pipe, ids); err != nil {
				return pipeline.DeliveryResult{}, nil, err
			}
		}
	}
	if plan.Snapshot != nil {
		owned := *plan.Snapshot
		owned.ctx = ctx
		plan.Snapshot = &owned
	} else {
		plan.Snapshot = m.capturePublicationSnapshot(source, ctx)
	}
	if len(events) == 0 {
		return pipeline.DeliveryResult{HTTPStatus: http.StatusNoContent}, nil, nil
	}
	for _, event := range events {
		if event.Record.End <= event.Record.Start {
			return pipeline.DeliveryResult{HTTPStatus: http.StatusUnprocessableEntity}, nil, fmt.Errorf("ingest: zero-width event %s", event.EventID)
		}
	}
	key := source.LogSourceID + "/" + source.SourceGeneration
	if plan.Snapshot == nil {
		plan.Snapshot = m.capturePublicationSnapshot(source)
	}
	m.publishMu.Lock()
	if !m.publicationSnapshotCurrentLocked(source, plan.Snapshot, plan.Days) {
		m.publishMu.Unlock()
		return pipeline.DeliveryResult{}, nil, errPublicationStale
	}
	m.mu.Lock()
	if parent := plan.Snapshot.earlierParent; parent != nil {
		permit, _ := ctx.Value(earlierWrittenRepairKey{}).(*earlierWrittenRepair)
		valid := m.writtenAttempts[key] == parent && m.pipes[key] == parent.pipe && parent.pipe.OwnsDeliveryGuard(ctx)
		if plan.Written != nil {
			valid = valid && parent.earlier == plan.Written
		} else {
			valid = valid && m.earlierRepairAllowedLocked(key, permit)
		}
		if !valid {
			m.mu.Unlock()
			m.publishMu.Unlock()
			return pipeline.DeliveryResult{}, nil, errPublicationStale
		}
	}
	if m.publicationAttempts == nil {
		m.publicationAttempts = map[string]uint64{}
	}
	if m.publicationInflight == nil {
		m.publicationInflight = map[string]int{}
	}
	m.publicationAttempts[key]++
	attempt := m.publicationAttempts[key]
	m.publicationInflight[key]++
	saved := m.state.Sources[key]
	saved.ProjectionGeneration = generation
	saved.PublicationPending = true
	m.state.Sources[key] = saved
	m.mu.Unlock()
	m.publishMu.Unlock()
	defer func() { m.mu.Lock(); m.publicationInflight[key]--; m.mu.Unlock() }()
	// 该代次名从此刻起可能出现在 VL 中（写入可能只完成一部分就失败）——因此**先**把它记入
	// 探测水位（复审 P2-11）：水位必须是「本进程为该源写过/尝试写过的最大序号」，否则一个
	// 半途失败的写入之后，同名候选会被缓存判成「未写过 ⇒ 未占用」，把重名风险重新引进来。
	m.recordProjectionGenerationAttempt(key, generation)
	// The attempted generation is durable before any VL write. A lost response
	// or failed Catalog commit must rebuild into a different physical target.
	//
	// 2026-10-03 起这里是**热路径**：有界等待 + 超时交办（persistHot ✗ 不再为落库阻塞投递）。
	// 「代次不得重名」的保护不依赖这一次落库：取名前的 VL 占用探测（nextFreeProjectionGeneration）
	// 才是主判据 ✓；最坏情况（进程在落库前崩溃）只会让该代次重取一次名 → 探测拦下 ✓ 不丢数据。
	if err := m.persistHot(); err != nil {
		return pipeline.DeliveryResult{}, nil, err
	}
	grouped, days, err := groupEventsByUTCDay(source, events)
	if err != nil {
		return pipeline.DeliveryResult{}, nil, err
	}
	archiveGrouped, _, err := groupEventsByUTCDay(source, plan.Archive)
	if err != nil {
		return pipeline.DeliveryResult{}, nil, err
	}
	writeGrouped, writeDays, err := groupEventsByUTCDay(source, plan.Write)
	if err != nil {
		return pipeline.DeliveryResult{}, nil, err
	}
	switch {
	case len(plan.Days) > 0:
		// 增量补天：写入面已由调用方按天过滤；这里只校验二者一致并固定顺序。
		if err := sameUTCDays(writeDays, plan.Days); err != nil {
			return pipeline.DeliveryResult{}, nil, err
		}
		writeDays = sortUTCDays(plan.Days)
	case plan.Replace:
		// 整窗重发（现状语义）：写入面扩为权威全量。
		writeDays = days
		writeGrouped = grouped
	}
	// **热层可发布窗口过滤（2026-10-21 真机实测发现的残留）**：整窗重发会把**权威全量**的
	// 每一天都拿去写，其中超出热层 retention 的天在 VL 里**不可能可见**（VL 对超期数据点
	// 返回 200 但静默丢弃）。不在这里过滤的后果，正是部署后真机复现的形态：
	// `projection verify failed after write: not fully visible before deadline` 持续重试，
	// `PublicationPending` 因此**永远清不掉**（本函数末尾的清 pending 只在全部天写完后执行）。
	//
	// 为什么必须放在这里而不是只放在 `rebuildDaysInner`：整窗重发走的是**本函数**（deliver
	// 路径），而重建走的是 `rebuildDaysInner`，两条路径各自构造写入面 —— 只修一条等于只治
	// 一半。真机上 inst:327 在重建侧已正确跳过、却仍在 deliver 侧反复撞校验失败，就是这个洞。
	//
	// 与重建侧的语义保持一致：超窗口的天**不写、不校验、不计入已发布**，并**如实打日志**；
	// 窗口内的天照原语义推进。`splitHotRetentionDays` 在 retention 未配置时原样返回
	// （零值即不启用过滤），故对未配保留期的部署零行为变化。
	if len(writeDays) > 0 {
		inWindow, beyond := m.splitHotRetentionDays(writeDays, time.Now())
		if len(beyond) > 0 {
			kept := make(map[string][]logtypes.Event, len(inWindow))
			for _, day := range inWindow {
				kept[day] = writeGrouped[day]
			}
			writeGrouped = kept
			writeDays = inWindow
			slog.Warn("整窗重发跳过超出热层保留期的 UTC 天（不写入、不校验、不计入已发布）",
				"source", source.LogSourceID, "generation", generation,
				"beyondHotRetentionDays", beyond, "writeDays", inWindow,
				"hotRetention", m.hotRetention)
		}
	}
	result := pipeline.DeliveryResult{HTTPStatus: http.StatusNoContent}
	// **全空硬闸（2026-10-21 复审发现的回归，红线级）**：热层过滤把 `writeDays` 清空时，
	// 下面的循环一次都不跑（`written == nil`），但函数会继续走到末尾把
	// `PublicationPending` 清成 false 并**返回 nil 错误** ⇒ `deliver` 认为投递成功
	// ⇒ `acquire.Pipeline` 据此调 `RecordHTTPResult` 推进 `Delivery` 水位
	// ⇒ `releaseRecovery` 的 `positions.Delivery >= to` 闸门从此放行
	// ⇒ 为**从未写入的区间**建副本证明 ⇒ `reclaim` 越过它 ⇒ `WAL.pruneReclaimed`
	// 按 `Record.End <= Reclaim` 删条目 ⇒ **数据彻底消失，而账本写着「有副本」**。
	//
	// 为什么这不是理论风险：源暂停 ≥ retention（30d）后 `DeliverPending` 重放一个月前的
	// durable 条目时，这批旧天会被整段过滤为空 —— 那正是「本地还有、VL 已不可能收」的形态。
	// 加过滤之前该形态会去写入并**校验失败**（响亮的失败）；加了过滤却没加这道闸，
	// 就把它变成了「静默的成功」。
	//
	// 语义：这不是「无事可做」，而是「本批注定不可发布」⇒ 必须返回错误、保持 pending，
	// 让调用方**不要**推进投递水位。
	if len(writeDays) == 0 && len(events) > 0 {
		m.markPublicationPending(key, generation)
		slog.Error("整窗写入面全部超出热层保留期：本批不可发布，拒绝宣称成功（保持发布待定）",
			"source", source.LogSourceID, "generation", generation,
			"events", len(events), "hotRetention", m.hotRetention)
		return result, nil, errProjectionBeyondHotRetention
	}
	// 累积「真正写入并逐字段校验通过的事件集合」，作为第三个返回值交给调用方做证据面。
	// 只累加**成功写完的那一天**：任何一天失败都会在下面 return err，调用方拿到的 err 优先。
	var written []logtypes.Event
	daySnapshot := *plan.Snapshot
	daySnapshot.materializationClass = plan.MaterializationClass
	daySnapshot.checkPendingWAL = plan.KeepPending || plan.Tail == nil
	daySnapshot.written = plan.Written
	daySnapshot.keepPending = plan.KeepPending
	for _, day := range writeDays {
		daySource := source
		daySource.UTCDay = day
		dayResult, writeErr := m.writeProjectionDay(daySource, grouped[day], writeGrouped[day], generation, archiveGrouped[day], plan.Replace, plan.Tail != nil, plan.Yield, &daySnapshot)
		if dayResult.HTTPStatus != 0 {
			result.HTTPStatus = dayResult.HTTPStatus
		}
		if writeErr != nil {
			return result, nil, writeErr
		}
		// 该天写入 + 校验已通过 ⇒ 这批事件确实落库，可作证据。
		written = append(written, writeGrouped[day]...)
	}
	m.mu.Lock()
	saved = m.state.Sources[key]
	if m.publicationAttempts[key] == attempt {
		saved.ProjectionGeneration = generation
		saved.PublicationPending = plan.KeepPending
		m.state.Sources[key] = saved
	}
	m.mu.Unlock()
	// 事件体写入磁盘段（先内容、后引用）：写入成功并 fsync 后，再让 state 元数据引用它。
	// **契约（2026-10 变更）**：段是「投递完成前的暂存与重放来源，投递并逐字段校验通过后可回收」。
	// 原档不在这里——平台不承担原档长期保存责任，原档由用户自己在每台服务器上保留文本日志，
	// VL 只做加速索引查询。因此**不再**要求段充当「VL 数据根丢失后重建 projection 的权威集合」：
	// 被回收区间的段正文按设计不再返回（回收水位与安全论证见 eventstore_prune.go 文件头）。
	//
	// 注意「可回收」不等于「这里可以省」：追加路径本身仍然只增不删，回收只发生在独立的
	// 每日驱动器（Manager.RunEventStorePrune）里，因此在一次投递内段内容的行界依然稳定。
	//
	// 快路径（plan.Tail）下 events 就是「权威集合的增量尾部」，直接追加即可：
	// 省掉 appendEvents 的 Count（末段校准 = O(末段行数)）与全量比对。
	if plan.Tail != nil && plan.Written == nil {
		if err := m.appendEventTail(key, *plan.Tail); err != nil {
			return result, written, err
		}
	} else if err := m.appendEvents(key, events); err != nil {
		return result, written, err
	}
	// 这里**不再**单独落库一次：本批次引发的 state 变更（投影代次 / 发布待定 / 段覆盖水位）
	// 会被随后任意一次 persist 覆盖，而 persist 是「按变更源重建 + 全量根表比对」的增量落库——
	// 后一次落库总是把前一次的遗漏补齐。紧随其后的落库点有两个：同源下一批的 DurablePersist
	// （VL 插入前的契约落库）与轮末的统一 persist（pollOnce 的 dirty 分支）。
	//
	// 为什么必须去掉（FR-498 并发采集轮的连带修复）：每源每批两次落库时，并发轮里 8 个 worker
	// 会在全局串行的持久化上排长队，把「短变更」调用者（实例登记）挤到队尾。去掉后单源每批
	// 只落库一次（VL 插入前那次，契约不变），持久化总量与排队长度减半。
	//
	// 崩溃语义没有变弱：投影状态最多晚一次落库到达索引（≤1 轮），而恢复路径本来就不依赖
	// state 里的投影标记——事件段存储是权威集合（见 canonicalRecoveryEvents），代次重名由
	// VL 侧占用探测兜底（见 nextFreeProjectionGeneration）。
	return result, written, nil
}

// appendEvents 把该源的 canonical 事件集合同步到磁盘段，并把该源标记为“事件体已落段”。
//
// 入参是**完整**权威集合（与旧版 `saved.Events` 的语义一致）。函数内部只追加段里还没有的身份
// （按段内已有 identity 集合求差集），因此每轮 poll 都幂等，不会把同一事件重复写入段。
//
// 关键判据（2026-10-10 根治「代次可见性不一致」的**真根因**）：
// 段是「只增不减的权威超集」，**允许包含冗余行**（同 event_id 且同哈希的重复行，语义等价），
// 而入参 `events` 是**按身份去重后**的权威集合。于是「段物理行数」可以**合法地**大于 `len(events)`：
//   - 生产实证（inst:148）：段物理 135113 行，去重后 134981 个 event_id，130 个 id 重复
//     （129 个 2 份 + 1 个 4 份），**全部同 canonical 哈希**；全生产 158 源中 36 源含此类冗余
//     共 59556 行（单源最多 13998 行），且**零**行缺 event_id、零例「同 id 异哈希」可归因于此。
//   - 旧实现把两者当「同一条前缀的长度」直接比较（`stored > len(events)` ⇒ 硬失败），于是
//     这些纯冗余行被误判成「段超前于权威集合 ⇒ 段被外部改写/恢复集合有漏」，**永久硬失败**：
//     该源此后每次恢复/重建都在这里撞墙 ⇒ 发布永远无法完成（`PublicationPending` 钉死）⇒
//     对账判缺失 ⇒ 反复整窗重发。生产现场该错误 **952 次、涉及 45 个源**，是比「读面代次」更深一层
//     的真凶（读面白名单本身是跨代次累积的，实测 661 个「源×天」组合里**零**个不可见）。
//
// 冗余行的来源（只读实测，2026-10-10）：重复对**记录区间完全相同、canonical 哈希相同、仅
// `ingest_time_utc` 不同**（例如同一物理行在 06:37 与 07:13 各被登记一次），且 130/130 对都落在
// **同一段内**、间隔 1~243 行。证据指向「旧实现按**下标**切片追加」：这些重复对中 119/130 在
// 两个孪生行之间夹着**位置更靠前**的事件（即该区间发生过内容位移），98/130 紧邻段内的非单调点
// （全段 13 处非单调点全落在受影响的两个段内）。内容一旦位移，`events[stored:]` 就会把已落段的
// 条目再追加一遍——这正是冗余行的生成方式。
//
// 本函数（`appendEvents`）改为按身份收敛后，**本路径不会再产生新的冗余行**（只追加段中不存在的
// 身份）。注意这不等于「全库不会再有冗余行」：`appendEventTail` 走的是不做身份核对的增量追加
// 快路径（其安全性由 `planDeliveryTail` 的「本批区间在已投递水位之后」判据保证，见该函数注释），
// 且既有冗余行按设计**不清理**（段只增不减，见本文件顶部契约）。
//
// 为什么改成按身份核对**不会**把不完整数据当完整结果返回（红线）：
//   - 「段超前」这个担忧的本质是「段里有一段谁也无法解释的内容」——但这里逐条核对的正是
//     「入参的**每一条**身份是否都已在段中」：只要有一条权威事件不在段里，就照旧放行追加；
//   - 段内事件数少于权威集合时按身份差集追加（只追加缺的身份），与旧行为同向，不会少写；
//   - 因此新实现**只放宽「段比权威集合多」的良性形态**，不放宽任何「权威事件不可见」的形态。
//
// 同 event_id 异哈希的处置（**必须与既有各路径同一口径**，不得新增失败模式）：
// 段内已存变体与权威变体内容不同时，走既有的 `adjudicateSameIntervalIdentity` 两类裁定——
// 可裁定（同区间一方更长 / 同区间同行仅时间不同）⇒ 保留较长或后到者（`takeIncoming` 时把权威
// 变体追加进去，段允许两份并存，语义由查询侧按同一规则收敛）；不可裁定 ⇒ **按既有约定记录
// `identityConflictRecord` 并跳过**，绝不为此硬失败。
//
// 为什么不能在这里硬失败（2026-10-09 复核，两处既有实现已给出结论）：冲突是**确定性**的
// （同一份段 + 同一份权威集合，重试输入不变）⇒ 硬失败会让该源的投递/恢复**永续失败** ⇒
// 发布水位钉死 ⇒ 查询按水位截断（正是 `planDeliveryBatchOutsideLock` 与
// `canonicalRecoveryEvents` 在 2026-10-08 修掉的形态，二者注释都写明「不再整批失败」）。
// 历史事件体一旦是残行，其修正版必然带不同哈希——"一律硬失败"会把**正常的残行修正**也永久堵死。
// 且该变更必须 fail loud 才安全：故冲突计入 `eventStoreIdentityConflicts` 并落
// `IdentityConflicts` 隔离记录（对运维可查、对投影质量可见），本函数**不静默吞掉**它。
//
// 标记与落段在同一临界区内推进：只有段写成功（已 fsync）后 state 才会引用它。
//
// 锁序：本函数内部只在最后取一次 m.mu（与既有实现一致）；段遍历走 eventstore 自己的 Store.mu，
// 二者不嵌套持有（核对在取 m.mu **之前**完成），故不会引入新的锁序边。
func (m *Manager) appendEvents(key string, events []logtypes.Event) error {
	if m.events == nil || len(events) == 0 {
		return nil
	}
	missing, conflicts, err := m.missingFromEventStore(key, events)
	if err != nil {
		return err
	}
	// 身份冲突按既有约定留痕（隔离记录 + 计数器）：既不整批失败，也不静默吞掉。
	// 放在追加**之前**：冲突结论与是否真的追加成功无关。
	if len(conflicts) > 0 {
		m.eventStoreIdentityConflicts.Add(int64(len(conflicts)))
		m.recordIdentityConflicts(key, conflicts)
	}
	if len(missing) > 0 {
		if err := m.events.Append(key, missing); err != nil {
			return err
		}
		m.bumpEventStoreEpoch(key)
	}
	if len(conflicts) == 0 {
		m.rememberBodyReferenceProof(key, events)
	}
	w := m.watchMutexWait("appendEvents.persist")
	m.mu.Lock()
	w.acquired()
	saved := m.state.Sources[key]
	saved.EventsStored = true
	// B1a：记录段存储已覆盖到的最大 record_end。`events` 是权威集合（按记录位置有序），
	// 其最大 record_end 即段存储已保证覆盖的范围；WAL 持久化据此按条决定「引用 or 内联」。
	var covered uint64
	for _, ev := range events {
		if ev.Record.End > covered {
			covered = ev.Record.End
		}
	}
	if covered > saved.EventsStoredThrough {
		saved.EventsStoredThrough = covered
	}
	// 已落段后不再保留内联副本，否则常驻切片会重新把 RSS 推高（FR-484 目标）。
	saved.Events = nil
	m.state.Sources[key] = saved
	m.mu.Unlock()
	w.done()
	return nil
}

// missingFromEventStore 返回权威集合里**尚未落段**的事件（按入参顺序），并返回碰到的身份冲突。
//
// 逐个身份核对（这是本函数存在的全部理由）：
//   - 段内已有同 event_id **且同 canonical_content_hash** ⇒ 该条已在段中，不追加（冗余副本不影响结论）；
//   - 段内没有该 event_id ⇒ 放进待追加集合（调用方追加；追加失败向上传播，绝不静默丢弃）；
//   - 段内已有同 event_id 但**哈希不同** ⇒ 走 `adjudicateSameIntervalIdentity` 的两类裁定
//     （同区间一方更长 ⇒ 取较长者；同区间同行仅时间不同 ⇒ 取后到者）。`takeIncoming` 时把权威
//     变体照常追加（段保留两份，语义由查询侧按同一规则收敛）；`keepExisting` 与不可裁定形态
//     均以 `identityConflictRecord` 留痕并**跳过该条**（与 planDeliveryBatchOutsideLock /
//     canonicalRecoveryEvents 的 2026-10-08 处置逐字同口径：不整批硬失败）。
//
// 为什么冲突不硬失败（复核既有结论）：冲突是确定性的，硬失败会让该源投递/恢复永续失败、发布
// 水位钉死 ⇒ 查询按水位截断（正是那两处已修掉的形态）。且历史残行与其修正版必然哈希不同，
// 「一律硬失败」连正常修正都会永久堵死。留痕（IdentityConflicts 隔离记录 + 计数器）保证它
// 对运维可查、对投影质量可见，不是静默吞掉。
//
// 备忘（storedCoverage）：入参集合的**内容指纹** + 段物理读数都未变时，直接判定「无待追加」，
// 省掉逐条遍历（生产单源 126MB / 13.5 万行，实测单次解析 1.7s）。指纹取自入参本身，故对
// 「不同调用方传不同集合」天然安全（长度相同也不行 ✗）——这正是不能用长度当键的原因。
func (m *Manager) missingFromEventStore(key string, events []logtypes.Event) ([]logtypes.Event, []identityConflictRecord, error) {
	// 备忘只在**已建立**时才可能命中，故只在那时才计算集合指纹（O(集合)，实测 13.5 万条约 232ms）；
	// 从未建立过备忘的源（每轮都有新事件的常见形态）连这一次开销都不付。
	memoExists := m.hasStoredCoverageMemo(key)
	var digest [sha256.Size]byte
	if memoExists {
		digest = eventSetDigest(events)
		covered, err := m.storedCoverage(key, digest)
		if err != nil {
			return nil, nil, err
		}
		if covered {
			m.storedCoverageHits.Add(1)
			return nil, nil, nil
		}
	}
	// 段内**每个身份当前胜出的变体**：与 `canonicalRecoveryEvents` 同一折叠规则（先到者胜，
	// 可裁定取较长/更正者），故「段能重建出的集合」与本判定逐条一致。
	//
	// 只留胜者而不是「每个身份的第一条」是幂等性的关键：段的追加语义决定它可能同时持有
	// 一个身份的多个变体，若记账只认第一条，「本次追加的胜者」在下一轮又会被判成「不在段中」
	// ⇒ 每轮重复追加同一事件（段无界增长 ✗）。
	stored := make(map[string]eventIdentity, len(events))
	if err := m.events.Iterate(key, func(event logtypes.Event) error {
		if event.EventID == "" {
			// 段内无身份的条目：不可能是任何权威事件的身份，且无法据此判定冲突；
			// 保留它（不删段内内容）但让权威事件照常按自身身份入段。
			return nil
		}
		existing, ok := stored[event.EventID]
		if !ok {
			stored[event.EventID] = eventIdentity{hash: event.CanonicalHash, when: event.EventTimeUTC, start: event.Record.Start,
				end: event.Record.End, size: len(event.Message), msg: event.Message, level: event.Level,
				stream: event.Stream, idx: -1}
			return nil
		}
		if existing.hash == event.CanonicalHash {
			// 段内自身的良性冗余（生产实测：单源最多 1.4 万行同 id 同哈希）：与本次核对无关。
			return nil
		}
		// 段内同一身份已有两个不同内容：按既有裁定收敛到胜者（与 canonicalRecoveryEvents 同规则）。
		// 注意返回值顺序是 (keepExisting, takeIncoming)。
		if _, takeIncoming := adjudicateSameIntervalIdentity(existing, event, len(event.Message)); takeIncoming {
			stored[event.EventID] = eventIdentity{hash: event.CanonicalHash, when: event.EventTimeUTC, start: event.Record.Start,
				end: event.Record.End, size: len(event.Message), msg: event.Message, level: event.Level,
				stream: event.Stream, idx: -1}
		}
		return nil
	}); err != nil {
		return nil, nil, err
	}
	missing := make([]logtypes.Event, 0, len(events))
	conflicts := make([]identityConflictRecord, 0)
	seen := make(map[string]bool, len(events))
	for _, event := range events {
		if event.EventID == "" {
			// 身份缺失的事件无法去重，只能照写（沿用旧行为：绝不因缺证据而少写）。
			missing = append(missing, event)
			continue
		}
		if seen[event.EventID] {
			continue
		}
		seen[event.EventID] = true
		existing, ok := stored[event.EventID]
		if !ok {
			missing = append(missing, event) // 该身份在段中完全没有 ⇒ 必追加
			continue
		}
		if existing.hash == event.CanonicalHash {
			continue // 段中胜者与权威变体一致 ⇒ 已覆盖
		}
		// 段中已有同身份但内容不同：与既有两个路径（planDeliveryBatchOutsideLock、
		// canonicalRecoveryEvents）逐字同口径地裁定，绝不整批硬失败（理由见函数头注释）。
		keepExisting, takeIncoming := adjudicateSameIntervalIdentity(existing, event, len(event.Message))
		switch {
		case takeIncoming:
			// 权威变体是较长者/更正版 ⇒ 追加进段（段不可改写，故与段内旧变体并存；
			// 段的重建折叠会选它，语义与查询侧收敛方向一致）。下一轮它已是胜者 ⇒ 幂等。
			missing = append(missing, event)
		case keepExisting:
			// 段中胜者更完整（本次权威变体是残行）⇒ 不追加、不记冲突（既有约定：
			// 可裁定形态只告警，不算「不可裁定冲突」）。
			slog.Warn("段内身份变体更完整（权威变体较短），保留段内已有内容",
				"key", key, "event_id", event.EventID, "start", event.Record.Start, "end", event.Record.End,
				"kept_bytes", existing.size, "dropped_bytes", len(event.Message))
		default:
			// 不可裁定（异区间 / 同区间等长且非同一物理行）⇒ 按既有约定留痕并跳过。
			conflicts = append(conflicts, identityConflictRecord{
				EventID: event.EventID, Start: event.Record.Start, End: event.Record.End,
				KeptHash: existing.hash, DroppedHash: event.CanonicalHash,
				ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
			})
		}
	}
	// 段已完整覆盖本次权威集合 ⇒ 记入备忘（内容指纹 + 本次段读数）。有待追加时不记：
	// 追加会推进段读数使备忘失效，而且真有待追加时该集合并未被完整覆盖。
	if len(missing) == 0 {
		if !memoExists {
			digest = eventSetDigest(events) // 只在确实要记时才补算指纹
		}
		m.rememberStoredCoverage(key, digest)
	}
	return missing, conflicts, nil
}

// eventSetDigest 计算一个事件集合的内容指纹（身份 + 内容哈希 + 记录区间）。
//
// 用途仅是**备忘的键**：用于回答「这次问的集合与上次已核对通过的是不是同一份」。
// 用 sha256 而不是短哈希：一次假命中会让本该追加的事件被跳过（静默丢数据），
// 属红线，故不接受任何可计算的碰撞风险（本函数只在内存里遍历，不做磁盘 I/O）。
func eventSetDigest(events []logtypes.Event) [sha256.Size]byte {
	h := sha256.New()
	var buf [8]byte
	for i := range events {
		h.Write([]byte(events[i].EventID))
		h.Write([]byte{0})
		h.Write([]byte(events[i].CanonicalHash))
		h.Write([]byte{0})
		binary.LittleEndian.PutUint64(buf[:], events[i].Record.Start)
		h.Write(buf[:])
		binary.LittleEndian.PutUint64(buf[:], events[i].Record.End)
		h.Write(buf[:])
	}
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}

// eventStoreCoverage 是「这个内容指纹的权威集合已被段逐条覆盖」的备忘条目。
type eventStoreCoverage struct {
	// digest 是已核对通过的权威集合内容指纹（见 eventSetDigest）。
	//
	// 为什么必须按内容而不是按长度：不同调用方会传**不同**的集合（投递主路径传权威全量、
	// commitBackfill 传补账批、migrateInlineEvents 传内联旧格式），它们的长度可能相同而内容
	// 完全不同 —— 按长度命中就会跳过本该追加的事件（静默丢数据 ✗）。
	digest [sha256.Size]byte
	// rows / epoch 是记下备忘时的段读数：段物理行数与本源的段物理版本号。
	//
	// 为什么这两个就够：段是只追加结构，除「末段半行截断」（崩溃残留校准）外没有改写路径。
	//   - 真正追加 ⇒ 行数变（且 epoch 变）⇒ 备忘自动失效；
	//   - 末段截断 ⇒ 行数变（变小）⇒ 备忘自动失效；
	//   - **头部回收（eventstore.Prune）⇒ 行数变（变小，见 eventstore_prune.go）⇒ 备忘自动失效**，
	//     且回收成功后调用方会调 invalidateEventStoreRowCaches 推进 epoch、直接删掉本条备忘 ——
	//     两道保险，因为「回收后又追加回同样行数」这种形态只有 epoch 能区分；
	//   - 三者都没变 ⇒ 段内容逐字节没变（由 eventstore.calibrateLocked 的校准语义保证：
	//     清单计数恒等于物理完整行数，故行数相同即内容相同）。
	rows  int
	epoch uint64
}

// hasStoredCoverageMemo 报告该源是否已有备忘（无备忘 ⇒ 不可能命中，调用方据此免算指纹）。
func (m *Manager) hasStoredCoverageMemo(key string) bool {
	m.mu.Lock()
	_, ok := m.storedCoverageMemo[key]
	m.mu.Unlock()
	return ok
}

// storedCoverage 报告「该指纹的集合已被段完整覆盖」是否可复用（未命中返回 false，调用方走全量遍历）。
//
// 锁序：先取段读数（eventstore 自己的 Store.mu），再取一次 m.mu 短临界区读备忘；
// 二者不嵌套持有，故不引入新的锁序边。
func (m *Manager) storedCoverage(key string, digest [sha256.Size]byte) (bool, error) {
	rows, err := m.events.Count(key)
	if err != nil {
		return false, err
	}
	m.mu.Lock()
	memo := m.storedCoverageMemo[key]
	epoch := m.eventsStoredEpoch[key]
	m.mu.Unlock()
	if memo.digest != digest || memo.rows != rows || memo.epoch != epoch {
		return false, nil
	}
	return true, nil
}

// rememberStoredCoverage 记下「该指纹的权威集合已完整落段」。
func (m *Manager) rememberStoredCoverage(key string, digest [sha256.Size]byte) {
	rows, err := m.events.Count(key)
	if err != nil {
		// 读数取不到就不记：备忘只是提速，缺它只会退回全量遍历核对。
		return
	}
	m.mu.Lock()
	if m.storedCoverageMemo == nil {
		m.storedCoverageMemo = map[string]eventStoreCoverage{}
	}
	epoch := m.eventsStoredEpoch[key]
	m.storedCoverageMemo[key] = eventStoreCoverage{digest: digest, rows: rows, epoch: epoch}
	m.mu.Unlock()
}

// bumpEventStoreEpoch 推进该源的段物理版本号，并**丢弃覆盖备忘**（本进程真正追加过段之后调用）。
//
// 两个写入出口（appendEvents / appendEventTail）都必须调它：否则「追加后备忘仍命中」会让
// 下一轮误判「无待追加」⇒ 静默漏写。
//
// 为什么顺手丢弃备忘：备忘的唯一用途是「同一份集合被反复问、其间没有任何写入」。
// 有写入 ⇒ 集合/段都变了，备忘本就不该复用；丢掉它还能让下一位调用方免于计算集合指纹
// （见 missingFromEventStore 的惰性指纹）。「备忘存在」因此等价于「自记备忘起无写入」，
// 这比单靠 rows/epoch 比对更早地排除掉不可命中的情形。
func (m *Manager) bumpEventStoreEpoch(key string) {
	m.mu.Lock()
	if m.eventsStoredEpoch == nil {
		m.eventsStoredEpoch = map[string]uint64{}
	}
	m.eventsStoredEpoch[key]++
	delete(m.storedCoverageMemo, key)
	m.mu.Unlock()
}

// appendEventTail 把「增量尾部」事件追加进段存储（投递快路径专用）。
//
// 与 appendEvents 的差别：后者的入参是**权威全量**，需要按身份核对段内已有内容后追加差集；
// 快路径已由 planDeliveryTail 保证「本批区间整体在段已覆盖水位之后」（该区间从未投递过 ⇒
// 段内不可能已有这些身份），因此可以直接整批追加，不做 O(段行数) 的核对。
//
// 状态推进与 appendEvents 逐字一致：段写成功后才把 state 标记为「已落段」并推进
// EventsStoredThrough（B1a 的按条引用判据），并清掉内联副本（避免常驻切片把 RSS 推高）。
func (m *Manager) appendEventTail(key string, tail deliveryTailPlan) error {
	if m.events == nil || len(tail.Events) == 0 {
		return nil
	}
	if err := m.events.Append(key, tail.Events); err != nil {
		return err
	}
	m.bumpEventStoreEpoch(key)
	m.rememberBodyReferenceProof(key, tail.Events)
	m.mu.Lock()
	if m.eventsStoredRows == nil {
		m.eventsStoredRows = map[string]int{}
	}
	m.eventsStoredRows[key] = tail.StoredRows + len(tail.Events)
	saved := m.state.Sources[key]
	saved.EventsStored = true
	var covered uint64
	for _, ev := range tail.Events {
		if ev.Record.End > covered {
			covered = ev.Record.End
		}
	}
	if covered > saved.EventsStoredThrough {
		saved.EventsStoredThrough = covered
	}
	saved.Events = nil
	m.state.Sources[key] = saved
	m.mu.Unlock()
	return nil
}

// migrateInlineEvents 把旧格式（state 内联 events）搬到段存储并重写 state。
// 任一步失败即保留旧格式继续运行（不丢数据、不半途改格式）。
func (m *Manager) migrateInlineEvents() error {
	if m.events == nil {
		return nil
	}
	type pending struct {
		key    string
		events []logtypes.Event
	}
	var todo []pending
	m.mu.Lock()
	for key, saved := range m.state.Sources {
		if len(saved.Events) > 0 {
			todo = append(todo, pending{key: key, events: saved.Events})
		}
	}
	m.mu.Unlock()
	if len(todo) == 0 {
		return nil
	}
	// 先全部落段；任一端失败则整体放弃迁移（旧格式数据仍完整）。
	for _, p := range todo {
		if err := m.appendEvents(p.key, p.events); err != nil {
			return nil // 保留旧格式继续用，不阻断启动
		}
	}
	m.mu.Lock()
	for _, p := range todo {
		saved := m.state.Sources[p.key]
		if saved.EventsStored {
			continue
		}
		saved.Events = nil
		saved.EventsStored = true
		m.state.Sources[p.key] = saved
	}
	m.mu.Unlock()
	return m.persist()
}

const maxPublishedProjectionGenerations = 64

func (m *Manager) writeProjectionDay(source SourceConfig, events, writeEvents []logtypes.Event, generation string, archiveEvents []logtypes.Event, replace bool, tailPlanned bool, yield func(), snapshots ...*publicationSnapshot) (pipeline.DeliveryResult, error) {
	var snapshot *publicationSnapshot
	if len(snapshots) > 0 {
		snapshot = snapshots[0]
	}
	ctx := context.Background()
	if snapshot != nil && snapshot.ctx != nil {
		ctx = snapshot.ctx
	}
	ctx, releaseGuard, guardErr := m.acquireSourceDelivery(ctx, source)
	if guardErr != nil {
		return pipeline.DeliveryResult{}, guardErr
	}
	defer releaseGuard()
	if m.deferredDelivery && acquire.DeliveryCaptureFromContext(ctx) == nil {
		var capture *acquire.DeliveryCapture
		if snapshot != nil && snapshot.written != nil {
			capture = snapshot.written.capture
		} else {
			pipe, ok := m.pipeFor(source.LogSourceID + "/" + source.SourceGeneration)
			if !ok {
				return pipeline.DeliveryResult{}, errPublicationStale
			}
			_, _, original, err := pipe.WAL().CaptureDelivery(nil)
			if err != nil {
				return pipeline.DeliveryResult{}, err
			}
			capture = original
		}
		// G没有采集入口ctx时也必须在实际身份裁定之前固定原cut，后续查询IO不能重造它。
		ctx = acquire.WithDeliveryCapture(ctx, capture)
	}
	if snapshot != nil {
		owned := *snapshot
		owned.ctx = ctx
		snapshot = &owned
	}
	if err := ctx.Err(); err != nil {
		return pipeline.DeliveryResult{}, err
	}
	// 热层保留期闸（2026-10-28 生产停采事件）：
	//
	// VL 对**早于 `now - retention`** 的数据点返回 HTTP 200 但立即丢弃（实测：写
	// _time=2026-09-20 的记录 → 200，随后查该窗口 → 0 行）。写进去必然查不到，而那会让
	// 后续投影校验恒失败 ⇒ PublicationPending 永不清 ⇒ 重建不断新建代次 ⇒ delivery /
	// reclaim 全部卡死 ⇒ WAL 撞上限 ⇒ 采集暂停。
	//
	// 为什么必须加在这里：`deliver`（整窗重发）与 `rebuildDaysInner`（重建）两条路径
	// 各自已有这道闸（见 hot_retention_window.go），但**本函数是第三条路径**（单天写入，
	// 归档导入的批次走它），而它之前没有。真机证据：生产日志里 543 次
	// 「整窗重发跳过超出热层保留期」同时，仍有 `archiveEvents:5781,
	// firstEventTime:2026-09-20T02:47:59Z` 的写入被发给 VL。
	//
	// 语义与另两条路径逐字一致：不写、不校、**如实记日志**（不静默丢）。
	if m.beyondHotRetention(source.UTCDay, time.Now()) {
		if len(writeEvents) > 0 || len(archiveEvents) > 0 {
			slog.Warn("写入面所属 UTC 天早于热层保留期，不写入、不校验（VL 会接收但丢弃）",
				"source", source.LogSourceID, "utcDay", source.UTCDay,
				"events", len(writeEvents), "archiveEvents", len(archiveEvents),
				"generation", generation, "hotRetention", m.hotRetention)
		}
		// 与 `deliver` 的全空硬闸同一语义：本批注定不可发布 ⇒ 不得宣称成功、
		// 不得让调用方推进 delivery 水位（否则会为从未进入 VL 的区间建副本证明）。
		return pipeline.DeliveryResult{}, errProjectionBeyondHotRetention
	}
	catKey := catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay}
	permit, permitErr := m.cat.AcquireWritePermit(catKey)
	if permitErr != nil {
		return pipeline.DeliveryResult{}, permitErr
	}
	defer m.cat.ReleaseWritePermit(permit)
	expectedAuthority, _ := m.cat.Get(catKey)
	if snapshot != nil && snapshot.earlierParent != nil {
		// 计划前置复核之后仍有真实持久化等待；新写许可不得将旧缺失证明重基到新权威/目标。
		if err := writtenProofContextErr(ctx); err != nil {
			return pipeline.DeliveryResult{}, err
		}
		parent := snapshot.earlierParent
		key := source.LogSourceID + "/" + source.SourceGeneration
		m.mu.Lock()
		valid := m.writtenAttempts[key] == parent && m.pipes[key] == parent.pipe && parent.pipe.OwnsDeliveryGuard(ctx) && sameCatalogAuthority(parent.authority, expectedAuthority) && parent.sourceToken == sourcePublicationToken(expectedAuthority, source) && parent.target.Owner == permit.Owner && parent.target.Generation == permit.Generation && parent.target.DirID == permit.DirID
		m.mu.Unlock()
		if !valid {
			return pipeline.DeliveryResult{}, errPublicationStale
		}
	}
	var writtenHint *writtenAttempt
	if snapshot != nil {
		writtenHint = snapshot.written
	}
	if writtenHint != nil && (!sameCatalogAuthority(writtenHint.authority, expectedAuthority) || writtenHint.sourceToken != sourcePublicationToken(expectedAuthority, source) || writtenHint.target.Owner != permit.Owner || writtenHint.target.Generation != permit.Generation || writtenHint.target.DirID != permit.DirID || writtenHint.generation != generation || writtenHint.day != source.UTCDay) {
		return pipeline.DeliveryResult{}, errPublicationStale
	}
	// tailPlanned 表示本次是「增量尾部」快路径：代次预算的判定已在 deliver 里做过
	// （同一判据、同源串行，见 projectionGenerationsExhausted），此处不得再改写 replace——
	// 快路径下 events 只是本批，没有可供整窗重发的权威全量。
	if current, ok := m.cat.Get(catKey); ok && writtenHint == nil && !replace && !tailPlanned &&
		len(publishedSourceProjectionGenerations(current.PublishedProjection, source)) >= maxPublishedProjectionGenerations {
		replace = true
		// 代次用尽 ⇒ 进位换代：新代按 replace 语义声明为「覆盖旧代并集」，读路径跨代解析 ✓。
		// 但**写入面保持调用方给出的差集**（本批；未变更行已由管线按「同 ID 同哈希」剔除，见 deliver 的
		// toWrite）：旧实现在此把写入面覆写为整窗全量 events ✗ ⇒ 同一 event_id 同 canonical_content_hash
		// 被再写进新代 ⇒ 查询面判重（w21 稿 ② 的根因）。仅当调用方未给出写入面（空）时才回退整窗，
		// 避免"声明了整窗却没写"。
		// 压缩是单代取代旧白名单，必须把本日完整 canonical 写入新代，不能只写差集。
		writeEvents = events
	}
	if m.deferredDelivery {
		if err := m.verifyActualProjectionIdentities(ctx, source, writeEvents, tailPlanned); err != nil {
			return pipeline.DeliveryResult{}, err
		}
	}
	client, frozen, err := m.clientForSource(source)
	if m.vlWritePermitRoute != nil {
		client, err = m.vlWritePermitRoute(source, permit)
		frozen = false
	}
	if err != nil || client == nil {
		return pipeline.DeliveryResult{}, fmt.Errorf("ingest: 写许可目标不可用: %w", err)
	}
	if frozen {
		return pipeline.DeliveryResult{}, catalog.ErrPhysicalDayFrozen
	}
	// 诊断日志（2026-09-28 恢复期「批次未落地」排查）：VL 侧核对时，需要能对上
	// 「哪一批、多少条、什么时间范围、每片写入返回什么」。生产实测曾出现
	// 「校验等待 09-27/28 窗口的数据，而 VL 里只有该源 09-20 的数据」——需要本日志
	// 才能判定是「没写」还是「写了没落地」。
	batchFirst, batchLast := "", ""
	if len(writeEvents) > 0 {
		batchFirst = writeEvents[0].EventTimeUTC
		batchLast = writeEvents[len(writeEvents)-1].EventTimeUTC
	}
	// 写面幂等（w21 稿 ②）尚未落地：差集预检与「对账不可用/关闭 ⇒ 整窗重发」（不得放宽）的既有护栏
	// 存在语义冲突，需先修订设计（仅在已发布面可信时启用 ✓），详见 w21-plan-idempotent-write.md 的后续裁定。
	slog.Info("投影写入开始",
		"generation", generation, "source", source.LogSourceID,
		"events", len(writeEvents), "archiveEvents", len(archiveEvents),
		"firstEventTime", batchFirst, "lastEventTime", batchLast,
	)
	// 写面幂等（w21 ②，2026-10-10 补上 replace 路径）：插入前对「已写入过的同 id 同 hash」做 id 级过滤。
	//
	// 作用域（与既有实现相比只放宽了一处，其余逐字不变）：
	//   - 慢路径（恢复/重放/非尾部增量）：按「任意未发布代次」过滤（既有行为 ✓）；
	//   - **replace 路径**（整窗重发/代次用尽/待发布）：按「**本次代次名**」过滤（本次新增）；
	//   - 增量尾部（tailPlanned）不经过本过滤：本批即新数据，无重复可言。
	//
	// 为什么 replace 路径必须过滤（2026-10-10 生产实测）：代次名由 `nextFreeProjectionGeneration`
	// 在**取 publishMu 之前**确定，并发或重试的两条投递可各自实探「未占用」、算出**同一个名字**，
	// 随后各写一份完整副本。生产可见后果（同一源、同一 2026-10-05 窗口）：`projection-5` 被整份
	// 写入两次、内容逐字节相同 ⇒ 该代次条数翻倍（544205 × 2 = 1088410，与 VL 实查吻合）；
	// 沙箱以生产等效容量复现同样形态（VL 条数 / 唯一事件 = 3.45x）。
	//
	// **只缩小插入面，不缩小校验面**（这是与"整批跳过"最要紧的区别）：校验面仍是本次计划的全量，
	// 于是「上次写全了」⇒ 插入面为空、校验全过、发布照常推进 ✓；「上次只写了一半」⇒ 缺席的那些
	// 条仍被插入、并按全量复核 ⇒ 自愈 ✓。若做成整批跳过，partial 形态会退化成「永不 publish +
	// 水位钉死」✗，故本实现刻意不做成那种形态。
	//
	// 判据不放宽（红线逐字保持 ✓）：只跳过同 id **且**同 hash ⇒ 同 id 异 hash 的真更正照写 ✓；
	// 任何查询失败一律照写 ✓；代次白名单与水位语义一字不动 ✓。
	// 注意：**不得**用 `expectedAuthority != nil` 早短路——分区记录暂缺（重启早期）与幂等无关 ✗。
	insertEvents := writeEvents
	if writtenHint != nil {
		insertEvents = nil
	}
	if writtenHint == nil && len(writeEvents) > 0 && !tailPlanned {
		if replace {
			// 整窗路径：只认本次代次名（`generation` 是本次将要写的那个物理代次）。
			insertEvents = m.dropEventsAlreadyWrittenCtx(ctx, client, source, generation, writeEvents)
		} else {
			// 慢路径：保持既有口径（任意未发布代次，含更早的 staging 代次）。
			insertEvents = m.dropEventsAlreadyWrittenCtx(ctx, client, source, "", writeEvents)
		}
	}
	var materialization stateindex.MaterializationReservation
	if (replace && !tailPlanned || snapshot != nil && snapshot.earlierParent != nil && writtenHint == nil) && len(insertEvents) > 0 {
		var release func()
		class := "rebuild"
		if snapshot != nil && snapshot.materializationClass != "" {
			class = snapshot.materializationClass
		}
		materialization, release, err = m.reserveMaterializationClass(ctx, source, generation, insertEvents, writeEvents, class)
		if err != nil {
			return pipeline.DeliveryResult{}, err
		}
		defer release()
	}
	capture := acquire.DeliveryCaptureFromContext(ctx)
	var members *acquire.DeliveryCapture
	var history *projectionHistoryProof
	liveCount := 0
	if m.deferredDelivery {
		key := source.LogSourceID + "/" + source.SourceGeneration
		pipe, ok := m.pipeFor(key)
		if !ok {
			return pipeline.DeliveryResult{}, errPublicationStale
		}
		if writtenHint == nil {
			active, historical, ids, currentCapture, captureErr := pipe.WAL().CaptureDeliverySubset(writeEvents, capture)
			if captureErr != nil {
				return pipeline.DeliveryResult{}, captureErr
			}
			capture = currentCapture
			if err := m.persistCommittedBeforeDelivery(ctx, key, pipe, ids); err != nil {
				return pipeline.DeliveryResult{}, err
			}
			var historyErr error
			history, historyErr = m.captureProjectionHistory(ctx, source, writeEvents, historical, active)
			if historyErr != nil {
				return pipeline.DeliveryResult{}, historyErr
			}
			var markErr error
			members, markErr = pipe.WAL().MarkDeliveryMembers(active, capture)
			if markErr != nil {
				return pipeline.DeliveryResult{}, markErr
			}
			capture = members
			liveCount = len(active)
		} else {
			capture = writtenHint.capture
			if _, ids, err := m.snapshotWrittenAttempt(ctx, source, writtenHint); err != nil {
				return pipeline.DeliveryResult{}, err
			} else if err := m.persistCommittedBeforeDelivery(ctx, key, pipe, ids); err != nil {
				return pipeline.DeliveryResult{}, err
			}
		}
	}
	if members != nil {
		defer func() {
			key := source.LogSourceID + "/" + source.SourceGeneration
			m.mu.Lock()
			hint := m.activeWrittenAttemptLocked(key)
			retained := hint != nil && hint.capture == members
			m.mu.Unlock()
			if !retained {
				pipe, _ := m.pipeFor(key)
				if pipe != nil {
					pipe.WAL().ClearDeliveryMembers(members)
				}
			}
		}()
	}
	status := 0
	if writtenHint != nil {
		status = writtenHint.status
	} else {
		status, err = m.insertInBatchesCtxYield(ctx, client, insertEvents, generation, yield)
		if len(insertEvents) > 0 && !vlsup.IsPermanent(err) {
			var earlierParent *writtenAttempt
			if snapshot != nil {
				earlierParent = snapshot.earlierParent
			}
			m.rememberWrittenAttempt(source, writeEvents, generation, expectedAuthority, permit, members, history, liveCount, status, materialization, err == nil, replace, tailPlanned, snapshot != nil && snapshot.keepPending, earlierParent)
		}
	}
	slog.Info("投影写入结束",
		"generation", generation, "source", source.LogSourceID,
		"events", len(insertEvents), "verified", len(writeEvents), "status", status, "err", err)
	if err != nil {
		return pipeline.DeliveryResult{HTTPStatus: status}, err
	}
	if writtenHint != nil {
		materialization = writtenHint.materialization
	} else if err := m.updateMaterialization(ctx, materialization, "written", false, false, "全部物理插入已返回成功"); err != nil {
		return pipeline.DeliveryResult{HTTPStatus: status}, err
	}
	if m.archive != nil && len(archiveEvents) > 0 {
		archivePayload, payloadErr := projectionPayload(archiveEvents, generation)
		if payloadErr != nil {
			return pipeline.DeliveryResult{HTTPStatus: status}, payloadErr
		}
		archiveKey := archive.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay, Generation: 1}
		if _, archiveErr := m.archive.RegisterRaw(ctx, archiveKey, archive.RawSource{
			Data: archivePayload, Origin: archive.OriginWorkerGenerated,
			LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration,
			ParserVersion: "worker-log-parser/v1", EventCount: uint64(len(archiveEvents)),
			EventTimeFromUTC: archiveEvents[0].EventTimeUTC,
			EventTimeToUTC:   archiveEvents[len(archiveEvents)-1].EventTimeUTC,
		}); archiveErr != nil {
			return pipeline.DeliveryResult{HTTPStatus: status}, archiveErr
		}
	}
	// **投递主路径不做逐字段回查**（2026-10-30 改造）：HTTP 状态码即结论。
	//
	// 为什么（2026-09-28 与 2026-10-22 两次现场 + 60 源基线实测）：写完再“逐字段回查”
	// 占了整轮墙钟的绝大多数——60 源逐源串行基线里 verify（VL 的 LogsQL 校验查询，含重试退避）
	// =85.3s / 99.6s（85.6%，见 defaultPollConcurrency 的注释）。而它产出的结论只有两种：
	// “查得到” ⇒ 发布；“查不到” ⇒ 把整批重写一遍再查一次。后一种形态一旦因 VL 负载偏高
	// 而反复超时，就变成「写完 573562 条 → 校验不过 → 换代次再写一遍」的正反馈（VL 体积放大到 75×）。
	//
	// 现在的语义边界（与 Vector / Fluent Bit 那类官方采集器对齐）：
	//   - 单批要么整批被 VL 接受（2xx）、要么整批失败（非 2xx / 传输错），由 insertInBatchesCtxYield
	//     的返回决定；**没有第三种“写没写进去未知”的状态**，因为请求体是一次 POST 送完的；
	//   - 事件重发幂等由 event_id 承担（VL 侧按 event_id 去重），所以“宁可重发”仍安全；
	//   - 回收链只依赖 REQUEST_DONE（按 HTTP 状态码落，见 acquire 的 recordHTTPResultLocked），
	//     不依赖本处的回查结果（VerifiedRuns 从未进入 ledger 包）；
	//   - 逐字段核验仍保留在**取证路径**上（repairWrittenEarlierDebt / tryConfirmPendingPublication /
	//     hot_retention_window / scoped_query_budget），那里面对的是“历史欠账是否真的在 VL 里”，
	//     与“本批刚发出去的 POST 是否被接受”是两个问题，不可混为一谈。
	//
	// 因此这里只做一件事：把本次投递的成功当进展登记（HTTP 2xx 本身就是进展），
	// 然后继续走发布。即使 VL 因某种原因“收下了但没入库”，后果也是“这批数据的可见性
	// 只能由后续的取证路径确认”，而不是“整批重写”——保守方向由下游的 pending/debt 判据承担。
	m.noteDeliveryProgress(source.LogSourceID + "/" + source.SourceGeneration)
	if err := m.updateMaterialization(ctx, materialization, "verified", true, false, "VL 已受理本批写入（HTTP 2xx）；逐字段可见性核验不在投递主路径上，尚未发布，CAS失效仍保留成本"); err != nil {
		return pipeline.DeliveryResult{HTTPStatus: status}, err
	}
	checkWAL := !tailPlanned || (snapshot != nil && snapshot.checkPendingWAL)
	debt, blocking, debtErr := m.hasKnownUnpublishedDayDebt(ctx, source, writeEvents, expectedAuthority, checkWAL)
	if debtErr != nil {
		return pipeline.DeliveryResult{HTTPStatus: status}, debtErr
	}
	if debt {
		// 新尾已真实写入，但同日旧正文尚未确认，不能把maxEnd宣称为完整封闭前缀。
		key := source.LogSourceID + "/" + source.SourceGeneration
		m.recordVerifiedRuns(key, gapRangesOfEvents(writeEvents))
		// **给「被卡住的那条历史记录」补登记可见性证据**（2026-11-02 真机缺陷修复）。
		//
		// 为什么必须补：`hasKnownUnpublishedDayDebt` 判的是**整条 WAL 里同日的历史条目**，
		// 而上面这条 `recordVerifiedRuns` 登记的是**本次新写入的**。于是：
		//
		//	旧记录 A 从未进 verified → debt 恒真 → 每轮都登记"本次的 B"、A 依然没登记
		//	→ A 永远等不到自己的区间 → closed 被它拖住（实测停在 150418 / 文件 497879）
		//
		// 铁证：被判欠账的记录 69716..69933 在 VL 里已存在 3 份（projection-3/15/17）——
		// 数据从未丢失，是记账漏了。补验通过的记录即可进入 verified，下一轮它就不再算欠账。
		//
		// 保守边界（一条都不放宽）：
		//   · 只对**这一条**已被判欠账的记录做核对，不扩大范围；
		//   · 核对**不通过就不登记**（`landedVisibleInVL` 返回空即作罢），
		//     绝不把"没查到的"写成已验证；
		//   · 补验失败不影响本次投递结论（仍返回 errKnownUnpublishedDayDebt）。
		if blocking != nil {
			if landed := m.landedVisibleInVL(source, []logtypes.Event{*blocking}); len(landed) == 1 {
				m.recordVerifiedRuns(key, gapRangesOfEvents(landed))
				slog.Info("已为历史欠账记录补登记可见性证据（下轮不再计入欠账）",
					"logSourceID", source.LogSourceID,
					"recordStart", blocking.Record.Start, "recordEnd", blocking.Record.End)
			}
		}
		m.deferDurableDelivery(key)
		m.scheduleRebuildConfirmPendingAsync(source, []string{source.UTCDay}, "同日已知未投递正文阻止封闭前缀越界")
		return pipeline.DeliveryResult{HTTPStatus: status}, errKnownUnpublishedDayDebt
	}
	if m.deferredDelivery {
		pipe, ok := m.pipeFor(source.LogSourceID + "/" + source.SourceGeneration)
		if !ok || !pipe.OwnsDeliveryGuard(ctx) || (writtenHint != nil && writtenHint.pipe != pipe) {
			return pipeline.DeliveryResult{HTTPStatus: status}, errPublicationStale
		}
		releaseWindow, windowErr := pipe.WAL().AcquireCapturedWindow(writeEvents, writtenHint != nil, func(event logtypes.Event) bool {
			day, err := eventUTCDay(source, event)
			return err == nil && day == source.UTCDay
		}, true, capture)
		if windowErr != nil {
			return pipeline.DeliveryResult{HTTPStatus: status}, windowErr
		}
		defer releaseWindow()
		// 租约线性化后再复核候选前缀债务；旧前缀不能在journal落地期间先COMMIT再被closed越过。
		debt, _, err := m.hasKnownUnpublishedDayDebt(ctx, source, writeEvents, expectedAuthority, true)
		if err != nil {
			return pipeline.DeliveryResult{HTTPStatus: status}, err
		}
		if debt {
			m.deferDurableDelivery(source.LogSourceID + "/" + source.SourceGeneration)
			return pipeline.DeliveryResult{HTTPStatus: status}, ErrVerifyDeferred
		}
	}
	if err := m.publishSnapshot(source, generation, writeEvents, replace, expectedAuthority, snapshot, true, permit); err != nil {
		return pipeline.DeliveryResult{HTTPStatus: status}, err
	}
	if err := m.updateMaterialization(ctx, materialization, "published", true, true, "逐字段校验与Catalog发布均成功"); err != nil {
		return pipeline.DeliveryResult{HTTPStatus: status}, err
	}
	if writtenHint == nil && (snapshot == nil || snapshot.earlierParent == nil) {
		key := source.LogSourceID + "/" + source.SourceGeneration
		m.mu.Lock()
		hint := m.writtenAttempts[key]
		m.mu.Unlock()
		if hint != nil && hint.day == source.UTCDay {
			m.clearWrittenAttempt(key, hint)
		}
	}
	return pipeline.DeliveryResult{HTTPStatus: status}, nil
}

// insertBatchMaxEvents / insertBatchMaxBytes / insertBatchFlushTimeout 等攒批常量已移到
// insert_batch.go（双阈值攒批层），本文件只保留调用点。
//
// 插入面有两个入口：insertInBatches（按攒批阈值分批投递，返回最后一批的 HTTP 状态；
// 任一批失败即中断并原样返回该批状态与错误，调用方据此判定投递结果）与
// dropEventsAlreadyWritten（写面幂等过滤，见下）。
