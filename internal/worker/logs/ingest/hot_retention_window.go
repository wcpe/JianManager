package ingest

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
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

// gapVisibilityBudget 是「缺口核对」在 VL 上等待「写后可见」的总预算。
//
// 为何向 6s：VL 的写后可见延迟实测恒为 1.028~1.057s（`_time` 新旧无关）；
// 退避序列 0/0.2/0.6/1.1s 的四个探测点在 1.0s 附近刚好够，但只要 VL 因负载
// 把可见延迟推到 1.5~3s 就会再次落空。本路径已异步化（不对投递主链施加延迟），
// 因此把预算留到 6s，覆盖「可见延迟上行为 3s」的退化情形。
//
// 不设为无限：缺口核对是 best-effort，预算耗尽仍要如实放弃本轮（不下任何虚假结论）。
const gapVisibilityBudget = 6 * time.Second

// gapVisibilityBudgetForTest 让用例能把预算收紧到毫秒级，避免「预算内不可见」
// 那条用例真的等满 6 秒。生产路径始终走 gapVisibilityBudget。
var gapVisibilityBudgetForTest = gapVisibilityBudget

func (m *Manager) gapVisibilityBudgetValue() time.Duration {
	if gapVisibilityBudgetForTest > 0 {
		return gapVisibilityBudgetForTest
	}
	return gapVisibilityBudget
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
	// 按天分组只需一次：重试轮次只重复「向 VL 查询」，不重复分组与规范化。
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
	ctx, cancel := context.WithTimeout(parent, m.gapVisibilityBudgetValue())
	defer cancel()
	// **退避重试，而不是「查一次就定论」**（2026-11-02 真机缺陷修复）。
	//
	// 事故形态（FR-498 长稳 + 干净单源实验双重实测）：
	//   05:34:52 投影写入结束 gen=projection-1 events=6 verified=6 status=200   ← 写成功
	//   05:34:52 投影校验查询 events=6 window=[21:34:50Z, 21:34:53Z]
	//   05:34:52 WARN 本地段有、VL 查不到 missingCount=6 visibleCount=0         ← 查空
	//   同一个 event_id 事后直查 VL：查到 9 条（projection-1/3/5/7/9/11/13/15/17），
	//   且它在 05:34:52 → 05:35:23 的 31 秒内被反复报缺失 9 次。
	// ⇒ **数据确实写进去了，只是「写后约 1.0 秒才可见」**（实测 1.028~1.057s，
	//   与 `_time` 是新是旧无关）。旧实现只查一次 ⇒ 每次都落在 1 秒窗口内 ⇒ 100% 查空
	//   ⇒ 缺口永不消解 ⇒ WAL 不回收 ⇒ 积压丢最旧 ⇒ 产生更多缺口（正反馈）。
	//
	// 语义边界（一个字都没放宽）：
	//   · 「查不到的就不作证据」方向不变 —— 重试只是多问几次，不改变判据；
	//   · 查询失败仍整体放弃（保守方向不变）；
	//   · 全部可见立即返回，**零额外延迟**（正常路径是第一次探测就命中）。
	backoff := m.verifyBackoffMin
	var kept []logtypes.Event
	probes := 0
	for {
		probes++
		var qerr error
		kept, qerr = m.landedVisibleProbeOnce(ctx, source, order, byDay)
		if qerr != nil {
			slog.Warn("缺口消解前核对 VL 可见性失败：本轮不消解任何缺口（下轮重试）",
				"source", source.LogSourceID, "probes", probes, "error", qerr)
			return nil
		}
		if len(kept) == len(landed) {
			// 全部可见：立即返回（热路径，无额外等待）。
			return kept
		}
		select {
		case <-ctx.Done():
			// 预算内未全部可见：如实保留缺口（不消解），并记录探测次数便于现场判断
			// 「是可见延迟超预算」还是「数据真的没进 VL」。
			slog.Warn("缺口核对在预算内未等到全部可见，本轮只消解已可见的部分（缺口其余部分保持可见）",
				"source", source.LogSourceID, "probes", probes,
				"landedCount", len(landed), "visibleCount", len(kept),
				"budget", gapVisibilityBudget)
			return kept
		case <-time.After(backoff):
		}
		backoff = nextVerifyBackoff(backoff, m.verifyBackoffMax)
	}
}

