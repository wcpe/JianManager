// 本文件实现「从 VL 反推分区天集合、补建 Catalog 分区记录」（B1，2026-11-05）。
//
// # 要解决的问题
//
// Catalog 分区记录曾是纯内存，进程重启后归零；而 2026-11-02 简化投递路径之后，
// 记录改由 `ensureCatalogPartition` **只在投递成功时**补建。两者叠加的后果：
//
//	某天数据投递成功 → 补建了记录 → 进程重启 → 记录没了
//	→ 只有「那天又产生新数据」才会补回来
//	→ 那天若已采集完，就**永久**在查询侧「不存在」（VL 里行还在，没人知道去查）
//
// 生产实证：`inst:318` 的 2026-11-01 有 2880 行、2026-11-02 有 1746 行，
// 查询侧全部报 `NO_CATALOG_RECORD`。
//
// journal 落盘（`catalog.BatchedStore`）只保证**从现在起不再丢**——它修不了
// 「之前从来没被写进 journal」的天。生产那个 journal 文件只写到 2026-10-30。
//
// # 为什么从 VL 反推是正当的
//
// `catalog/types.go` 的契约写着：「**目录登记：物理扫描是输入，权威角色由 Catalog 决定。**」
//
// 所以「这些天在 VL 里有没有数据」是**物理事实（输入）**，而 owner / generation /
// dirID 仍完全由 `Catalog.WriteAdmission` 决定（**权威**）。本文件不发明任何权威，
// 只把已经存在于 VL 里的事实登记进来。这与 `ensureCatalogPartition` 的形状逐字一致。
//
// # 四条硬红线（改动前必须先读）
//
//  1. **天桶只由 VL 给**：用 `stats by (_time:1d)`，不在 Go 里算天界。
//     用错基准归天正是前三起事故的根因（见 `utcday` 包注释）。
//  2. **幂等且绝不覆盖**：已有记录（journal 重放来的、迁移到 COLD 的）一律跳过。
//     把迁移到 COLD 的分区强行改回 HOT 比「查不到」更严重——那是数据面破坏。
//  3. **有界**：回溯窗口可配（`log_catalog.backfill_days`，默认 30），
//     且单次运行有总查询数上限；不做无界历史回溯。
//  4. **可观测**：补建/跳过/失败三类计数都打结构化日志，绝不静默。
//
// # 为什么放在启动期异步跑
//
// 不能放在查询路径上：那会让每次查询都多一次 VL 聚合查询，并引入不确定延迟。
// 启动期跑一次即可；跑之前查询侧对历史天仍如实报 `NO_CATALOG_RECORD`（不粉饰），
// 跑完之后自然能查到。
package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/utcday"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// 补建任务的默认上界。
const (
	// DefaultCatalogBackfillDays 是回溯窗口默认值（天）。
	//
	// # 2026-11-05 订正（原值 30，依据是过时的假设）
	//
	// 原注释写「`-search.maxQueryTimeRange` 是 31d，所以窗口取 30 是可查询范围内最宽的一档」。
	// **这个前提已经不成立**：本仓 VL 的实际启动参数是
	//
	//	-retentionPeriod=180d  -search.maxQueryTimeRange=4344h(=181d)
	//
	// 二者的关系是「maxQueryTimeRange = retention + 1d」（见 vlsup.BuildArgs）。
	// 按 30 天回搜的实际后果，生产实测：
	//
	//	旧 journal 有 388 个分区，hits 只补回 300 个
	//	缺的 88 个全部落在 2026-09-20 ~ 2026-10-06（早于 30 天窗口起点 10-07）
	//	而 VL 里那几天**确实有数据**（9-20: 11562 条、9-21: 29422 条）
	//
	// ⇒ 窗口设小了不是「保守」，而是**直接丢掉能查到的历史**。
	//
	// 现在取 180 天，与默认 retention 同量级；仍受 maxQueryTimeRange(181d) 约束，
	// 因此不会撞 400。要更长历史再调 retention 与该值即可。
	DefaultCatalogBackfillDays = 180

	// catalogBackfillMaxNamespaces 是单次运行最多处理的命名空间数。
	// 保留期外的 namespace 不会产生查询；这只是给「源数异常膨胀」一个硬上界。
	catalogBackfillMaxNamespaces = 512

	// catalogBackfillQueryTimeout 是单个 namespace 的聚合查询超时。
	//
	// 聚合查询要扫 30 天的行数统计，比逐行查询便宜得多，但仍是重查询；
	// 超时后**跳过该 namespace 并记账**（不阻断其它 namespace，也不假装成功）。
	catalogBackfillQueryTimeout = 45 * time.Second

	// catalogBackfillMaxRange 是 VL 单次查询允许的最大时间跨度。
	//
	// 2026-11-05 订正：原值 30d，注释写「超出会被 400 拒绝（-search.maxQueryTimeRange）」——
	// 依据的前提是那个上限为 31d。实际启动参数是 `-search.maxQueryTimeRange=4344h`（=181d，
	// 由 retention(180d)+1d 派生，见 vlsup.BuildArgs），所以 30d 是**自己加的限制**，
	// 不是 VL 的要求。
	//
	// 但仍保留切窗：单次聚合扫描跨度越大，单查询内存与超时风险越高。
	// 取 90d 作为切窗粒度——既低于 VL 上限（181d），又比 30d 少一半往返。
	catalogBackfillMaxRange = 90 * 24 * time.Hour
)

