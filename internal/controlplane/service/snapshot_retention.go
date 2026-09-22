package service

import (
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 快照保留策略与启动清扫（FR-466 §2.4 / m6）。
//
// 与 snapshot.go 同属 SnapshotService，拆出的理由：原文件同时承载「创建」「回滚」
// 「保留裁剪」「启动清扫」「底链校验」五块职责（R26）。本文件只放保留策略与启动清扫
// ——即「把行纳入终态与裁剪管辖」的那一半，与回滚编排（snapshot.go）天然分层。
// 纯机械搬迁，无行为变化。

// ---- 保留策略（FR-466 §2.4）----

// snapshotRetentionTick 保留裁剪巡检周期（与备份裁剪同量级；按天/按条数的策略不需要更密的巡检）。
const snapshotRetentionTick = time.Hour

// Start 启动保留裁剪后台巡检（幂等）。未注入设置读取器时用默认值（10 条 / 30 天）。
//
// 启动时先做一次**孤儿清扫**（m6）：把上次进程残留的 pending/running 快照收敛为 failed，
// 再进入保留裁剪循环。顺序有意为之——先收敛终态，裁剪才会把这些行纳入正常管辖。
//
// stopCh 在每次 Start 时重建（与 BackupService/ArtifactReconcileService 同口径）：
// Stop 会 close 该 channel，若复用同一个，Stop→Start 会 panic 在 close of closed channel。
// 生产路径只 Start 一次，但测试与将来的热重启都会踩到。本不变量在本包三处 Start
// （本函数 / QuotaEnforcer.Start / CrashSnapshotService.Start）统一按「重建 + 取局部引用」
// 实现，见 restartableStop（R22/R30）。
func (s *SnapshotService) Start() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	stop := restartableStop(&s.stopCh)
	s.mu.Unlock()

	// 同步执行（非 goroutine）：清扫会改状态并影响紧接着的裁剪结果，
	// 且量级极小（残留行数），放在后台反而让「启动后立刻查询」看到过渡态。
	s.sweepStartupPendingOrphans()
	s.sweepStartupOrphans()

	go func() {
		ticker := time.NewTicker(snapshotRetentionTick)
		defer ticker.Stop()
		s.pruneOnce()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				s.pruneOnce()
			}
		}
	}()
	slog.Info("快照保留裁剪巡检已启动")
}

// Stop 停止保留裁剪巡检。
func (s *SnapshotService) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return
	}
	close(s.stopCh)
	s.running = false
}

// retentionCount / retentionDays / preRollbackKeep 读平台设置的生效值（缺省 10 / 30 / 3）。
func (s *SnapshotService) retentionCount() int {
	return s.intSetting(SettingKeySnapshotRetentionCount, 10)
}

func (s *SnapshotService) retentionDays() int {
	return s.intSetting(SettingKeySnapshotRetentionDays, 30)
}

// preRollbackKeep 读「天数裁剪时必须保留的 pre_rollback 条数」，**收敛最小值 1**。
//
// 关键不变量：pre_rollback 是回滚的「退回点」，任何保留策略都不得把它清空
// （spec §2.4：禁止「回滚把回滚点挤掉」）。条数维已由 `kind <> pre_rollback` 整体豁免，
// 天数维则只靠本值兜底——若它为 0，`keepNewestPreRollbackIDs(0)` 返回空集，
// `snapshot.retention_days`(默认 30) 就会把**全部** pre_rollback 删光，
// 正是该不变量要禁止的场景。
//
// 写路径（`validateSettingValue`）已拒收 <1，但读路径仍需自行收敛，因为
// ①存量库可能留有本校验上线前写入的 0（`intSetting` 仅在 n<0 时回落缺省，0 会被采纳）；
// ②运维可直接改库。护栏放在**消费点**才能覆盖全部数据来源，而不是只信任写入端的当次校验。
func (s *SnapshotService) preRollbackKeep() int {
	if n := s.intSetting(SettingKeySnapshotPreRollbackKeep, 3); n >= 1 {
		return n
	}
	slog.Warn("snapshot.pre_rollback_keep 生效值小于 1，已按最小值 1 处理"+
		"（pre_rollback 不得被保留策略清空）", "key", SettingKeySnapshotPreRollbackKeep)
	return 1
}

// intSetting 读整型设置；未注入读取器或值非法时用 fallback。
func (s *SnapshotService) intSetting(key string, fallback int) int {
	if s.settings == nil {
		return fallback
	}
	n, err := strconv.Atoi(strings.TrimSpace(s.settings.EffectiveValue(key)))
	if err != nil || n < 0 {
		return fallback
	}
	return n
}

// pruneOnce 按保留策略裁剪一轮（全平台），返回删除条数（便于测试断言）。
//
// **pre_rollback 不受条数裁剪**（spec §2.4 关键约束）：它是回滚的「退回点」，
// 被 snapshot.retention_count 挤掉就等于「回滚把回滚点挤掉」，误点回滚将不可逆。
// 故 pre_rollback 只受天数裁剪，且**每实例至少保留最近 `max(pre_rollback_keep, 1)` 条**
// ——最小值收敛到 1 而非 0：配 0 会让天数维把 pre_rollback 清空，条数维的豁免形同虚设。
//
// 全平台语义（instanceID == 0）用于启动与周期巡检；回滚成功后的同步裁剪只裁本实例
// （见 pruneInstanceOnce），避免「点一次回滚就触发一轮全平台扫描」（R10）。
func (s *SnapshotService) pruneOnce() int {
	return s.pruneScope(0)
}

