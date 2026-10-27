package main

import (
	"context"
	"fmt"
	"log/slog"
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
		pending := make(map[string]bool)
		for _, day := range cat.PhysicalDayMigrations() {
			pending[day.UTCDay] = true
		}
		blocked := make(map[string]bool)
		for _, key := range keys {
			if rec, ok := cat.Get(key); ok && rec.MigrationState != "" && rec.MigrationState != catalog.StateCleaned {
				_, err := catalog.HappyIndex(rec.MigrationState)
				if rec.MigrationState == catalog.StateFailedManual || (err != nil && rec.MigrationState != catalog.StateFailedRetryable) {
					blocked[key.UTCDay] = true
				} else {
					pending[key.UTCDay] = true
				}
			}
		}
		out := make([]retention.PartitionRef, 0, len(keys))
		for _, key := range keys {
			rec, ok := cat.Get(key)
			if !ok {
				// Keys 与 Get 之间的不一致（并发删除）不是错误：跳过即可。
				continue
			}
			retryable := pending[key.UTCDay] && !blocked[key.UTCDay]
			out = append(out, retention.PartitionRef{
				StorageNamespace: key.StorageNamespace,
				// 直接沿用 catalog 的日期写法（解析层两种都认，避免在这里做格式转换）。
				UTCDay: key.UTCDay,
				Owner:  string(rec.Owner),
				// 正常未完成批次也必须续跑；手工和未知状态继续隔离。
				InFlight: rec.MigrationState != "" &&
					rec.MigrationState != catalog.StateCleaned && !retryable,
				Retryable: retryable,
			})
		}
		return out, nil
	})
}

// newCatalogArchiveAuthority 把 catalog 的「已归档」权威态接成 retention 的迁移出口。
//
// 为什么这一层必须存在（而不是让 retention 直接 import catalog）：
// 保留策略只该回答「副本可信吗、能删吗」两个问题，分区 Catalog 是 Worker 的内部真源；
// 让策略包依赖它，等于把「权威态怎么改」的细节扩散到判定层，日后任何 Catalog 字段演进
// 都要穿透策略包。这里只做类型搬运，两个类型各自演进互不影响。
//
// 语义要点（全部由 catalog 侧的不变量保证，本层不重写判定）：
//   - MarkArchived 幂等：同一归档位置重跑不会产生第二条归档记录；
//   - MarkArchived 会先确认没有活跃查询租约，避免把「正在读的位置」掀掉；
//   - RollbackArchived 在热层已回收后拒绝回滚；
//   - 证据不完整（无 prefix / checksum / 对象数为 0）一律拒绝。
type catalogArchiveAuthority struct {
	cat *catalog.Catalog
	now func() time.Time
}

func newCatalogArchiveAuthority(cat *catalog.Catalog) retention.ArchiveAuthority {
	if cat == nil {
		return nil
	}
	return &catalogArchiveAuthority{cat: cat, now: func() time.Time { return time.Now().UTC() }}
}

func (a *catalogArchiveAuthority) key(namespace, day string) (catalog.PartitionKey, error) {
	parsed, err := parsePartitionDay(day)
	if err != nil {
		return catalog.PartitionKey{}, err
	}
	// Catalog 的日期写法是 `2006-01-02`（与 retention 兼容的 `20060102` 归一到这里）。
	return catalog.PartitionKey{StorageNamespace: namespace, UTCDay: parsed.Format("2006-01-02")}, nil
}

func (a *catalogArchiveAuthority) FreezeForArchive(ctx context.Context, namespace, day string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := a.key(namespace, day)
	if err != nil {
		return err
	}
	// 冻结粒度是**物理日**（VL 的 partitions/<utc-day>/ 目录装着当天所有命名空间），
	// 故这里用 Catalog 写法归一后的日期，而不是命名空间。
	return a.cat.FreezeDayForArchive(key.UTCDay, "archive:"+key.UTCDay)
}

func (a *catalogArchiveAuthority) ReleaseArchiveFreeze(ctx context.Context, namespace, day string, reason string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := a.key(namespace, day)
	if err != nil {
		return err
	}
	// 该日仍有其它**已归档**成员时保持冻结：归档工作尚未结束，
	// 把一天的一部分写成 HOT、另一部分已在归档，会让「删目录」变成不可能安全的操作。
	for _, rec := range a.cat.RecordsForDay(key.UTCDay) {
		if rec != nil && rec.Owner == catalog.OwnerArchive {
			return nil
		}
	}
	return a.cat.AbortArchiveFreeze(key.UTCDay, reason)
}

func (a *catalogArchiveAuthority) MarkArchived(ctx context.Context, namespace, day string, ev retention.ArchiveEvidence) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := a.key(namespace, day)
	if err != nil {
		return err
	}
	_, _, err = a.cat.MarkArchived(key, catalog.ArchiveEvidence{
		ProviderKind:     ev.ProviderKind,
		ObjectPrefix:     ev.ObjectPrefix,
		ManifestChecksum: ev.ManifestChecksum,
		Coverage: catalog.ArchiveCoverage{
			ObjectCount: ev.ObjectCount,
			SourceBytes: ev.SourceBytes,
			StoredBytes: ev.StoredBytes,
			Complete:    ev.CoverageComplete,
		},
		ArchivedAt: ev.At,
	}, a.now())
	return err
}