// CatalogBackfillStats 是一次补建运行的读数（供日志与测试断言）。
//
// 三个计数刻意分开：运维需要能区分「补回了 9 天」与「9 天都有记录所以全跳过」
// 与「查询失败所以什么都没做」——三者含义完全不同。
type CatalogBackfillStats struct {
	// Namespaces 是实际发起查询的命名空间数。
	Namespaces int
	// DaysSeen 是从 VL 返回的天桶总数（去重前）。
	DaysSeen int
	// Created 是本次补建的分区记录数。
	Created int
	// SkippedExisting 是已有记录而跳过的天数（幂等证据）。
	SkippedExisting int
	// SkippedUnwritable 是 WriteAdmission 拒绝的天数（物理日冻结等）。
	SkippedUnwritable int
	// Failed 是查询或写 journal 失败的次数。
	Failed int
	// QueryErrors 记录各 namespace 的查询失败原因（最多保留 8 条，避免日志爆炸）。
	QueryErrors []string
}

// String 渲染成一行可读摘要（日志用）。
func (s CatalogBackfillStats) String() string {
	return fmt.Sprintf("namespaces=%d days=%d created=%d skipped_existing=%d skipped_unwritable=%d failed=%d",
		s.Namespaces, s.DaysSeen, s.Created, s.SkippedExisting, s.SkippedUnwritable, s.Failed)
}

// backfillDaysWindow 把「回溯 N 天」算成 [from, to) —— 以 now 所在 UTC 日为准。
//
// 用 `utcday` 而不是自己加减 24h：这是全仓唯一天界实现，前三起事故都是「多份归天
// 实现各自容错」造成的。
func backfillDaysWindow(now time.Time, days int) (time.Time, time.Time) {
	if days <= 0 {
		days = DefaultCatalogBackfillDays
	}
	todayStart, _, ok := utcday.Bounds(utcday.Of(now))
	if !ok {
		// 理论上不可能（Of 必然产出合法日期）；保守退化为「按 24h 回退」。
		return now.Add(-time.Duration(days) * 24 * time.Hour), now
	}
	// 含今天在内共 days 天。
	return todayStart.Add(-time.Duration(days-1) * 24 * time.Hour), todayStart.Add(24 * time.Hour)
}