// landedVisibleProbeOnce 执行**一次**可见性核对：按天向 VL 查这批事件里哪些真的查得到，
// 返回可见的那些（顺序即 landed 的顺序）。
//
// ## 2026-11-02 简化：不再读「已发布代次白名单」
//
// 旧实现要先取该天 catalog 的 `PublishedProjection`（`CoverageComplete` + 代次白名单 +
// `ClosedVisibleSeq` 水位），再用这三样构造查询。它们是「多代次重写 + 按批次过滤」那套
// 机制的产物：同一事件在 VL 里堆 N 份，不按白名单/水位过滤就会查重。
//
// 简化后一批数据**只写一次**（event_id 幂等），不再有代次与发布水位。若仍要求白名单，
// 则「投递成功但未发布」的源永远拿不到证据 ⇒ 缺口永不消解 ⇒ WAL 不回收 ⇒ 源被钉死。
// 因此改为**直接按 event_id 问 VL**：「这批事件在不在」本来就是唯一要问的问题。
//
// 保守方向不变：查不到、查询失败、取不到客户端 —— 一律不作证据（不消解）。
func (m *Manager) landedVisibleProbeOnce(ctx context.Context, source SourceConfig, order []string, byDay map[string][]logtypes.Event) ([]logtypes.Event, error) {
	// **跨天也要并发**（2026-11-02 第二处修复）。
	//
	// 现场形态（60 源 × 15 行/s 长稳 r1 实测）：
	//   缺口核对报 `probes=1 error="context deadline exceeded"` —— **首次探测就把预算耗光**。
	//   原因：一次探测要按天串行遍历 `order` 里的**每一天**（多天累积时会有好几天），
	//   每天内再按 12KB selector 切成 ~180 条/片。单片的真实耗时实测 0.8~1.6s
	//   （`投影校验慢查询 elapsedMs=1578` 是生产同规格读数）⇒ 4 天 × 2 片 ÷ 并发 4
	//   在 6s 预算内就是紧平衡，稍有抖动即超时。
	//
	// 为什么跨天并发是安全的：各天的查询用**各自的** `daySource`（含各自的 UTCDay）
	// 与**各自的** client 路由，彼此没有共享可变状态；结果按 order 顺序合并即可。
	//
	// 并发度仍复用 `verifyChunkConcurrency`（默认 4），与天内的切片并发同一旋钮。
	type dayResult struct {
		idx     int
		visible []logtypes.Event
	}
	dayResults := make([]dayResult, len(order))
	dayConc := m.verifyChunkConcurrency
	if dayConc <= 0 {
		dayConc = defaultVerifyChunkConcurrency
	}
	if dayConc > len(order) {
		dayConc = len(order)
	}
	if dayConc < 1 {
		dayConc = 1
	}
	daySem := make(chan struct{}, dayConc)
	var dayWG sync.WaitGroup
	var dayMu sync.Mutex
	var dayErr error
	for idx, day := range order {
		// **必须把 UTCDay 设成该事件所属天**：查询时间窗会被夹在「源当前天」内，
		// 若沿用源当前天，跨天缺口的时间窗会落在错误的一天 ⇒ 恒查不到
		// ⇒ 事件被判「不可见」⇒ 缺口永远无法消解 ⇒ 阻塞 ResumeAcquire ⇒ 源暂停。
		daySource := source
		daySource.UTCDay = day
		rec, _ := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: day})
		client, qerr := m.publishedEvidenceClient(daySource, rec)
		if qerr != nil || client == nil {
			return nil, fmt.Errorf("ingest: 缺口核对目标不可读: %w", qerr)
		}
		candidates := byDay[day]
		dayWG.Add(1)
		go func(idx int, daySource SourceConfig, client *vlsup.Client, candidates []logtypes.Event) {
			defer dayWG.Done()
			select {
			case daySem <- struct{}{}:
			case <-ctx.Done():
				dayMu.Lock()
				if dayErr == nil {
					dayErr = ctx.Err()
				}
				dayMu.Unlock()
				return
			}
			defer func() { <-daySem }()
			batchVisible, qerr := m.visibleEventsInVL(ctx, client, daySource, candidates)
			if qerr != nil {
				dayMu.Lock()
				if dayErr == nil {
					dayErr = qerr
				}
				dayMu.Unlock()
				return
			}
			dayResults[idx] = dayResult{idx: idx, visible: batchVisible}
		}(idx, daySource, client, candidates)
	}
	dayWG.Wait()
	if dayErr != nil {
		return nil, dayErr
	}
	visible := make([]logtypes.Event, 0, 16)
	for idx := range order {
		visible = append(visible, dayResults[idx].visible...)
	}
	visibleSet := make(map[string]struct{}, len(visible))
	for _, ev := range visible {
		visibleSet[ev.EventID] = struct{}{}
	}
	kept := make([]logtypes.Event, 0, len(visible))
	var missing []string
	for _, ev := range byDay0(byDay, order) {
		if _, ok := visibleSet[ev.EventID]; ok {
			kept = append(kept, ev)
			continue
		}
		if len(missing) < 3 {
			missing = append(missing, ev.EventID)
		}
	}
	if len(kept) < len(byDay0(byDay, order)) {
		slog.Debug("本轮缺口核对仍有事件在 VL 查不到（可能是写后可见延迟，退避重试中）",
			"source", source.LogSourceID, "sampleEventIDs", missing)
	}
	return kept, nil
}