// pruneInstanceOnce 只裁剪指定实例的快照，返回删除条数（R10）。
//
// 缺陷现场：回滚成功后同步调 `pruneOnce()`，即每次点回滚都要为**全平台**每个有快照的
// 实例各发 2 次查询（条数维）+ 1 次（天数维）——回滚路径被全平台成本污染，且这段成本
// 落在用户等待的任务体内。回滚只可能改变目标实例的快照集合（新建 pre_rollback + 标记
// rolled_back），其它实例的状态不受影响，下一轮周期巡检自然会处理它们。
func (s *SnapshotService) pruneInstanceOnce(instanceID uint) int {
	if instanceID == 0 {
		return 0
	}
	return s.pruneScope(instanceID)
}

// pruneScope 裁剪一轮：instanceID == 0 表示全平台，否则只处理该实例（R10）。
func (s *SnapshotService) pruneScope(instanceID uint) int {
	days := s.retentionDays()
	count := s.retentionCount()
	keepPre := s.preRollbackKeep()
	deleted := 0
	cutoff := time.Now().AddDate(0, 0, -days)

	// 1. 按天数裁剪（所有类型，含 pre_rollback）——但 pre_rollback 至少要留 keepPre 条。
	if days > 0 {
		query := s.db.Where("created_at < ?", cutoff)
		if instanceID != 0 {
			query = query.Where("instance_id = ?", instanceID)
		}
		var expired []model.InstanceSnapshot
		if err := query.Order("created_at ASC, id ASC").Find(&expired).Error; err != nil {
			slog.Error("查询超期快照失败", "error", err, "instanceId", instanceID)
		} else {
			protected := s.keepNewestPreRollbackIDs(keepPre, instanceID)
			for i := range expired {
				if protected[expired[i].ID] {
					continue
				}
				if err := s.deleteQuietly(expired[i].ID); err != nil {
					continue
				}
				deleted++
			}
		}
	}

	// 2. 按条数裁剪：按实例维度保留最新 count 条**非 pre_rollback** 快照。
	if count > 0 {
		instanceIDs := []uint{instanceID}
		if instanceID == 0 {
			instanceIDs = nil
			if err := s.db.Model(&model.InstanceSnapshot{}).
				Where("kind <> ?", model.SnapshotKindPreRollback).
				Distinct("instance_id").Pluck("instance_id", &instanceIDs).Error; err != nil {
				slog.Error("查询快照实例失败", "error", err)
				return deleted
			}
		}
		for _, iid := range instanceIDs {
			var keepIDs []uint
			if err := s.db.Model(&model.InstanceSnapshot{}).
				Where("instance_id = ? AND kind <> ?", iid, model.SnapshotKindPreRollback).
				Order("created_at DESC, id DESC").Limit(count).
				Pluck("id", &keepIDs).Error; err != nil {
				continue
			}
			if len(keepIDs) == 0 {
				continue
			}
			var stale []model.InstanceSnapshot
			if err := s.db.Where("instance_id = ? AND kind <> ? AND id NOT IN ?",
				iid, model.SnapshotKindPreRollback, keepIDs).Find(&stale).Error; err != nil {
				continue
			}
			for i := range stale {
				if err := s.deleteQuietly(stale[i].ID); err != nil {
					continue
				}
				deleted++
			}
		}
	}
	if deleted > 0 {
		slog.Info("按保留策略裁剪旧快照", "deleted", deleted, "count", count, "days", days, "instanceId", instanceID)
	}
	return deleted
}

// keepNewestPreRollbackIDs 取「按天数裁剪时必须保留」的 pre_rollback 快照 ID 集合
// （每实例最近 keep 条；instanceID 非 0 时只看该实例）。
//
// keep<=0 时收敛为 1：调用方本应已用 `preRollbackKeep()`（最小值 1），此处再兜一层，
// 使「pre_rollback 被天数裁剪清空」在任何入参下都不可能发生——这是 spec §2.4 的核心不变量，
// 不能依赖单一调用点的正确性。
func (s *SnapshotService) keepNewestPreRollbackIDs(keep int, instanceID uint) map[uint]bool {
	protected := make(map[uint]bool)
	if keep <= 0 {
		keep = 1
	}
	instanceIDs := []uint{instanceID}
	if instanceID == 0 {
		instanceIDs = nil
		if err := s.db.Model(&model.InstanceSnapshot{}).
			Where("kind = ?", model.SnapshotKindPreRollback).
			Distinct("instance_id").Pluck("instance_id", &instanceIDs).Error; err != nil {
			return protected
		}
	}
	for _, iid := range instanceIDs {
		var ids []uint
		if err := s.db.Model(&model.InstanceSnapshot{}).
			Where("instance_id = ? AND kind = ?", iid, model.SnapshotKindPreRollback).
			Order("created_at DESC, id DESC").Limit(keep).
			Pluck("id", &ids).Error; err != nil {
			continue
		}
		for _, id := range ids {
			protected[id] = true
		}
	}
	return protected
}

// deleteQuietly 删除一条快照（含底层备份），底层删除失败不阻断记录清理。
func (s *SnapshotService) deleteQuietly(snapshotID uint) error {
	return s.Delete(snapshotID)
}
