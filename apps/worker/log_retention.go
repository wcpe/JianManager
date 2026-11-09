package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	workercfg "github.com/wcpe/JianManager/internal/worker"
	"github.com/wcpe/JianManager/internal/worker/logs/archive"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/retention"
	workerstorage "github.com/wcpe/JianManager/internal/worker/storage"
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
				Owner:  retentionOwnerForSingleInstance(rec.Owner),
				// 正常未完成批次也必须续跑；手工和未知状态继续隔离。
				InFlight: rec.MigrationState != "" &&
					rec.MigrationState != catalog.StateCleaned && !retryable,
				Retryable: retryable,
			})
		}
		return out, nil
	})
}

// retentionOwnerForSingleInstance 把 catalog owner 归一成**单实例视角**下的 owner（2026-10-29 审计修复）。
//
// ## 为什么必须归一
//
// `retention/driver.go` 的候选闸会跳过 `Owner == OwnerColdName` 的分区（原文注释：
// “仅稳定冷层和真正活动/手工迁移跳过”）。那条规则的前提是**真的存在两个物理层** ——
// 已经在 COLD 的分区不需要再搬。
//
// 冷热双层废除后只有一个 VL 实例（`lifecycle/vlops.go` 明确写「单实例下 Hot ≡ Cold」），
// catalog 里残留的 `OwnerCold` 只是**历史记录**，不再意味着“已在另一物理位置”。
// 若继续按它跳过：升级前已迁移到 COLD 的日分区将**永远进不了归档路径**，
// 到期时被 VL 原生 TTL 直接删除且**无任何副本** —— 门禁被旧 owner 值绕过，是真正的丢数据风险。
//
// 归一成 `OwnerHot` 后，这些分区会正常走归档计划（没有 mover 时落 ActionBlocked =
// 保留原物 + 告警，仍不会裸删），符合“宁占盘不丢数据”的既定原则。
func retentionOwnerForSingleInstance(owner catalog.Owner) string {
	if owner == catalog.OwnerCold {
		// 单实例下“冷层”与“热层”是同一个物理目录，不存在“已在另一位置”这回事。
		return string(catalog.OwnerHot)
	}
	return string(owner)
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
// # 为何签名收窄成 float64
//
// 原实现要一个 `acquire.CapacityBudget`（旧链路的容量模型：WAL 上限、缺口上限、
// 降级/暂停阈值）。而保留策略只用到其中的 `DiskUsagePercent` —— 其余字段属于
// 采集侧内部概念，新架构里根本不存在。保留整包会让“为取一个字段而传一整个模型”
// 的分歧永久留着；直接收窄到数字，两边都清楚。
func newDiskPercentReader(provider func() (float64, error)) retention.DiskReader {
	return func() (float64, error) {
		return provider()
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

// newArchiveCopyProvider 按配置构造「导出归档」目的地。
//
// 为什么要抽成函数并返回 error 而不是就地 if 分支：三条目的地（local / s3 / minio）
// 各有自己的前置条件，失败时必须能让装配层**单独降级**（不装导出器 ⇒ 计划阶段不产出
// archive_copy ⇒ 到期分区继续落 blocked 保留原物），而不是让整个 Worker 启动失败。
// 这个降级方向与 `main.go` 里既有的「开关开了但没目的地」那条分支完全一致。
//
// 为什么不复用 `log_archive.*` 的配置：Deep Archive 是**查询面**的归档后端
// （Rehydrate/查询路由用），导出归档是**保留面**的删除前承接物。两者可以指向
// 同一个桶（运维想共用时把键写成一样即可），但不应该隐式捆在一起——
// 「给查询面开了深归档」不该顺带把「到期分区开始往外导出」也打开，反之亦然。
//
// 复用点仅限下层的 `archive.RemoteS3Provider`（同一份 SigV4 客户端、同一套对象键布局、
// 同一个 `archive.Provider` 接口），不新造协议也不新造凭证模型。
func newArchiveCopyProvider(cfg workercfg.LogRetentionArchiveCopyConfig) (archive.Provider, error) {
	switch cfg.ArchiveProviderKind() {
	case "local":
		root := strings.TrimSpace(cfg.Root)
		if root == "" {
			return nil, fmt.Errorf("log_retention.archive_copy.root 为空")
		}
		// 与 `log_retention.archive_copy.root` 的文档口径一致：根目录是**磁盘绝对路径**，
		// 不做数据根派生。归档目的地写错盘是现场事故，不能靠隐式约定猜位置。
		return archive.NewLocalArchive(root), nil
	case "s3", "minio":
		storageCfg := workerstorage.Config{
			Type:      workerstorage.TypeS3,
			Endpoint:  strings.TrimSpace(cfg.Endpoint),
			Bucket:    strings.TrimSpace(cfg.Bucket),
			Region:    strings.TrimSpace(cfg.Region),
			Prefix:    strings.TrimSpace(cfg.Prefix),
			AccessKey: strings.TrimSpace(cfg.AccessKey),
			SecretKey: cfg.SecretKey,
			UseSSL:    cfg.UseSSL,
		}
		// 凭证只在这里作为已解析值使用；日志与错误信息里一律不回显。
		if cfg.ArchiveProviderKind() == "minio" {
			provider, err := archive.NewRemoteMinioProvider(storageCfg)
			if err != nil {
				return nil, fmt.Errorf("log_retention.archive_copy: 构造 MinIO 归档 Provider 失败：%w", err)
			}
			return provider, nil
		}
		provider, err := archive.NewRemoteS3Provider(storageCfg)
		if err != nil {
			return nil, fmt.Errorf("log_retention.archive_copy: 构造 S3 归档 Provider 失败：%w", err)
		}
		return provider, nil
	default:
		return nil, fmt.Errorf("log_retention.archive_copy.provider 不支持 %q", cfg.Provider)
	}
}