// byDay0 按 order 展平分组事件（保持 landed 的原始顺序，便于调用方对齐）。
func byDay0(byDay map[string][]logtypes.Event, order []string) []logtypes.Event {
	out := make([]logtypes.Event, 0, 16)
	for _, day := range order {
		out = append(out, byDay[day]...)
	}
	return out
}

// visibleEventsInVL 用**一次**查询取出这批事件里在 VL 可见的那些。
//
// 分批上限：单批**按查询字节**切（不是按条数），因为单条 selector 的长度取决于 event_id
// 与固定前缀，而 VL 的 query 文本上限是**字节**（实测固定前缀 + 512 条 id 会超过 16KiB）。
// 切片上限取 `scopedProjectionQueryMaxBytes`（12KB），与写路径的逐字段校验同一口径。
//
// 源标识仍必须带：不同源完全可能撞出相同 event_id（event_id 的稳定性口径不跨源保证），
// 不带它会把「另一个源写了同一 id」误当成「本源的这条已经在里面」。
func (m *Manager) visibleEventsInVL(ctx context.Context, client *vlsup.Client, source SourceConfig,
	events []logtypes.Event) ([]logtypes.Event, error) {
	if client == nil {
		return nil, errors.New("ingest: 缺口核对客户端不可用")
	}
	if len(events) == 0 {
		return nil, nil
	}
	// 按字节切成若干片（每片 ≤ 单个查询的字节安全界）。
	spans := m.eventIDsQuerySpans(source, events)
	out := make([]logtypes.Event, 0, len(events))
	// 只有一片时走串行路径：绝大多数调用只有 1 片，为它起 goroutine 纯属白付调度成本。
	total := len(spans)
	if total <= 1 {
		visibleSet, err := m.eventIDsVisibleInVL(ctx, client, source, events)
		if err != nil {
			return nil, err
		}
		for _, ev := range events {
			if _, ok := visibleSet[ev.EventID]; ok {
				out = append(out, ev)
			}
		}
		return out, nil
	}
	// **切片必须并发查**（2026-11-02 修）。
	//
	// 事故形态（60 源 × 15 行/s 实测，与长稳同规格）：
	//   缺口核对的 selector 被 12KB 上限切成 180 条/片；一次核对待查 3778 条 ⇒ **21 片**。
	//   原实现串行逐片查询 ⇒ 单片一个 HTTP 往返 ⇒ 一轮核对就要 21 个往返，
	//   任何预算（3s / 6s / 30s）都会被单次核对的串行往返吃掉：
	//   实测 154 次 `context deadline exceeded`、20 次「预算内未等到全部可见」。
	//   而这些是**同一批待查事件**，彼此完全独立 —— 串行没有任何语义上的理由。
	//
	// 并发度复用既有的 `verifyChunkConcurrency`（默认 4），不新造并行旋钮。
	// 语义不变：仍然是「查到哪些算哪些」，查询失败仍整体返回错误。
	concurrency := m.verifyChunkConcurrency
	if concurrency <= 0 {
		concurrency = defaultVerifyChunkConcurrency
	}
	if concurrency > total {
		concurrency = total
	}
	results := make([]map[string]bool, total)
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for idx := 0; idx < total; idx++ {
		start, end := spans[idx][0], spans[idx][1]
		wg.Add(1)
		go func(idx, start, end int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				mu.Lock()
				if firstErr == nil {
					firstErr = ctx.Err()
				}
				mu.Unlock()
				return
			}
			defer func() { <-sem }()
			visibleSet, err := m.eventIDsVisibleInVL(ctx, client, source, events[start:end])
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			results[idx] = visibleSet
		}(idx, start, end)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	// 按原始顺序合并（保持 landed 的顺序语义，调用方按它对齐）。
	for idx := 0; idx < total; idx++ {
		if results[idx] == nil {
			continue
		}
		start, end := spans[idx][0], spans[idx][1]
		for _, ev := range events[start:end] {
			if results[idx][ev.EventID] {
				out = append(out, ev)
			}
		}
	}
	return out, nil
}

