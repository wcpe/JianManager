package ingest

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// errHitsRebuildNeedsCatalog 表示重建前置不满足（无 Catalog）。
var errHitsRebuildNeedsCatalog = errors.New("ingest: 重建分区需要 Catalog")

// SourceDayFact 是 hits 给的一条「(源, 天) 有数据」事实。
//
// 它是**物理输入**而非权威：owner/generation/dirID 仍由 Catalog.WriteAdmission 决定。
// LogSourceID 是**完整源 id**（`inst:318/file`），UTCDay 是 `2006-01-02` 形态的 UTC 日。
type SourceDayFact struct {
	LogSourceID string
	UTCDay      string
	Weight      uint64
}

// RebuildCatalogPartitionsFromHits 用 VictoriaLogs 的 `hits` 端点重建分区目录。
//
// # 为什么用 hits 取代「journal 落盘 + 30 天回填」
//
// 「哪些源在哪些天有数据」是 VL 已经知道的**物理事实**。此前我们用三层补救去近似它：
//
//	第 1 层  Catalog journal 落盘（与 2026-10-30「机械盘不落盘」裁定冲突）
//	第 2 层  ensureCatalogPartition（只在有新数据写入时补）
//	第 3 层  BackfillCatalogPartitionsFromVL（30 天窗口，每源一次查询）
//
// 三层都建立在「我们自己维护一份副本」这个前提上，而副本一丢就要再加一层补救。
// hits 一次请求给出完整事实（实测 372 个 (源,天) / 217ms / 31KB），不需要副本。
//
// # 权威边界（不可越）
//
// hits 只回答「哪天有数据」（**输入**）。owner / generation / dirID 仍必须由
// `Catalog.WriteAdmission` 决定（**权威**）。这与 `ensureCatalogPartition`、
// `ensureCatalogPartitionForDay` 的形状逐字一致——本函数不发明任何权威。
//
// # 天界口径
//
// 分区键是 **UTC 日**，`vlsup.HitsBySourceDay` 不传 `offset`（详见该函数注释）。
// 这里再按 UTC 日过滤一道，防止上游改动引入本地时区偏移。
//
// # 只重建「已登记的源」
//
// VL 里可能有探针/历史测试数据（实测 11 个 `probe:*`）。它们不属于平台管理的实例源，
// 写进分区目录会让查询侧为不存在的实例建立覆盖。故只处理 `SourceConfigs` 里登记过的
// 命名空间；未登记的直接跳过并计数。
func (m *Manager) RebuildCatalogPartitionsFromHits(ctx context.Context, opts CatalogBackfillOptions) (CatalogBackfillStats, error) {
	var stats CatalogBackfillStats
	if m == nil || m.cat == nil {
		return stats, errHitsRebuildNeedsCatalog
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

	client := m.hitsClient()
	if client == nil {
		// 没有可读的 VL 客户端不算错误：本地未配置 VL 时分区目录保持为空，
		// 查询侧会如实报「无分区记录」而不是粉饰成空成功。
		slog.Warn("无法重建 Catalog 分区：本 Worker 没有可读的 VictoriaLogs 客户端")
		return stats, nil
	}
	facts, err := client.HitsBySourceDay(ctx, from, to)
	if err != nil {
		return stats, err
	}

	// 已登记的命名空间 → 该命名空间下的源配置（用于 dirID 兜底与代次）。
	known := m.registeredSourcesByNamespace()
	stats.DaysSeen = len(facts)
	for _, fact := range facts {
		if ctx.Err() != nil {
			break
		}
		namespace := namespaceOfLogSourceID(fact.LogSourceID)
		source, ok := known[namespace]
		if !ok {
			// 未登记的源（probe:*、其他 Worker 的源）：不建分区。
			stats.SkippedExisting++
			continue
		}
		// 天界口径再核一道：只接受严格 UTC 日形态。
		day, err := normalizeUTCDay(fact.UTCDay)
		if err != nil {
			stats.Failed++
			continue
		}
		if _, exists := m.cat.Get(catalog.PartitionKey{StorageNamespace: namespace, UTCDay: day}); exists {
			// 已有记录（journal 重放来的、迁移过的）：一律不动。
			// 把迁移到 COLD 的分区强行改回 HOT 比「查不到」更严重——那是数据面破坏。
			stats.SkippedExisting++
			continue
		}
		if err := m.ensureCatalogPartitionForDay(namespace, day, source); err != nil {
			stats.Failed++
			if len(stats.QueryErrors) < 8 {
				stats.QueryErrors = append(stats.QueryErrors, namespace+"/"+day+": "+err.Error())
			}
			continue
		}
		stats.Created++
	}
	slog.Info("已用 VictoriaLogs hits 重建 Catalog 分区目录",
		"windowDays", days, "facts", len(facts), "created", stats.Created,
		"skipped", stats.SkippedExisting, "failed", stats.Failed,
		"from", from.Format(time.RFC3339), "to", to.Format(time.RFC3339))
	return stats, nil
}

// RunCatalogHitsRebuildOnStartup 在启动路径上异步重建分区目录，不阻塞启动。
//
// 失败不拒启（与 journal 装配同一纪律）：重建是「让历史天可查」的补救动作，
// 它失败不该拖坠 Worker；但必须 ERROR 留痕，否则运维会以为已经重建过了。
func (m *Manager) RunCatalogHitsRebuildOnStartup(parent context.Context, opts CatalogBackfillOptions) {
	if m == nil {
		return
	}
	go func() {
		started := time.Now()
		stats, err := m.RebuildCatalogPartitionsFromHits(parent, opts)
		if err != nil {
			slog.Error("用 hits 重建 Catalog 分区失败（历史天在查询侧仍不可查）",
				"elapsedMs", time.Since(started).Milliseconds(), "error", err)
			return
		}
		if stats.Failed > 0 {
			slog.Warn("用 hits 重建 Catalog 分区部分失败（失败的天仍不可查）",
				"created", stats.Created, "failed", stats.Failed, "skipped", stats.SkippedExisting,
				"queryErrors", stats.QueryErrors)
			return
		}
		slog.Info("用 hits 重建 Catalog 分区完成",
			"created", stats.Created, "skipped", stats.SkippedExisting,
			"elapsedMs", time.Since(started).Milliseconds())
	}()
}

// namespaceOfLogSourceID 从完整源 id（`inst:318/file`）取分区命名空间（`inst:318`）。
//
// 与 `catalog.PartitionKey.StorageNamespace` 同一口径：源注册时
// `StorageNamespace = binding.TargetID`，而 `LogSourceID = TargetID + "/" + stream`。
// 不含 `/` 的 id（如 `node:1`）原样返回——它本身就是命名空间。
func namespaceOfLogSourceID(logSourceID string) string {
	if i := strings.LastIndex(logSourceID, "/"); i > 0 {
		return logSourceID[:i]
	}
	return logSourceID
}

// normalizeUTCDay 校验并归一化 `2006-01-02`，拒绝已带时区的形态。
func normalizeUTCDay(day string) (string, error) {
	ts, err := time.Parse("2006-01-02", strings.TrimSpace(day))
	if err != nil {
		return "", err
	}
	return ts.Format("2006-01-02"), nil
}

// registeredSourcesByNamespace 返回「已登记的命名空间 → 一个代表性 SourceConfig」。
//
// 同一命名空间可能对应多个流（`inst:318/file`、`inst:318/stdout`），它们的
// StorageNamespace 相同、SourceGeneration 也相同，取任意一个即可用于 dirID 兜底。
func (m *Manager) registeredSourcesByNamespace() map[string]SourceConfig {
	out := make(map[string]SourceConfig, 8)
	m.mu.Lock()
	for _, cfg := range m.state.SourceConfigs {
		if cfg.StorageNamespace == "" {
			continue
		}
		if _, exists := out[cfg.StorageNamespace]; exists {
			continue
		}
		out[cfg.StorageNamespace] = cfg
	}
	m.mu.Unlock()
	return out
}

// hitsClient 返回可用的 hits 客户端；未接线时返回 nil。
//
// 用接口而非具体类型，让 ingest 不必依赖 vlsup 的具体实现（测试可注入假客户端）。
func (m *Manager) hitsClient() HitsSource {
	if m == nil || m.hits == nil {
		return nil
	}
	return m.hits
}

// HitsSource 是 hits 重建所需的唯一能力面。
//
// 用接口而非具体类型，让 ingest 不必依赖 vlsup 的具体实现（测试可注入假客户端）。
type HitsSource interface {
	HitsBySourceDay(ctx context.Context, start, end time.Time) ([]SourceDayFact, error)
}

// HitsSourceFunc 把普通函数适配成 HitsSource，便于在装配点做类型转换
// （vlsup 的 SourceDay 与 ingest 的 SourceDayFact 字段相同但类型不同，
// 让任意一方反向依赖都会成环，故在装配点显式桥接）。
type HitsSourceFunc func(ctx context.Context, start, end time.Time) ([]SourceDayFact, error)

// HitsBySourceDay 实现 HitsSource。
func (f HitsSourceFunc) HitsBySourceDay(ctx context.Context, start, end time.Time) ([]SourceDayFact, error) {
	return f(ctx, start, end)
}

// SetHitsSource 接上 hits 数据源（nil = 不启用重建，行为与改动前一致）。
func (m *Manager) SetHitsSource(src HitsSource) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.hits = src
	m.mu.Unlock()
}
