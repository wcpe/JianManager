package ingest

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// ── 热层可发布窗口（2026-10-21 生产缺陷修复）────────────────────────────────────
//
// 现场事实（`worker-final2.log`，已用真实 VL 实测复核）：
//  1. VL **热层**进程以 `-retentionPeriod=30d` 运行（端口 19461）；
//  2. VL 对**超出 retention 的数据点返回 HTTP 200 但静默丢弃**
//     （写 45 天前数据 → 200，随后查询 0 行；写当天数据 → 200，查询 1 行）；
//  3. 于是重建重放历史天时拿到 200 却写不进去 ⇒ 日志打
//     `投影写入结束 events:5781, verified:5781, status:200`（**假成功**），
//     随后逐字段校验恒 `rows:0` ⇒ 14 秒后超时 ⇒ `markPublicationPending`
//     ⇒ `PublicationPending` 永久为真 ⇒ 重建重试到上限后「已停止自动重试」；
//  4. `reconcileWithExpectation` 每天仍把该天判成 `observed < expected`
//     ⇒ 缺失天永远存在 ⇒ 永远重新登记重建。
//
// 修复口径：引入「热层可发布窗口」。早于边界（`now - 热层 retention`）的 UTC 天
// **不可能**在热层可见，因此既不重建、也不算缺失天；但必须**如实记录**它们
// 「超出热层保留期、未发布到热层」，绝不宣称已发布（数据诚实性红线）。

// hotRetentionBoundary 返回热层保留边界：早于该时刻的数据点在热层里不可能可见。
//
// 纯函数（不读时钟、不碰 VL 状态），供判定与日志/排障共用同一个口径。
func hotRetentionBoundary(now time.Time, retention time.Duration) time.Time {
	return now.Add(-retention)
}

// hotRetentionDayBeyondWindow 报告 UTC 天 `utcDay`（`YYYY-MM-DD`）是否**完全**落在
// 热层保留边界之前——即该天整天都早于 `now - retention`，因而不可能在热层可见。
//
// 保守判据（宁可多发布一次，绝不误伤可见数据）：
//   - `retention <= 0` ⇒ 恒 false：零值语义是「未知 / 未启用过滤」，必须保持改动前的
//     行为（没配置就把正常数据滤掉，比不修更糟）；
//   - 天字符串不可解析 ⇒ 恒 false（同上：不因脏数据而静默丢弃）；
//   - 只有当该天**整天**（含最后一纳秒）都早于边界时才判 true；**跨越边界的那一天**
//     （部分数据点仍落在窗口内）一律按可发布处理，不得误伤。
//
// 为什么用「含最后一纳秒的日末」而不是「次日零点」：`utcDayBounds` 给出的日末是
// `次日零点 - 1ns`，对它做 `Before(boundary)` 恰好等价于「次日零点 <= 边界」——
// 而天区间是左闭右开，边界之后的数据点才可能保留，故这个口径既严格又不多滤一天。
func hotRetentionDayBeyondWindow(utcDay string, now time.Time, retention time.Duration) bool {
	if retention <= 0 || utcDay == "" {
		return false
	}
	_, dayEnd, ok := utcDayBounds(utcDay)
	if !ok {
		return false
	}
	return dayEnd.Before(hotRetentionBoundary(now, retention))
}

// beyondHotRetention 是 `hotRetentionDayBeyondWindow` 在 Manager 上的绑定形态
// （retention 来自 Options.HotRetention；零值 ⇒ 恒 false，保持改动前行为）。
func (m *Manager) beyondHotRetention(utcDay string, now time.Time) bool {
	if m == nil {
		return false
	}
	return hotRetentionDayBeyondWindow(utcDay, now, m.hotRetention)
}

// splitHotRetentionDays 把一批 UTC 天切成「窗口内（可发布）」与「超窗口（热层不可能可见）」
// 两段，两段都保持升序。
//
// 为什么必须**两段都返回**：调用方要跳过超窗口的天（不写、不校验、不算缺失），
// 但绝不能把它们静默丢掉——它们必须进日志、进对账报告，作为「未发布」被如实记录。
func (m *Manager) splitHotRetentionDays(days []string, now time.Time) (inWindow, beyond []string) {
	inWindow = make([]string, 0, len(days))
	beyond = make([]string, 0, len(days))
	for _, day := range days {
		if m.beyondHotRetention(day, now) {
			beyond = append(beyond, day)
			continue
		}
		inWindow = append(inWindow, day)
	}
	return sortUTCDays(inWindow), sortUTCDays(beyond)
}

