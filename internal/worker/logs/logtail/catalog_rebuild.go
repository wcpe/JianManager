package logtail

// 本文件实现「用 VictoriaLogs 的 hits 重建 Catalog 分区目录」。
//
// # 为何需要它（新采集器同样需要）
//
// `PartitionEnsurer` 只在**投递当时**补建分区。它修不了已经发生的事：
// Worker 停机期间、或本功能上线之前写进 VL 的历史天，Catalog 里没有记录 ⇒
// 用户看到「这天没日志」，而 VL 里行是在的。
//
// hits 端点一次请求就能给出「哪些源在哪些天有数据」（实测 372 个 (源,天) / 217ms / 31KB）。
//
// # 权威边界（不可越）
//
// hits 只回答「哪天有数据」（**输入**）。owner / generation / dirID 仍必须由
// `Catalog.WriteAdmission` 决定（**权威**）。本文件不发明任何权威。
//
// # 天界口径
//
// 分区键是 **UTC 日**，`vlsup.HitsBySourceDay` 不传 `offset`（详见该函数注释）。
// 这里再按 UTC 日过滤一道，防止上游改动引入本地时区偏移。
//
// # 只重建「已登记的源」
//
// VL 里可能有探针/历史测试数据。它们不属于平台管理的实例源，写进分区目录会让
// 查询侧为不存在的实例建立覆盖。所以只处理绑定表里出现过的命名空间。

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// SourceDay 是一条「(源, 天) 有数据」事实（与 vlsup.SourceDay 同形）。
//
// 用本包自己的类型而不是 vlsup 的：vlsup 是写/查客户端，本函数只需要三个标量。
// 装配层做一次转换即可（与旧链路 HitsSourceFunc 同一模式）。
type SourceDay struct {
	// LogSourceID 是完整源 id（`inst:318/file`）。
	LogSourceID string
	// UTCDay 是 `2006-01-02` 形态的 UTC 日。
	UTCDay string
	// Weight 是该 (源,天) 的命中权重（只用于日志，不参与判定）。
	Weight uint64
}

// HitsSource 是 hits 重建所需的最小能力面。
type HitsSource func(ctx context.Context, start, end time.Time) ([]SourceDay, error)

// CatalogRebuildOptions 控制重建窗口。
type CatalogRebuildOptions struct {
	// Days 是回溯天数（<=0 用默认 30）。
	Days int
	// Now 是时间基准（零值用 time.Now）。
	Now time.Time
}

// DefaultCatalogRebuildDays 是默认回溯窗口。
//
// 30 天：与 `log_catalog.backfill_days` 的出厂口径一致，且远小于 VL 的
// `search.maxQueryTimeRange`（本机 4344h ≈ 181 天）——不会因窗口过大被拒。
const DefaultCatalogRebuildDays = 30

// CatalogRebuildStats 是重建读数。
type CatalogRebuildStats struct {
	DaysSeen        int
	Created         int
	SkippedExisting int
	SkippedUnbound  int
	Failed          int
}

// RebuildCatalogFromHits 用 hits 重建分区目录（同步，失败返回 error）。
//
// 幂等：已有记录一律不动（它们可能来自迁移/归档，强行改写是数据面破坏）。
func RebuildCatalogFromHits(ctx context.Context, cat *catalog.Catalog, ensurer *PartitionEnsurer,
	bindings *Bindings, hits HitsSource, opts CatalogRebuildOptions) (CatalogRebuildStats, error) {

	var stats CatalogRebuildStats
	if cat == nil || ensurer == nil || hits == nil {
		return stats, nil
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
		days = DefaultCatalogRebuildDays
	}
	from := now.AddDate(0, 0, -days)

	facts, err := hits(ctx, from, now)
	if err != nil {
		return stats, err
	}
	stats.DaysSeen = len(facts)

	// 只重建**绑定表里出现过**的命名空间。
	known := make(map[string]struct{})
	if bindings != nil {
		for _, e := range bindings.Entries() {
			if ns := strings.TrimSpace(e.TargetID); ns != "" {
				known[ns] = struct{}{}
			}
		}
	}

	for _, fact := range facts {
		if ctx.Err() != nil {
			break
		}
		namespace := namespaceOf(fact.LogSourceID)
		if len(known) > 0 {
			if _, ok := known[namespace]; !ok {
				stats.SkippedUnbound++
				continue
			}
		}
		// 天界口径再核一道：只接受严格 UTC 日形态。
		day, derr := normalizeDay(fact.UTCDay)
		if derr != nil {
			stats.Failed++
			continue
		}
		if _, exists := cat.Get(catalog.PartitionKey{StorageNamespace: namespace, UTCDay: day}); exists {
			stats.SkippedExisting++
			continue
		}
		if eerr := ensurer.Ensure(namespace, day); eerr != nil {
			stats.Failed++
			continue
		}
		// Ensure 内部也可能发现记录已存在（并发补建），复核后再计数。
		if _, exists := cat.Get(catalog.PartitionKey{StorageNamespace: namespace, UTCDay: day}); exists {
			stats.Created++
		}
	}
	return stats, nil
}

// RunCatalogRebuildOnStartup 在启动路径上异步跑一次重建，不阻塞启动。
//
// 失败不拒启：重建是「让历史天重新可查」的补救动作，它失败不该拖坠 Worker。
// 但必须 ERROR 级留痕——否则运维会以为已经重建过了。
func RunCatalogRebuildOnStartup(parent context.Context, cat *catalog.Catalog, ensurer *PartitionEnsurer,
	bindings *Bindings, hits HitsSource, opts CatalogRebuildOptions) {

	if cat == nil || ensurer == nil || hits == nil {
		return
	}
	go func() {
		started := time.Now()
		stats, err := RebuildCatalogFromHits(parent, cat, ensurer, bindings, hits, opts)
		if err != nil {
			slog.Error("用 hits 重建 Catalog 分区失败（历史天在查询侧仍不可查）",
				"elapsedMs", time.Since(started).Milliseconds(), "error", err)
			return
		}
		if stats.Failed > 0 {
			slog.Warn("用 hits 重建 Catalog 分区部分失败（失败的天仍不可查）",
				"created", stats.Created, "failed", stats.Failed,
				"skippedExisting", stats.SkippedExisting, "skippedUnbound", stats.SkippedUnbound)
			return
		}
		slog.Info("用 hits 重建 Catalog 分区完成",
			"created", stats.Created, "skippedExisting", stats.SkippedExisting,
			"skippedUnbound", stats.SkippedUnbound,
			"elapsedMs", time.Since(started).Milliseconds())
	}()
}

// normalizeDay 校验并归一化 `2006-01-02`，拒绝已带时区的形态。
func normalizeDay(day string) (string, error) {
	ts, err := time.Parse("2006-01-02", strings.TrimSpace(day))
	if err != nil {
		return "", err
	}
	return ts.Format("2006-01-02"), nil
}
