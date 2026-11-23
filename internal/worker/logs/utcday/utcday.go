// Package utcday 是日志链路的**时间归属唯一实现**：事件属于哪一天、那一天的边界在哪。
//
// # 为什么单独成包
//
// 三个已修的生产缺陷是同一族：**用错了基准去归天**。
//
//   - 「零水位被当成零上界」—— 把 0 当作"没有数据的第 0 天"参与上下界计算；
//   - 「Catalog 记录建在源登记日而非事件日」—— 补建分区键时用 `source.UTCDay`
//     （源**登记那一刻**的日）而不是事件自身的日；
//   - 「无时间戳行的接收时间冻结在进程启动时刻」—— 归一化只在 New() 取一次 now，
//     长驻进程跨过 UTC 午夜后，今天的 RAW 行被归进启动日。
//
// 它们的共同点不是"某处写错了一行"，而是**同一个问题有多个各自为政的答案**。
// 归天逻辑原先散在 ingest 包 5 个文件里（runtime_delivery_pipeline.go 的
// eventUTCDay / utcDayBounds / parseEventTimeUTC、runtime_source.go 的 eventUTCDays /
// utcDayOfEventTime、hot_retention_window.go 的 sortUTCDays），
// 每个都有自己的回退顺序与容错口径，改一处不会提醒你去看另一处。
//
// 抽出来之后有两条硬约束：
//
//  1. **任何"这个事件属于哪一天"的判断都必须经过本包**。调用方不得自己
//     `time.Parse` 后 `Format("2006-01-02")`——那正是过去分叉的来源。
//  2. 本包**不 import** ingest / catalog / pipeline 等任何业务包，只依赖 logtypes。
//     纯函数、无状态、可用假时钟直接测。
//
// # 语义（左闭右开，UTC）
//
// 一天 = `[当天 00:00:00.000000000Z, 次日 00:00:00.000000000Z)`。
// `Bounds` 返回的 End 是 `次日零点 - 1ns`（闭区间表示），与 VL 时间窗的用法一致；
// 判「某天是否整日早于某时刻」用 `dayEnd.Before(boundary)` 恰好等价于
// 「次日零点 <= boundary」，不多滤一天也不少滤。
//
// 时区：本包**只处理 UTC 日**。"按源时区解释日志文本里的 [HH:MM:SS]" 是 normalize
// 的职责（它产出已换算的 `EventTimeUTC`），本包只消费那个已归一化的字符串。
// 这条边界必须守住，否则又会出现"两处各自解释时区"的分叉。
package utcday

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// Layout 是分区键的日期格式（与 catalog.PartitionKey.UTCDay 一致）。
const Layout = "2006-01-02"

// Of 把时刻换算成 UTC 日。零值/零时时刻返回空串（调用方据此判断"没有可用时间"）。
func Of(when time.Time) string {
	if when.IsZero() {
		return ""
	}
	return when.UTC().Format(Layout)
}

// Parse 解析 RFC3339Nano 时刻；空白、不可解析一律 (zero, false)。
//
// **不猜**：解析失败不返回"某个近似值"。调用方要么回退到下一个时间来源，
// 要么如实报错——写一条错天的记录比没有记录更难排查。
func Parse(raw string) (time.Time, bool) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, false
	}
	moment, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, false
	}
	return moment.UTC(), true
}

// OfEventTime 解析 RFC3339 事件时间并换算成 UTC 日；解析失败返回空串。
func OfEventTime(eventTimeUTC string) string {
	when, ok := Parse(eventTimeUTC)
	if !ok {
		return ""
	}
	return Of(when)
}

// Bounds 返回 "2006-01-02" 形式的 UTC 日闭区间 [起, 止]（止 = 次日零点 - 1ns）。
// 格式不符时返回 ok=false，调用方保持原行为（向后兼容）。
func Bounds(day string) (start, end time.Time, ok bool) {
	parsed, err := time.Parse(Layout, day)
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	return parsed, parsed.Add(24*time.Hour - time.Nanosecond), true
}

// WholeDayBefore 报告 UTC 日 `day` 是否**整天**（含最后一纳秒）都早于 `boundary`。
//
// 保守判据（宁可多算一天可发布，绝不误伤仍可见的数据）：
//   - `day` 不可解析 ⇒ false（不因脏数据而丢弃）；
//   - 只有整日都早于边界才 true；**跨越边界的那一天**一律按"未超出"处理。
func WholeDayBefore(day string, boundary time.Time) bool {
	_, end, ok := Bounds(day)
	if !ok {
		return false
	}
	return end.Before(boundary)
}

// EventDay 返回事件所属的 UTC 日，回退顺序为：
// `EventTimeUTC` → `IngestTimeUTC` → `fallbackDay`（源配置的分区日）。
//
// # 为什么必须回退而不是报错
//
// 真实日志行可能没有可解析的语义时间（解析失败或非标准格式）。若此处直接报错，
// 投递/投影路径会失败 → 受管恢复责任无法转移 → reclaim 永不推进 → WAL 保留全部
// 事件（真机 64 源实测：Worker RSS 涨到 2GiB）。事件时间回退是既有契约行为。
//
// # `fallbackDay` 是最后手段，且它有已知陷阱
//
// 它通常是 `SourceConfig.UTCDay`，而那个值是**源被登记那一刻**的 UTC 日
// （只在为空时用 time.Now() 填一次，此后固定）。拿它当归天基准正是
// 「Catalog 记录建在源登记日而非事件日」那个缺陷的成因。
//
// 所以调用方**不要**用它来给真实事件归天；它是给"连 ingest 时间都没有"的
// 退化形态兜底的。正常情况下 IngestTimeUTC 恒非空（归一化必填），本参数用不到。
//
// 三者都没有 ⇒ 返回错误（不猜）。
func EventDay(event logtypes.Event, fallbackDay string) (string, error) {
	for _, raw := range []string{event.EventTimeUTC, event.IngestTimeUTC} {
		if day := OfEventTime(raw); day != "" {
			return day, nil
		}
	}
	if fallbackDay != "" {
		return fallbackDay, nil
	}
	return "", fmt.Errorf("utcday: event %s has no usable event/ingest time or partition day", event.EventID)
}

// EventDays 返回一批事件涉及的**所有** UTC 日（升序去重）。
//
// # 为什么取"所有"而不是第一条
//
// 一批可能跨天：长时间没 poll 会一次读回多天，恢复/重放路径也可能一次带上跨天区间。
// 只取第一天会让其余天仍然查不到（`ensureCatalogPartition` 正是靠它逐天补建分区记录）。
//
// 只看 `EventTimeUTC`（规范化之后必有值），不看 `IngestTimeUTC`——
// 后者是"什么时候写进来的"，与分区归属无关。
func EventDays(events []logtypes.Event) []string {
	seen := make(map[string]struct{}, 2)
	for _, ev := range events {
		if day := OfEventTime(ev.EventTimeUTC); day != "" {
			seen[day] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	days := make([]string, 0, len(seen))
	for day := range seen {
		days = append(days, day)
	}
	return Sorted(days)
}

// Sorted 返回升序副本（不修改入参）。
func Sorted(days []string) []string {
	out := append([]string(nil), days...)
	sort.Strings(out)
	return out
}