// ── 「本地有 vs VL 有」的证据面判定（2026-10-21 真机缺陷，第二版修复）──────────────
//
// 第一版修的是「按热层保留期窗口过滤证据面」，**判别维度选错了**：
// 它证明的是「这天在窗口内」，而缺陷要证明的是「这批事件确实在 VL 里」。
// 生产上真实缺失的那天（`inst:317` 的 2026-10-21）**恰在窗口内**，
// 所以窗口过滤根本不会命中它 —— 缺口照样被消解成 `delivery confirmed`，
// 而 VL 里该源上界正是缺口起点（7583541），一行业务数据都没有。
//
// 正确判据只能是**向 VL 实际查询**：只把「查得到」的事件当作证据面，
// 查不到的（本地段有、VL 无）必须保留缺口并显式告警。

// landedVisibleInVL 从一批「本地段里已有」的事件中，只挑出**在 VL 中确实查得到**的那些，
// 作为缺口消解的证据面；查不到的原样留在外面（其缺口因此不被消解）。
//
// ── 为什么必须是「查 VL」而不是「判窗口」（2026-10-21 真机缺陷的第二次修复）──────
// 第一版用「这天是否超出热层保留期」做过滤，判别维度错误：生产上真实缺失的那天是
// `inst:317` 的 2026-10-21，**在窗口内**，窗口过滤不会命中 ⇒ 缺口仍被消解成
// `delivery confirmed`，而 VL 该源上界恰为缺口起点（7583541）、零行数据。
//
// ── 查询口径 ────────────────────────────────────────────────────────────
// 用**已发布代次白名单**（`publishedSourceProjectionGenerations`）+ `event_id:in(...)` 查，
// 与查询侧「能查到」的定义一致：不要求它属于本次代次（缺口事件可能属旧代次），
// 只要求「按当前已发布白名单能查到」——这正是「用户查得到」的等价条件。
//
// ── 保守边界（任何不确定都**不消解**）─────────────────────────────────────
//
//	· 没有 VL 客户端 / 取不到 catalog 记录 / 白名单为空 ⇒ 空证据；
//	· 查询失败（网络、超时、4xx）⇒ 空证据：宁可缺口多留一轮，也不能凭猜测抹平欠账；
//	· `landed` 为空 ⇒ 直接返回。
//
// 查到一部分时返回那一部分：查不到的事件必然不被计入，其缺口保持可见并落 WARN。
func (m *Manager) landedVisibleInVL(source SourceConfig, landed []logtypes.Event) []logtypes.Event {
	return m.landedVisibleInVLCtx(context.Background(), source, landed)
}