// enumerateVLDays 向 VL 询问「该 namespace 下有哪些 UTC 天有数据」，返回升序天列表。
//
// # 为什么用 `stats by (_time:1d)`
//
// 它让 **VL 自己**做天桶切分。在 Go 里 parse 时间再加减 24h 切天正是历次事故的
// 根因（时区/DST/日界口径各自为政）。这里的 `_time` 是 UTC 天桶的起点，直接取其日期部分。
//
// # 为什么按 source 逐个查、再合并
//
// Catalog 的键是 `(StorageNamespace, UTCDay)`，但「有没有数据」只能按 VL 行上的
// `log_source_id` 问。一个 namespace 可能对应多个源（instance 的 file/stdout/stderr），
// 所以先按源查、再按 namespace 取并集。
//
// # 失败即放弃该 namespace
//
// 查询失败/超时/解析失败一律返回错误，调用方记 `Failed` 并**不补建**。
// 方向与既有一致：宁可继续「查不到」，也不凭猜测写一条记录。
func enumerateVLDays(ctx context.Context, client *vlsup.Client, sourceID string, from, to time.Time) ([]string, error) {
	if client == nil {
		return nil, fmt.Errorf("ingest: 补建分区需要可读的 VL 客户端")
	}
	if strings.TrimSpace(sourceID) == "" {
		return nil, fmt.Errorf("ingest: 补建分区需要非空 log_source_id")
	}
	if !to.After(from) {
		return nil, fmt.Errorf("ingest: 补建分区时间窗为空 [%s, %s)", from, to)
	}
	// 源前缀匹配：`inst:318/file` 的实例源可能以 `/stdout`、`/stderr` 结尾，
	// 而 VL 的 `:=` 是精确匹配。用 `*` 前缀通配覆盖同源全部分支。
	//
	// 引号是必须的：source id 里含 `:` 与 `/`，不加引号会被 logsql 当语法。
	selector := fmt.Sprintf("log_source_id:%s*", strconv.Quote(sourceID))
	query := selector + " | stats by (_time:1d) count() | sort by (_time)"

	// 按 VL 的单查询时间跨度上限切窗（30d 窗口边界可能刚好越界）。
	var days []string
	seen := make(map[string]struct{})
	for windowStart := from; windowStart.Before(to); {
		windowEnd := windowStart.Add(catalogBackfillMaxRange)
		if windowEnd.After(to) {
			windowEnd = to
		}
		params := url.Values{
			"query": {query},
			"start": {windowStart.UTC().Format(time.RFC3339Nano)},
			"end":   {windowEnd.UTC().Format(time.RFC3339Nano)},
		}
		err := client.Stream(ctx, "/select/logsql/query", params, func(body io.Reader) error {
			dec := json.NewDecoder(io.LimitReader(body, 8<<20))
			for {
				var row struct {
					// VL 的 day 桶在 `_time` 里（形如 `2026-11-02T00:00:00Z`）。
					Time string `json:"_time"`
				}
				if decodeErr := dec.Decode(&row); decodeErr != nil {
					if decodeErr == io.EOF {
						return nil
					}
					return fmt.Errorf("ingest: 解析 VL 天桶失败: %w", decodeErr)
				}
				// 天桶起点 → 日期。用 utcday 归天（不自己格式化）。
				day := utcday.OfEventTime(row.Time)
				if day == "" {
					continue
				}
				if _, dup := seen[day]; dup {
					continue
				}
				seen[day] = struct{}{}
				days = append(days, day)
			}
		})
		if err != nil {
			return nil, fmt.Errorf("ingest: 查询 VL 天桶失败（source=%s）: %w", sourceID, err)
		}
		windowStart = windowEnd
	}
	return utcday.Sorted(days), nil
}

// CatalogBackfillOptions 是补建任务的输入。
type CatalogBackfillOptions struct {
	// Days 是回溯窗口（天）。<=0 取 DefaultCatalogBackfillDays。
	Days int
	// Now 便于测试注入时钟；零值取 time.Now()。
	Now time.Time
}

