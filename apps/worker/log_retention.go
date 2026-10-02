package main

import (
	"context"
	"fmt"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/lifecycle"
	"github.com/wcpe/JianManager/internal/worker/logs/retention"
)

// 本文件是保留策略的**生产适配层**：把既有真源（catalog / lifecycle / 容量门禁读数）
// 接到 retention 包的抽象上。
//
// 为什么适配层与判定层要分开：判定与执行的全部语义（触发口径、动作次序、
// 「搬不动就留原物」）都在 retention 包里并被回归覆盖；这里只做「字段搬运」——
// 把 catalog 的 Record 映射成 PartitionRef、把 Mover 调用转成 dayManager 调用。
// 于是本文件可以薄到几乎不需要测试，而真正的规则不会因为换个真源就变形。

// newCatalogPartitionLister 把 catalog 投影为候选分区列表。
func newCatalogPartitionLister(cat *catalog.Catalog) retention.PartitionLister {
	return retention.ListerFunc(func() ([]retention.PartitionRef, error) {
		if cat == nil {
			return nil, nil
		}
		keys := cat.Keys()
		out := make([]retention.PartitionRef, 0, len(keys))
		for _, key := range keys {
			rec, ok := cat.Get(key)
			if !ok {
				// Keys 与 Get 之间的不一致（并发删除）不是错误：跳过即可。
				continue
			}
			out = append(out, retention.PartitionRef{
				StorageNamespace: key.StorageNamespace,
				// 直接沿用 catalog 的日期写法（解析层两种都认，避免在这里做格式转换）。
				UTCDay: key.UTCDay,
				Owner:  string(rec.Owner),
				// 在途判据与既有 RPC 迁移入口**逐字一致**（见 SetPartitionMigrator 的分支）：
				// 有迁移状态、未清理完、且已登记目标 owner ⇒ 视为在途，驱动器不得再次下发。
				InFlight: rec.MigrationState != "" &&
					rec.MigrationState != catalog.StateCleaned &&
					rec.TargetOwner.Valid(),
			})
		}
		return out, nil
	})
}

// newDayManagerMover 把 lifecycle 状态机接成 Mover。
//
// 分支与既有 `SetPartitionMigrator` 的语义**逐字对齐**（同一套判据、同一个调用序列）：
// 已在冷层 ⇒ 幂等返回；在途 ⇒ Resume 而非重新 Start；否则 Start 到 COLD。
// 之所以不另写一套：两条入口（人工 API 与自动驱动器）对同一个状态机若有不同判据，
// 早晚会在「人工介入与自动搬运同时发生时」互相踩掉对方的状态。
func newDayManagerMover(mgr *lifecycle.DayManager, cat *catalog.Catalog) retention.Mover {
	return retention.MoverFunc(func(ctx context.Context, storageNamespace, day string) (string, error) {
		if mgr == nil {
			return "", fmt.Errorf("日志 Lifecycle 管理器未就绪")
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		parsed, err := parsePartitionDay(day)
		if err != nil {
			return "", err
		}
		key := catalog.PartitionKey{StorageNamespace: storageNamespace, UTCDay: parsed.Format("2006-01-02")}
		rec, ok := cat.Get(key)
		if !ok {
			return "", fmt.Errorf("日志分区 %s 不在 Catalog 中", key)
		}
		if rec.Owner == catalog.OwnerCold {
			// 幂等：已经在冷层，视为成功（返回现有权威目录 ID）。
			return rec.OwnerDirID, nil
		}
		dirID := parsed.Format("20060102")
		if rec.MigrationState != "" && rec.MigrationState != catalog.StateCleaned && rec.TargetOwner.Valid() {
			if err := mgr.Resume(day); err != nil {
				return "", err
			}
			return dirID, nil
		}
		if err := mgr.Start(day, catalog.OwnerCold, dirID); err != nil {
			return "", err
		}
		return dirID, nil
	})
}

// parsePartitionDay 解析分区日期（兼容 `20060102` 与 `2006-01-02`）。
func parsePartitionDay(raw string) (time.Time, error) {
	for _, layout := range []string{"20060102", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, raw, time.UTC); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("无法解析日志分区日期 %q（支持 20060102 与 2006-01-02）", raw)
}

// newDiskPercentReader 返回磁盘使用率读数。
//
// 直接复用采集侧**同一份** CapacityProvider（`ingest.DiskCapacityProvider`）：
// 容量门禁与保留触发若各采一次磁盘，两者迟早会给出不同结论，
// 而现场只会表现成「有时降级有时不降级」，极难归因。
func newDiskPercentReader(provider func() (acquire.CapacityBudget, error)) retention.DiskReader {
	return func() (float64, error) {
		budget, err := provider()
		if err != nil {
			return 0, err
		}
		return budget.DiskUsagePercent, nil
	}
}

// logRetentionTriggerSummary 把触发配置折成一行摘要（启动日志用），
// 使现场一眼能确认「到底按什么口径在搬」。
func logRetentionTriggerSummary(policy retention.Policy) string {
	t := policy.Trigger.Normalize()
	if !t.Enabled() {
		return "仅按年龄"
	}
	return fmt.Sprintf("年龄 + 磁盘水位 %v%%（最小年龄 %s，取先到）", t.DiskPercent, t.MinAge)
}

// retentionPolicyLogFields 汇总启动时需要打印的策略字段。
func retentionPolicyLogFields(policy retention.Policy) []any {
	return []any{
		"describe", policy.Describe(),
		"hotRetention", policy.HotWindow().String(),
		"trigger", logRetentionTriggerSummary(policy),
		"discard", policy.Discard,
	}
}