func (m *Manager) landedVisibleInVLCtx(parent context.Context, source SourceConfig, landed []logtypes.Event) []logtypes.Event {
	if m == nil || len(landed) == 0 || m.cat == nil {
		return nil
	}
	// Raw事件在外发时已按稳定IngestTime规范化；回收/重复取证必须使用同一时间与内容哈希口径。
	canonical := make([]logtypes.Event, 0, len(landed))
	for _, event := range landed {
		normalized, err := normalizeCanonicalEvent(event)
		if err != nil {
			return nil
		}
		canonical = append(canonical, normalized)
	}
	landed = canonical
	// **必须按每个事件所属的分区取白名单**，不能统一用 `source.UTCDay`：
	// 缺口跨越历史天（生产实测缺口在 10-21，而源的当前天可能是今天），
	// 用当前天分区的白名单去查历史天的事件必然查不到，会把「可见」误判成「不可见」。
	generationsByDay := make(map[string][]string)
	generationsFor := func(day string) ([]string, bool) {
		if gens, ok := generationsByDay[day]; ok {
			return gens, len(gens) > 0
		}
		rec, ok := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: day})
		if !ok || rec == nil || rec.PublishedProjection == nil || !rec.PublishedProjection.CoverageComplete {
			generationsByDay[day] = nil
			return nil, false
		}
		gens := publishedSourceProjectionGenerations(rec.PublishedProjection, source)
		generationsByDay[day] = gens
		return gens, len(gens) > 0
	}
	ctx, cancel := context.WithTimeout(parent, m.verifyBackoffMax*4+time.Second)
	defer cancel()
	// 按天分组后各组用各自的白名单批量查询（见上方 generationsFor 的理由）。
	visible := make([]logtypes.Event, 0, len(landed))
	byDay := make(map[string][]logtypes.Event)
	order := make([]string, 0, 4)
	for _, ev := range landed {
		day, derr := eventUTCDay(source, ev)
		if derr != nil || day == "" {
			// 取不到天：保守地不作证据（不消解对应缺口）。
			continue
		}
		if _, seen := byDay[day]; !seen {
			order = append(order, day)
		}
		byDay[day] = append(byDay[day], ev)
	}
	for _, day := range order {
		gens, ok := generationsFor(day)
		if !ok {
			// 该天没有已发布投影 ⇒ 查询侧本来也查不到它，不作证据。
			continue
		}
		// **必须把 UTCDay 设成该事件所属天**：`verifyProjectionQuery` 会用
		// `utcDayBounds(source.UTCDay)` 把查询时间窗夹在「源当前天」内
		// （见 runtime.go 里那段「时间窗必须夹在本源的 UTC 日内」的历史注释）。
		// 若沿用源当前天，跨天缺口的时间窗会落在错误的一天 ⇒ 恒查不到
		// ⇒ 事件被判「不可见」⇒ 缺口永远无法消解 ⇒ 阻塞 ResumeAcquire ⇒ 源暂停。
		daySource := source
		daySource.UTCDay = day
		rec, _ := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: day})
		client, qerr := m.publishedEvidenceClient(daySource, rec)
		if qerr != nil || client == nil {
			return nil
		}
		closed := publishedEvidenceClosed(rec, daySource)
		candidates := make([]logtypes.Event, 0, len(byDay[day]))
		for _, event := range byDay[day] {
			if event.Record.End <= closed {
				candidates = append(candidates, event)
			}
		}
		batchVisible, qerr := m.visibleEventsInVL(ctx, client, daySource, gens, candidates)
		if qerr != nil {
			slog.Warn("缺口消解前核对 VL 可见性失败：本轮不消解任何缺口（下轮重试）",
				"source", source.LogSourceID, "utcDay", day, "error", qerr)
			return nil
		}
		visible = append(visible, batchVisible...)
	}
	visibleSet := make(map[string]struct{}, len(visible))
	for _, ev := range visible {
		visibleSet[ev.EventID] = struct{}{}
	}
	missing := make([]logtypes.Event, 0)
	kept := make([]logtypes.Event, 0, len(visible))
	for _, ev := range landed {
		if _, ok := visibleSet[ev.EventID]; ok {
			kept = append(kept, ev)
			continue
		}
		missing = append(missing, ev)
	}
	if len(missing) > 0 {
		ids := make([]string, 0, len(missing))
		for _, ev := range missing {
			ids = append(ids, ev.EventID)
		}
		slog.Warn("本地段有、VL 查不到：这些事件不得作为缺口消解证据（缺口保持可见）",
			"source", source.LogSourceID, "missingCount", len(missing),
			"visibleCount", len(visible), "sampleEventIDs", ids[:min(len(ids), 3)])
	}
	return kept
}

// visibleEventsInVL 用**一次**查询取出这批事件里在 VL 可见的那些（按已发布代次白名单）。
//
// 分批上限取 `verifyChunkEvents` 同量级：单次 LogsQL 的 `event_id:in(...)` 过长会被拒，
// 且过大的响应体不值得为一个只读核对付成本。超限时按批切分、逐批查询后合并。
func (m *Manager) visibleEventsInVL(ctx context.Context, client *vlsup.Client, source SourceConfig,
	generations []string, events []logtypes.Event) ([]logtypes.Event, error) {
	if len(generations) == 0 || client == nil {
		return nil, errors.New("ingest: 当前已发布证据面不可用")
	}
	const batch = 512
	out := make([]logtypes.Event, 0, len(events))
	for start := 0; start < len(events); start += batch {
		end := start + batch
		if end > len(events) {
			end = len(events)
		}
		ok, _, err := m.verifyProjectionQuery(ctx, client, source, generations[0], events[start:end], nil, generations)
		if err != nil {
			return nil, err
		}
		if !ok {
			// 本批未能凑齐 ⇒ 再逐条确认本批内哪些可见（批内通常很小，成本可控）。
			for _, ev := range events[start:end] {
				one, _, oneErr := m.verifyProjectionQuery(ctx, client, source, generations[0],
					[]logtypes.Event{ev}, nil, generations)
				if oneErr != nil {
					return nil, oneErr
				}
				if one {
					out = append(out, ev)
				}
			}
			continue
		}
		out = append(out, events[start:end]...)
	}
	return out, nil
}