// eventIDsQuerySpans 把一批事件按**查询字节预算**切成若干 [start,end) 片。
//
// 为何不能按条数切：单条 selector 的长度取决于 event_id（32~64 字符）与固定前缀，
// 而 VL 对 query 文本的限制是**字节**。实测：512 条切一片已经超过 16KiB 被拒 400。
// 这里用写路径同一个口径——`scopedProjectionQueryMaxBytes`（12KB）——保证每片都能发出。
//
// 边界（保守方向）：单条 id 就超过预算时 **不报错、单独成片**——因为 12KB 是我们在
// 选主上的自设保守值，而 VL 的真实上限更大；单片一条仍会被真实上限保护。
func (m *Manager) eventIDsQuerySpans(source SourceConfig, events []logtypes.Event) [][2]int {
	if len(events) == 0 {
		return nil
	}
	const fixed = 512 // 固定前缀（源标识 + `| fields ...`）的保守占位
	spans := make([][2]int, 0, 1+len(events)/128)
	start, used := 0, fixed
	for i, event := range events {
		cost := len(strconv.Quote(event.EventID)) + 1
		if i > start && used+cost > scopedProjectionQueryMaxBytes {
			spans = append(spans, [2]int{start, i})
			start, used = i, fixed
		}
		used += cost
	}
	spans = append(spans, [2]int{start, len(events)})
	return spans
}

