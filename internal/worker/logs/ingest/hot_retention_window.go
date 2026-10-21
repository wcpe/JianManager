package ingest

import (
	"log/slog"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
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

// evidenceWithinHotRetention 从一批「已落库」事件里剔出**热层不可能可见**的天，
// 返回可用作缺口消解证据的子集。
//
// 为什么需要它（2026-10-21 真机缺陷，生产主要命中路径）：`deliver` 有一条
// 「本地已有、无需重写」的分支（`len(toWrite)==0`）。它直接拿 `landed` 去
// `resolveGapsLandedByDelivery`，而 `landed` 只证明「本地段里有这些事件」，
// 不证明「VL 里有」。超出热层保留期的天，VL 对写入返回 200 但**静默丢弃**，
// 本地段却完好保留 —— 于是从未进入 VL 的区间被消解成 `delivery confirmed`。
//
// 生产实测：`inst:317` 的 `[7583541,7585609)`（10-21，2068 字节）被如此消解，
// 而 VL 该源最新只到 7583541；随后 `reclaim` 被推到 `read_pos`，欠账再无信号。
//
// 保守边界：`retention<=0`（未配置）或事件取不到天时**一律保留**，
// 即零配置下行为与改动前逐字一致。
func (m *Manager) evidenceWithinHotRetention(source SourceConfig, events []logtypes.Event) []logtypes.Event {
	if m == nil || len(events) == 0 || m.hotRetention <= 0 {
		return events
	}
	now := time.Now()
	out := make([]logtypes.Event, 0, len(events))
	skippedDays := make(map[string]struct{})
	for _, ev := range events {
		day, err := eventUTCDay(source, ev)
		if err == nil && m.beyondHotRetention(day, now) {
			skippedDays[day] = struct{}{}
			continue
		}
		out = append(out, ev)
	}
	if len(skippedDays) > 0 {
		days := make([]string, 0, len(skippedDays))
		for day := range skippedDays {
			days = append(days, day)
		}
		slog.Warn("投递证据面排除超出热层保留期的事件（不消解其缺口：该区间不可能在 VL 可见）",
			"source", source.LogSourceID, "beyondHotRetentionDays", sortUTCDays(days),
			"hotRetention", m.hotRetention)
	}
	return out
}