// errProjectionBeyondHotRetention 表示「本批写入面全部超出热层保留期，注定不可发布」。
//
// 与「写入失败」区分开：这不是 VL 出错，而是**本批数据在热层不可能可见**。
// 调用方必须把它当作「未投递」处理——**不得**推进 Delivery 水位、不得建回收证明，
// 否则会把「从未进入 VL 的区间」标成已有副本，随后 reclaim 越过后 WAL 条目被剪。
var errProjectionBeyondHotRetention = errors.New("ingest: projection write face entirely beyond hot retention")

// ── 发布确认取证的三态结论（2026-10-21 重建饥饿根治）──────────────────────────
//
// 背景：`tryConfirmPendingPublicationCtx` 是两态的（`(bool, error)`），而它在算出
// `missing` 之后还有三道「证据稳定性」校验（发布快照未变 / 各天 authority 未变 /
// attempt 与代次未变）。活跃源每个 poll 都在写、都在发布，这三道**几乎必然失败**，
// 而它们失败时返回 `(false, nil)` —— 已经算出来的 `missing` **被丢弃**。
//
// 上层 `rebuildDaysInner` 拿不到 `missing`，就退回使用「登记时的全量天清单」
// （`reconcileExpectationDays` = 该源有内容的全部天，升序）。配合默认切片预算
// 1 天/轮 ⇒ **每轮重写最老那一天**。若最老那天恰好跨热层保留边界（判据按整天算，
// 故仍被当成「可发布」），它早于边界的事件永远写不进 VL ⇒ 永远校验失败 ⇒
// **永久饥饿**：pending 永不清零、代次被持续消耗、VL 体积无界增长。
// 生产实证：`published=['2026-09-21'] remaining_n=15` 连续六轮完全相同。
//
// 修法：把结论拆成三态，让「取证不稳定」与「已测出缺失天」不再混为一谈。
type confirmVerdict int

const (
	// confirmConfirmed：VL 已完整可见，pending 可以清（原有成功路径）。
	confirmConfirmed confirmVerdict = iota
	// confirmMissingMeasured：取证**已完成**，`missing` 是可信的实测缺失天。
	// 上层应据此收敛重建面，而不是退回登记全量。
	confirmMissingMeasured
	// confirmInconclusive：取证**没跑完**（有在飞写入 / 快照失效 / authority 变化 /
	// attempt 变化 / 取权威集合失败）。此时 `missing` 不可信（可能为空但没测全）。
	//
	// **上层必须直接返回 pending 且一天都不写**：把「证据不稳定」当成
	// 「按老清单重写」正是饥饿的成因；当成「没有缺失」又会谎报完成。
	confirmInconclusive
)

// beyondHotRetentionForEvents 报告这批事件是否**全部**落在已超出热层保留期的 UTC 天。
//
// 为什么需要（2026-10-29 生产停采最后一环）：补段要求「从 VL 取回已发布证据」，而
// 超出保留期的天在 VL 里物理上不存在（VL 对超期数据返回 200 但立即丢弃，已实测）。
// 这类批次补段**永远**不会成功，只会让 reclaim 卡死、WAL 撞上限、整源停采。
//
// 判据取「全部」而非「任一」：只要还有一天在窗口内，就应该正常走补段（那天的证据是可取的）。
// 空批次返回 false（无事可做，不触发放弃）。
//
// `hotRetention <= 0`（未配置）⇒ 恒 false：与 `beyondHotRetention` 同一取舍，
// 没配置就放弃数据比不修更糟。
func (m *Manager) beyondHotRetentionForEvents(source SourceConfig, events []logtypes.Event) bool {
	if m == nil || m.hotRetention <= 0 || len(events) == 0 {
		return false
	}
	now := time.Now()
	for _, event := range events {
		day, err := eventUTCDay(source, event)
		if err != nil || day == "" {
			slog.Info("超保留期判定：事件天判不出，不放弃",
				"source", source.LogSourceID, "err", err, "eventTime", event.EventTimeUTC)
			return false // 判不出天就不放弃（保守方向）
		}
		if !m.beyondHotRetention(day, now) {
			slog.Info("超保留期判定：存在窗口内的天，走正常补段",
				"source", source.LogSourceID, "day", day,
				"boundary", hotRetentionBoundary(now, m.hotRetention).UTC().Format(time.RFC3339))
			return false // 有一天在窗口内 ⇒ 走正常补段
		}
	}
	return true
}