// BackfillCatalogPartitionsFromVL 从 VL 反推天集合，为缺失的天补建 Catalog 分区记录。
//
// 幂等：已有记录一律跳过（见文件头的红线 2）。可重复调用。
//
// 返回的 stats 即使有失败也有意义（部分成功），调用方据此打日志；
// 只有「完全没有可读客户端」这类整体性错误才返回 error。
func (m *Manager) BackfillCatalogPartitionsFromVL(ctx context.Context, opts CatalogBackfillOptions) (CatalogBackfillStats, error) {
	var stats CatalogBackfillStats
	if m == nil || m.cat == nil {
		return stats, fmt.Errorf("ingest: 补建分区需要 Catalog")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	days := opts.Days
	if days <= 0 {
		days = DefaultCatalogBackfillDays
	}
	from, to := backfillDaysWindow(now, days)

	// —— 收集 (namespace → 该命名空间下的源 id 集合) ——
	//
	// 用已登记的源而不是「VL 里的所有 namespace」：后者要求一次全库聚合查询，
	// 既贵又会把别的 Worker 的命名空间拉进来。已登记的源就是「本节点负责的东西」。
	m.mu.Lock()
	byNamespace := make(map[string][]SourceConfig, len(m.sources))
	for _, src := range m.sources {
		if src.StorageNamespace == "" || src.LogSourceID == "" {
			continue
		}
		byNamespace[src.StorageNamespace] = append(byNamespace[src.StorageNamespace], src)
	}
	m.mu.Unlock()

	if len(byNamespace) == 0 {
		// 启动早期还没登记任何源：不是错误，如实返回零计数（下次再来）。
		return stats, nil
	}
	namespaces := make([]string, 0, len(byNamespace))
	for ns := range byNamespace {
		namespaces = append(namespaces, ns)
	}
	sort.Strings(namespaces)
	if len(namespaces) > catalogBackfillMaxNamespaces {
		slog.Warn("补建分区：命名空间数超过单次上限，本轮只处理前 N 个（下一轮继续）",
			"total", len(namespaces), "limit", catalogBackfillMaxNamespaces)
		namespaces = namespaces[:catalogBackfillMaxNamespaces]
	}

	for _, ns := range namespaces {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		// 该 namespace 在 VL 里有数据的所有天（多个源取并集）。
		daySet := make(map[string]struct{})
		queried := false
		for _, src := range byNamespace[ns] {
			client, _, clientErr := m.clientForSource(src)
			if clientErr != nil || client == nil {
				stats.Failed++
				if len(stats.QueryErrors) < 8 {
					stats.QueryErrors = append(stats.QueryErrors,
						fmt.Sprintf("%s: 取 VL 客户端失败: %v", ns, clientErr))
				}
				continue
			}
			queryCtx, cancel := context.WithTimeout(ctx, catalogBackfillQueryTimeout)
			days, queryErr := enumerateVLDays(queryCtx, client, src.LogSourceID, from, to)
			cancel()
			if queryErr != nil {
				stats.Failed++
				if len(stats.QueryErrors) < 8 {
					stats.QueryErrors = append(stats.QueryErrors, fmt.Sprintf("%s/%s: %v", ns, src.LogSourceID, queryErr))
				}
				continue
			}
			queried = true
			for _, day := range days {
				daySet[day] = struct{}{}
			}
		}
		if !queried {
			// 该 namespace 下没有一个源查成功：不补建（宁可继续查不到，也不猜）。
			continue
		}
		stats.Namespaces++

		orderedDays := make([]string, 0, len(daySet))
		for day := range daySet {
			orderedDays = append(orderedDays, day)
		}
		orderedDays = utcday.Sorted(orderedDays)
		stats.DaysSeen += len(orderedDays)

		for _, day := range orderedDays {
			// **幂等且不覆盖**（红线 2）：已有记录一律跳过，读一次 Get 就返回。
			key := catalog.PartitionKey{StorageNamespace: ns, UTCDay: day}
			if _, exists := m.cat.Get(key); exists {
				stats.SkippedExisting++
				continue
			}
			if err := m.ensureCatalogPartitionForDay(ns, day, byNamespace[ns][0]); err != nil {
				// WriteAdmission 拒绝（物理日冻结等）与写 journal 失败都计入 Failed；
				// 具体原因由 ensureCatalogPartitionForDay 打日志。
				stats.Failed++
				continue
			}
			// ensureCatalogPartitionForDay 内部也可能发现记录已存在（并发补建），
			// 用 Get 复核一下再计数，避免把「跳过」报成「补建」。
			if _, exists := m.cat.Get(key); exists {
				stats.Created++
			} else {
				stats.SkippedUnwritable++
			}
		}
	}

	return stats, nil
}

// ensureCatalogPartitionForDay 为**指定的 (namespace, day)** 补建一条最小分区记录。
//
// 形状与 `ensureCatalogPartition` 逐字一致（owner/dirID 取自 `WriteAdmission`、
// 带非 nil 空投影、走 AppendJournal），差别只是分区键来自**事件所属天**而不是
// `source.UTCDay`。抽出这个独立函数是因为补建路径需要显式指定 day。
//
// 返回 error 表示「没建成」（已有记录不算失败——调用方已先查过）。
func (m *Manager) ensureCatalogPartitionForDay(namespace, day string, source SourceConfig) error {
	if m == nil || m.cat == nil {
		return fmt.Errorf("ingest: 补建分区需要 Catalog")
	}
	if namespace == "" || day == "" {
		return fmt.Errorf("ingest: 补建分区需要非空 namespace 与 day")
	}
	key := catalog.PartitionKey{StorageNamespace: namespace, UTCDay: day}
	target, err := m.cat.WriteAdmission(key)
	if err != nil {
		slog.Warn("补建分区被拒绝（查询侧仍看不到该天）",
			"namespace", namespace, "utcDay", day, "error", err)
		return err
	}
	if !target.OK {
		slog.Warn("补建分区被拒绝（无写目标）",
			"namespace", namespace, "utcDay", day, "reason", target.Reason)
		return fmt.Errorf("ingest: 补建分区无写目标: %s", target.Reason)
	}
	dirID := target.DirID
	if dirID == "" {
		// 与 ensureCatalogPartition / publishSnapshot 的兜底逐字一致。
		dirID = string(target.Owner) + "-" + source.SourceGeneration
	}
	rec := catalog.NewStableRecord(key, target.Owner, target.Generation, dirID)
	// 与 ensureCatalogPartition 同样的理由：必须带非 nil 投影（否则
	// querySnapshotLocked 的门禁会把该分区排除），且**刻意不填代次**
	// （简化后行上没有 projection_generation 标签，填假代次会限制在不存在的标签上）。
	rec.PublishedProjection = &catalog.PublishedProjection{
		QueryLocationDirID: dirID,
		QueryGeneration:    target.Generation,
	}
	if err := m.cat.AppendJournal(catalog.JournalEntry{Key: key, Record: rec}); err != nil {
		slog.Warn("补建分区写 journal 失败（查询侧仍看不到该天）",
			"namespace", namespace, "utcDay", day, "error", err)
		return err
	}
	return nil
}

// RunCatalogBackfillOnStartup 在启动路径上**异步**跑一次补建，不阻塞 Worker 启动。
//
// 失败不拒启（与 journal 装配的处理一致）：补建是「让历史天重新可查」的补救动作，
// 它失败不该拖坠整个 Worker。但必须 ERROR 级留痕——否则运维会以为已经补过了。
//
// 幂等由 `BackfillCatalogPartitionsFromVL` 保证，重复调用不会重复建。
func (m *Manager) RunCatalogBackfillOnStartup(parent context.Context, opts CatalogBackfillOptions) {
	if m == nil {
		return
	}
	go func() {
		started := time.Now()
		stats, err := m.BackfillCatalogPartitionsFromVL(parent, opts)
		if err != nil {
			slog.Error("启动补建 Catalog 分区失败（历史天在查询侧仍不可查）",
				"stats", stats.String(), "elapsedMs", time.Since(started).Milliseconds(), "error", err)
			return
		}
		if stats.Failed > 0 {
			slog.Warn("启动补建 Catalog 分区部分失败（失败的天仍不可查）",
				"stats", stats.String(), "elapsedMs", time.Since(started).Milliseconds(),
				"queryErrors", stats.QueryErrors)
			return
		}
		slog.Info("启动补建 Catalog 分区完成",
			"stats", stats.String(), "elapsedMs", time.Since(started).Milliseconds(),
			"backfillDays", func() int {
				if opts.Days <= 0 {
					return DefaultCatalogBackfillDays
				}
				return opts.Days
			}())
	}()
}
