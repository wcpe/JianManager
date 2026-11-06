package logtail

import (
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/utcday"
)

// PartitionEnsurer 保证「写进 VL 的那一天，查询侧能查到」。
//
// # 为什么必须有它（会导致 “明明有日志却查不到”）
//
// 查询入口是 `query.Planner.Plan` → `p.cat.Get(key)`。**没有 Catalog 分区记录 ⇒ 该目标
// 不进视图**（`NO_CATALOG_RECORD`），用户看到的是「这天没日志」。
//
// 旧链路依靠投递时调 `ensureCatalogPartition` 补记录；新采集器绕过了整条投递链，
// 若不同样补一笔，结果就是：VL 里一堆行、日志页一片空白。
// 这与本仓历史上那个「数据进 VL 却查不到」的缺陷是同型。
//
// # 权威边界（不可越）
//
// owner / generation / dirID 必须取自 `Catalog.WriteAdmission`，不能自己编。
// 自己定 owner 会造出「记录写着 HOT，数据却在 COLD」这种更难查的形态。
//
// # 为什么不覆盖已有记录
//
// 已有记录可能来自迁移/历史重建（owner 不同、目录不同），强行改写是**数据面破坏**。
// 只在缺失时补建。
//
// # 为何必须走 journal
//
// 记录一旦建立，查询侧立刻能看到该分区下 VL 里的行。若不经 journal，就会出现
// 「持久日志里没这条记录、内存里却有」—— 重启后消失，而这期间用户已经看到过那份数据。
//
// # 并发
//
// 采集器多 harvester 并发写，同一 (源, 天) 会反复进来。进程内用 claimed 集合去重，
// 避免每批都去 Get 一遭；跨进程由 Catalog 自己的 journal 幂等与 Get 前置检查兼顾。
type PartitionEnsurer struct {
	cat *catalog.Catalog

	mu      sync.Mutex
	claimed map[catalog.PartitionKey]struct{}
}

// NewPartitionEnsurer 创建补建器。cat 为 nil 时所有调用都无操作（未装配查询面）。
func NewPartitionEnsurer(cat *catalog.Catalog) *PartitionEnsurer {
	return &PartitionEnsurer{cat: cat, claimed: map[catalog.PartitionKey]struct{}{}}
}

// Ensure 为 (namespace, utcDay) 补建一条最小分区记录（已存在则跳过）。
//
// 返回 error 仅用于观测；调用方不应因它中断采集——记录缺失是「查不到」，
// 而中断采集是「彻底没了」，两者严重程度差一个数量级。
func (e *PartitionEnsurer) Ensure(namespace, utcDay string) error {
	if e == nil || e.cat == nil {
		return nil
	}
	namespace = strings.TrimSpace(namespace)
	utcDay = strings.TrimSpace(utcDay)
	if namespace == "" || utcDay == "" {
		return nil
	}
	key := catalog.PartitionKey{StorageNamespace: namespace, UTCDay: utcDay}

	e.mu.Lock()
	if _, done := e.claimed[key]; done {
		e.mu.Unlock()
		return nil
	}
	e.mu.Unlock()

	if _, exists := e.cat.Get(key); exists {
		// 已有权威：不动它（可能来自重建/迁移，owner 与目录可能不同）。
		e.markClaimed(key)
		return nil
	}
	target, err := e.cat.WriteAdmission(key)
	if err != nil {
		slog.Warn("补建 Catalog 分区被拒绝（查询侧将看不到本分区）",
			"namespace", namespace, "utcDay", utcDay, "error", err)
		return err
	}
	if !target.OK {
		slog.Warn("补建 Catalog 分区被拒绝（无写目标）",
			"namespace", namespace, "utcDay", utcDay, "reason", target.Reason)
		return nil
	}
	dirID := target.DirID
	if dirID == "" {
		// 与旧链路 ensureCatalogPartition 的兜底逐字一致。
		dirID = string(target.Owner) + "-logtail"
	}
	rec := catalog.NewStableRecord(key, target.Owner, target.Generation, dirID)
	// **必须带非 nil 投影**：`querySnapshotLocked` 的门禁是
	// `rec == nil || rec.PublishedProjection == nil || !Recover(rec).Queryable || !OwnerForQuery(rec).OK`
	// —— 缺投影就直接 continue（分区不进快照），即使 owner/dirID 都对也查不到。
	//
	// 刻意不填代次：新采集器写的行上没有 `projection_generation` 标签，
	// 填假代次会把查询限制在一个不存在的标签上。
	rec.PublishedProjection = &catalog.PublishedProjection{
		QueryLocationDirID: dirID,
		QueryGeneration:    target.Generation,
	}
	if err := e.cat.AppendJournal(catalog.JournalEntry{Key: key, Record: rec}); err != nil {
		slog.Warn("补建 Catalog 分区写 journal 失败（查询侧暂看不到本分区）",
			"namespace", namespace, "utcDay", utcDay, "error", err)
		return err
	}
	e.markClaimed(key)
	slog.Info("已为本批日志补建 Catalog 分区记录",
		"namespace", namespace, "utcDay", utcDay, "owner", string(target.Owner), "dirID", dirID)
	return nil
}

func (e *PartitionEnsurer) markClaimed(key catalog.PartitionKey) {
	e.mu.Lock()
	e.claimed[key] = struct{}{}
	e.mu.Unlock()
}

// EnsureForEventTime 按事件时间归天并补建（UTC 日，与分区键同口径）。
//
// # 为什么必须用 UTC 日
//
// 分区键是 UTC 日；用本地日会在东八区把 00:00–08:00 的数据划到前一个分区，
// 而那些行在 VL 里的 `_time` 是 UTC —— 两侧天界一致才能查到。
func (e *PartitionEnsurer) EnsureForEventTime(namespace string, when time.Time) error {
	if e == nil {
		return nil
	}
	return e.Ensure(namespace, utcday.Of(when))
}