// abandonBeyondHotRetention 把「VL 里物理上不存在」的这段前缀如实登记为永久丢失，
// 并让回收链跨过它。
//
// 与 `AbandonGapsThrough` 的分工：后者处理**已登记的缺口**；这里的形态是
// 「WAL 有数据、账本说已确认投递、但 VL 里取不到已发布证据」，缺口列表里未必有对应条目
// （生产实测：这些源的缺口全是零宽的 ARCHIVE_CONTENT_UNKNOWN，不覆盖那 2 万条 WAL）。
// 所以直接按**位置区间**落凭据，语义与缺口放弃一致（都会被 `walRecordAbandoned` 认到）。
//
// 为什么要 operator：`AbandonGapsThrough` 拒绝空操作人（放弃是唯一允许回收链跨过
// 永远补不上的空洞的动作，无痕放弃等于给静默丢日志开后门）。这里用固定的系统操作人，
// 明确的语义是「系统按保留期事实自动裁定」，与人工裁定可区分（都进 Abandonments 凭据）。
func (m *Manager) abandonBeyondHotRetention(p *pipeline.Pipeline, source SourceConfig, from, to uint64) error {
	if p == nil || p.Ledger() == nil || to <= from {
		return nil
	}
	key := source.LogSourceID + "/" + source.SourceGeneration
	entry := p.Ledger().Get(p.Key())
	if entry == nil {
		return nil
	}
	// 用 AbandonGapsThrough 落凭据：它会把**结束位置 <= to** 的未解决缺口一并标记为
	// PERMANENTLY_LOST（本场景下这些零宽缺口本来也属于永久丢失类），并把凭据写进
	// entry.Abandonments ⇒ 随后 `walRecordAbandoned` 就能让回收链跨过这段。
	//
	// 若该源此刻没有任何未解决缺口，AbandonGapsThrough 返回 (0, nil) 且不写凭据
	// （它刻意避免「什么都没放弃的空裁定」）——那种情况下我们要自己补一条纯位置凭据。
	resolved, err := p.Ledger().AbandonGapsThrough(p.Key(), to, ledger.GapAbandonment{
		ReasonCode: ledger.GapReasonPermanentlyLost,
		Reason:     "data beyond VictoriaLogs retention period",
		Detail: "VL accepts but silently drops data older than -retentionPeriod; this prefix " +
			"can never be proven published, so the reclaim chain must cross it",
		Operator: "system:retention-window",
		Through:  to,
	})
	if err != nil {
		return err
	}
	// 位置区间本身也必须有凭据：回收链（`walRecordAbandoned`）认的是 Abandonments 里的**区间**，
	// 而 `AbandonGapsThrough` 只在「有缺口挡在 through 之前」时才会写凭据。
	//
	// 生产形态正是反例：这 10 个源的 18 条未解决缺口全是**零宽**的
	// （`ARCHIVE_CONTENT_UNKNOWN` 的 [x,x)），它们虽然会被标记为已解，
	// 但零宽区间盖不住那 2 万条 WAL 的真实位置段 ⇒ 回收链仍然跨不过去。
	//
	// 所以显式登记一条覆盖 `[from, to)` 的缺口再放弃它——用与人工裁定完全相同的机制，
	// 使凭据落到 `entry.Abandonments` 里，语义、审计面、幂等性都与既有路径一致。
	if err := p.Ledger().RecordGap(p.Key(), from, to, ledger.GapReasonPermanentlyLost,
		"data beyond VictoriaLogs retention period: VL accepts but silently drops data older than "+
			"-retentionPeriod, so this prefix can never be proven published; reclaim must cross it"); err != nil {
		return err
	}
	if _, err := p.Ledger().AbandonGapsThrough(p.Key(), to, ledger.GapAbandonment{
		ReasonCode: ledger.GapReasonPermanentlyLost,
		Reason:     "data beyond VictoriaLogs retention period",
		Detail: "VL accepts but silently drops data older than -retentionPeriod; " +
			"reclaim must cross this prefix or the source stalls forever",
		Operator: "system:retention-window",
		Through:  to,
	}); err != nil {
		return err
	}
	slog.Warn("该前缀超出热层保留期、VL 里不可能有已发布证据：已登记为永久丢失，回收链跨过它",
		"source", source.LogSourceID, "key", key, "from", from, "to", to,
		"resolvedGaps", resolved, "hotRetention", m.hotRetention)
	return nil
}
