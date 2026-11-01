package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	// 为什么这是**唯一的不一致点**：同一个「查 VL 可见性」的动作，投递路径的
	// `verifyProjectionChunk` 有完整的 `nextVerifyBackoff` 退避环（其注释明确写着
	// 「封顶 500ms 后探测点变为 0/0.2/0.6/1.1 s，数据一可见就命中并返回」），
	// 而本函数此前没有任何重试。现在两者同源。
	//
	// 语义边界（一个字都没放宽）：
	//   · 「查不到的就不作证据」方向不变 —— 重试只是多问几次，不改变判据；
	//   · 查询失败仍整体放弃（保守方向不变）；
	//   · 全部可见立即返回，**零额外延迟**（正常路径是第一次探测就命中）。
	//
	// 为什么预算取 `gapVisibilityBudget`（6s）而不是既有的 `verifyBackoffMax*4+1s`（3s）：
	// 那 3 秒是给「退避探测」设计的固定预算，实测可见延迟 1.0s 时四个探测点
	// （0/0.2/0.6/1.1）刚好够；但只要 VL 因负载把可见延迟推到 1.5~3s 就会再次落空。
	// 本路径已异步化（见 gap_resolve_worker.go），拉长预算不再阻塞投递主链。
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

// landedVisibleProbeOnce 执行**一次**可见性核对：按天取该天已发布白名单，向 VL 查这批事件里
// 哪些可见，返回可见的那些（顺序即 landed 的顺序）。
//
// 返回 error 表示「查询这一动作失败」（网络/超时/鉴权等）——调用方据此整体放弃本轮，
// 与历史语义一致（查询不确定时不作任何证据）。
func (m *Manager) landedVisibleProbeOnce(ctx context.Context, source SourceConfig, order []string, byDay map[string][]logtypes.Event) ([]logtypes.Event, error) {
	// **必须按每个事件所属的分区取白名单**，不能统一用 `source.UTCDay`：
	// 缺口跨越历史天（生产实测缺口在 10-21，而源的当前天可能是今天），
	// 用当前天分区的白名单去查历史天的事件必然查不到，会把「可见」误判成「不可见」。
	//
	// 每轮探测都重新取（不跨轮缓存）：重试窗口内白名单可能被并发发布更新，
	// 用旧白名单会把「刚变可见」误判成「仍不可见」。catalog.Get 是内存读，成本可忽略。
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
	// **跨天也要并发**（2026-11-02 第二处修复）。
	//
	// 现场形态（60 源 × 15 行/s 长稳 r1 实测）：
	//   缺口核对报 `probes=1 error="context deadline exceeded"` —— **首次探测就把预算耗光**。
	//   原因：一次探测要按天串行遍历 `order` 里的**每一天**（多天累积时会有好几天），
	//   每天内再按 12KB selector 切成 ~180 条/片。单片的真实耗时实测 0.8~1.6s
	//   （`投影校验慢查询 elapsedMs=1578` 是生产同规格读数）⇒ 4 天 × 2 片 ÷ 并发 4
	//   在 6s 预算内就是紧平衡，稍有抖动即超时。
	//
	// 为什么跨天并发是安全的：各天的查询用**各自的** `daySource`（含各自的 UTCDay 与白名单）
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
			return nil, fmt.Errorf("ingest: 缺口核对目标不可读: %w", qerr)
		}
		closed := publishedEvidenceClosed(rec, daySource)
		candidates := make([]logtypes.Event, 0, len(byDay[day]))
		for _, event := range byDay[day] {
			if event.Record.End <= closed {
				candidates = append(candidates, event)
			}
		}
		dayWG.Add(1)
		go func(idx int, daySource SourceConfig, client *vlsup.Client, gens []string, candidates []logtypes.Event) {
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
			batchVisible, qerr := m.visibleEventsInVL(ctx, client, daySource, gens, candidates)
			if qerr != nil {
				dayMu.Lock()
				if dayErr == nil {
					dayErr = qerr
				}
				dayMu.Unlock()
				return
			}
			dayResults[idx] = dayResult{idx: idx, visible: batchVisible}
		}(idx, daySource, client, gens, candidates)
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
	// 切片数 <= 1 时走原串行路径：绝大多数调用只有 1 片（批大小就是 512），
	// 为它起 goroutine 纯属白付调度成本。
	total := (len(events) + batch - 1) / batch
	if total <= 1 {
		_, visibleSet, _, err := m.verifyProjectionQueryWithVisible(ctx, client, source, generations[0],
			events, nil, generations)
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
	// 并发度复用既有的 `verifyChunkConcurrency`（默认 4），与投递路径的分簇校验同一口径，
	// 不新造并行旋钮。语义不变：仍然是「查到哪些算哪些」，查询失败仍整体返回错误。
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
		start := idx * batch
		end := start + batch
		if end > len(events) {
			end = len(events)
		}
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
			_, visibleSet, _, err := m.verifyProjectionQueryWithVisible(ctx, client, source, generations[0],
				events[start:end], nil, generations)
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
		start := idx * batch
		end := start + batch
		if end > len(events) {
			end = len(events)
		}
		for _, ev := range events[start:end] {
			if results[idx][ev.EventID] {
				out = append(out, ev)
			}
		}
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