func (a *catalogArchiveAuthority) RollbackArchived(ctx context.Context, namespace, day string, reason string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := a.key(namespace, day)
	if err != nil {
		return err
	}
	stillArchived, err := a.cat.RollbackArchived(key, reason, a.now())
	if err != nil {
		return err
	}
	if !stillArchived {
		// 该日已没有任何归档成员 ⇒ 解除归档冻结，把写入接纳还给仍在 HOT 的分区。
		// 解除失败不掩盖原始回滚结果：冻结残留只会让该日拒绝写入（报错可见），
		// 而数据仍然完整；返回错误会让调用方误以为回滚失败。
		if err := a.cat.AbortArchiveFreeze(key.UTCDay, "归档全部回滚"); err != nil {
			slog.Warn("日志归档冻结解除失败（该物理日将继续拒绝写入，数据仍完整）",
				"day", key.UTCDay, "error", err)
		}
	}
	return nil
}

func (a *catalogArchiveAuthority) MarkHotReclaimed(ctx context.Context, namespace, day string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := a.key(namespace, day)
	if err != nil {
		return err
	}
	if _, err := a.cat.MarkArchiveReclaimed(key, a.now()); err != nil {
		return err
	}
	// 全日完成归档后解除冻结；仍有 HOT 成员时 CompleteArchiveFreeze 会拒绝，
	// 这属于正常中间态（其他命名空间还没归档），不作为错误上抛。
	if err := a.cat.CompleteArchiveFreeze(key.UTCDay); err != nil {
		slog.Debug("日志归档日冻结尚未满足解除条件（其余命名空间仍待归档）",
			"day", key.UTCDay, "error", err)
	}
	return nil
}

// newDayManagerMover 把 lifecycle 状态机接成 Mover。
//
// 已有迁移必须先 Resume，不能因 Owner 已切 COLD 跳过租约排空与清理。
// 手工失败与未知状态不自动覆盖；稳定冷层才允许幂等返回，否则 Start 到 COLD。
// Resume/Start 均委托既有状态机，继续保留最后副本保护、校验与查询租约门禁。
func newDayManagerMover(mgr *lifecycle.DayManager, cat *catalog.Catalog) retention.Mover {
	return retention.MoverFunc(func(ctx context.Context, storageNamespace, day string) (string, error) {
		if mgr == nil || cat == nil {
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
		canonicalDay := parsed.Format("2006-01-02")
		pending := false
		if physical, ok := cat.PhysicalDay(canonicalDay); ok {
			pending = physical.Frozen
		}
		hotResidual := false
		for _, member := range cat.RecordsForDay(canonicalDay) {
			hotResidual = hotResidual || member.Owner == catalog.OwnerHot
			if member.MigrationState != "" && member.MigrationState != catalog.StateCleaned {
				_, stateErr := catalog.HappyIndex(member.MigrationState)
				if member.MigrationState == catalog.StateFailedManual || (stateErr != nil && member.MigrationState != catalog.StateFailedRetryable) {
					return "", fmt.Errorf("日志分区 %s 迁移状态 %s 需要人工处理", member.Key, member.MigrationState)
				}
				pending = true
			}
		}
		if pending {
			if err := mgr.Resume(canonicalDay); err != nil {
				return "", err
			}
			if physical, ok := cat.PhysicalDay(canonicalDay); ok {
				return physical.TargetDirID, nil
			}
			return rec.OwnerDirID, nil
		}
		if rec.MigrationState != "" && rec.MigrationState != catalog.StateCleaned {
			_, stateErr := catalog.HappyIndex(rec.MigrationState)
			if rec.MigrationState == catalog.StateFailedManual || (stateErr != nil && rec.MigrationState != catalog.StateFailedRetryable) {
				return "", fmt.Errorf("日志分区 %s 迁移状态 %s 需要人工处理", key, rec.MigrationState)
			}
			if rec.TargetOwner != catalog.OwnerCold || rec.TargetDirID == "" {
				return "", fmt.Errorf("日志分区 %s 缺少已登记的冷层恢复目标", key)
			}
			if err := mgr.Resume(canonicalDay); err != nil {
				return "", err
			}
			return rec.TargetDirID, nil
		}
		if rec.Owner == catalog.OwnerCold && !hotResidual {
			// 只有整日稳定冷层才能幂等成功，不能遗漏另一成员的 HOT 残留。
			return rec.OwnerDirID, nil
		}
		dirID := parsed.Format("20060102")
		if err := mgr.Start(canonicalDay, catalog.OwnerCold, dirID); err != nil {
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
		// 次选路径的开关与「删除三闸是否全开」必须进启动日志：
		// archive_copy 打开后，到期分区会真的被导出（写盘/写对象存储），而回收是否发生
		// 取决于三闸。读日志的人要能一眼分辨「没开导出」与「开了导出但闸门关着」。
		"archiveCopy", policy.ArchiveCopy.Enabled,
		"deleteGates", deleteGateSummary(policy),
	}
}

// deleteGateSummary 汇总删除三闸的开启状态（启动日志用）。
//
// 措辞与 retention.Policy.DeleteGatesOpen 对齐：只有三闸全开才写 all-open，
// 否则逐条列出未开的那几道——「差一道」与「差三道」的处置动作完全不同。
func deleteGateSummary(policy retention.Policy) string {
	if open, reason := policy.DeleteGatesOpen(); !open {
		return "closed: " + reason
	}
	return "all-open（discard + sweep.vl_sweep + log_vl.delete_enabled）"
}