// eventIDsVisibleInVL 向 VL 问一次「这批 event_id 里哪些真的查得到」，返回可见集合。
//
// 查询口径（简化后）：
//
//	event_id:in("a","b",...) AND log_source_id:="…" AND source_generation:="…"
//
// 时间窗夹在该事件所属 UTC 日内（左闭右开推到日末），与写入时的时间口径一致。
//
// 只取 `_time`：本函数只回答「在不在」，不承担内容比对（内容一致性由写入路径
// 的逐字段校验负责，且简化后一批只写一次、不存在同 id 多副本）。
func (m *Manager) eventIDsVisibleInVL(ctx context.Context, client *vlsup.Client, source SourceConfig, events []logtypes.Event) (map[string]bool, error) {
	if len(events) == 0 {
		return map[string]bool{}, nil
	}
	window, err := projectionQueryWindowFor(source, events)
	if err != nil {
		return nil, err
	}
	// 选择器以**事件自身的** source 标识为准，而不是源配置：配置里的 SourceGeneration
	// 与事件里的 Source.SourceGeneration 可能指向不同实例（2026-09-28 生产实测）。
	sourceID, sourceGeneration := source.LogSourceID, source.SourceGeneration
	if v := events[0].Source.LogSourceID; v != "" {
		sourceID = v
	}
	if v := events[0].Source.SourceGeneration; v != "" {
		sourceGeneration = v
	}
	ids := make([]string, 0, len(events))
	for _, event := range events {
		ids = append(ids, strconv.Quote(event.EventID))
	}
	query := "event_id:in(" + strings.Join(ids, ",") + ")" +
		" AND log_source_id:=" + strconv.Quote(sourceID) +
		" AND source_generation:=" + strconv.Quote(sourceGeneration)
	params := url.Values{
		// **要把正文比对的字段一起取回来**（2026-11-02）。
		//
		// 只查 `event_id` 能回答「在不在」，但回答不了「在 VL 里的到底是不是这一条」。
		// 同 id 异内容在任何幂等体系里都必须显式拒绝（旧实现的 `projection content mismatch`），
		// 否则「篡改过的正文」会被当成已落库证据。
		"query": {query + projectionVerifyFields},
		"start": {window.start.UTC().Format(time.RFC3339Nano)},
		"end":   {window.end.UTC().Format(time.RFC3339Nano)},
	}
	if m.verifyLimiter != nil {
		if err := m.verifyLimiter.Acquire(ctx); err != nil {
			return nil, err
		}
		defer m.verifyLimiter.Release()
	}
	// want 是本批的权威内容快照：只有**逐字段一致**的行才算可见。
	want := make(map[string]logtypes.Event, len(events))
	for _, event := range events {
		want[event.EventID] = event
	}
	visible := make(map[string]bool, len(events))
	err = client.Stream(ctx, "/select/logsql/query", params, func(body io.Reader) error {
		scanner := bufio.NewScanner(io.LimitReader(body, 32<<20+1))
		scanner.Buffer(make([]byte, 64*1024), 4<<20)
		for scanner.Scan() {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			var row struct {
				EventID       string `json:"event_id"`
				CanonicalHash string `json:"canonical_content_hash"`
				EventTimeUTC  string `json:"_time"`
				Message       string `json:"_msg"`
				Level         string `json:"level"`
				Stream        string `json:"stream"`
			}
			if json.Unmarshal(scanner.Bytes(), &row) != nil {
				continue
			}
			expected, ok := want[row.EventID]
			if !ok {
				// 不属于本批（窗口宽容导致的邻行）：不作证据。
				continue
			}
			if row.CanonicalHash != expected.CanonicalHash || row.EventTimeUTC != expected.EventTimeUTC ||
				row.Message != expected.Message || row.Level != expected.Level || row.Stream != expected.Stream {
				// 同 id 不同内容：**不算可见**（保守方向）。
				continue
			}
			visible[row.EventID] = true
		}
		return scanner.Err()
	})
	if err != nil {
		return nil, err
	}
	return visible, nil
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

// sortUTCDays 对 UTC 日字符串（YYYY-MM-DD）排序后返回新切片（不改入参）。
// 定长 ISO 格式的字典序即时间序，直接 sort.Strings 即可。
func sortUTCDays(days []string) []string {
	out := append([]string(nil), days...)
	sort.Strings(out)
	return out
}
