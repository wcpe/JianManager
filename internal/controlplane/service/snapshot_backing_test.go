package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestSnapshotBackingChain_SurvivesBackupRetention 是 B-1 的核心回归测试。
//
// 缺陷现场：快照的底层备份由 RegisterFull 以普通 Backup（Type=Manual/Mode=full）登记，
// 而 BackupService.pruneExpiredOnce 按 created_at 无差别裁剪（默认 backup.retention_days=14），
// 与 snapshot.retention_days=30 冲突 → 第 15 天起快照底链被裁、快照成为指向软删行的死链，
// 而列表仍显示「可回滚」，点下去才报 record not found。
//
// 本测试同时跑两套保留策略（快照 30 天 + 备份 14 天），断言：
//  1. 超过备份保留期的快照底链**未被裁剪**；
//  2. 该快照仍可作为回滚目标（GetRollableByID 通过）；
//  3. 普通备份（非快照来源）在同一轮里照常被裁掉——证明豁免只针对快照底链，没有一刀切关掉策略。
func TestSnapshotBackingChain_SurvivesBackupRetention(t *testing.T) {
	svc, backups, db, inst, _ := newSnapshotHarness(t)
	// 快照保留 30 天、备份保留 14 天：制造「快照还没到期、底链先到期」的经典冲突。
	svc.SetSettingsReader(stubSettings{
		SettingKeySnapshotRetentionCount:  "0",
		SettingKeySnapshotRetentionDays:   "30",
		SettingKeySnapshotPreRollbackKeep: "3",
	})
	backups.SetSettingsReader(stubSettings{SettingKeyBackupRetentionDays: "14"})

	// 1) 制造一条 20 天前的快照（含底链）与一条 20 天前的普通备份。
	old := time.Now().AddDate(0, 0, -20)
	snap := &model.InstanceSnapshot{
		InstanceID: inst.ID, Name: "老快照", Kind: model.SnapshotKindManual,
		State: model.SnapshotStateCompleted,
	}
	require.NoError(t, db.Create(snap).Error)
	require.NoError(t, db.Model(&model.InstanceSnapshot{}).Where("id = ?", snap.ID).
		Update("created_at", old).Error)

	// 底链经真实 RegisterFull 登记（Origin=snapshot），确保测的是生产路径而非手工构造。
	rootBackup, err := backups.RegisterFull(inst.ID, "snapshot-root")
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.Backup{}).Where("id = ?", rootBackup.ID).
		Update("created_at", old).Error)
	require.Equal(t, model.BackupOriginSnapshot, rootBackup.Origin, "RegisterFull 必须登记为快照来源")
	require.NoError(t, db.Model(&model.InstanceSnapshot{}).Where("id = ?", snap.ID).
		Update("root_backup_id", rootBackup.ID).Error)

	plain := &model.Backup{
		InstanceID: inst.ID, Name: "普通老备份", Type: model.BackupTypeManual,
		Mode: model.BackupModeFull, Status: model.BackupStatusCompleted, Origin: model.BackupOriginManual,
	}
	require.NoError(t, db.Create(plain).Error)
	require.NoError(t, db.Model(&model.Backup{}).Where("id = ?", plain.ID).
		Update("created_at", old).Error)

	// 2) 同时跑两套保留策略（先快照后备份，与 apps/control-plane 的接线顺序一致）。
	require.Equal(t, 0, svc.pruneOnce(), "30 天窗口内快照本身不该被裁")
	require.Equal(t, 1, backups.pruneExpiredOnce(), "普通超期备份照常裁剪（豁免不得一刀切）")

	// 3) 断言：底链仍在、普通备份已软删、快照仍可真回滚。
	var reloaded model.Backup
	require.NoError(t, db.First(&reloaded, rootBackup.ID).Error, "快照底链必须存活于备份保留策略之外")

	var gone model.Backup
	require.ErrorIs(t, db.First(&gone, plain.ID).Error, gorm.ErrRecordNotFound,
		"非快照来源的超期备份应被裁掉")

	rollable, err := svc.GetRollableByID(snap.ID)
	require.NoError(t, err, "底链未被裁剪时快照必须可回滚")
	require.Equal(t, snap.ID, rollable.ID)
	require.Empty(t, rollable.NotRollableReason)
	require.Equal(t, model.SnapshotBackingOK, rollable.RootBackupState)
}

// TestSnapshotBackingChain_MissingDegradesExplicitly B-1 ②：底链缺失必须**显式降级**为不可回滚。
//
// 覆盖两条读路径：
//   - 列表：RootBackupState=missing + NotRollableReason 非空（前端据此禁用按钮并显示原因）；
//   - 回滚入口：GetRollableByID 返回 ErrSnapshotNotRollable 且原因含「底层备份」，
//     而不是让任务在回放阶段才报 record not found。
func TestSnapshotBackingChain_MissingDegradesExplicitly(t *testing.T) {
	svc, backups, db, inst, _ := newSnapshotHarness(t)

	snap := &model.InstanceSnapshot{
		InstanceID: inst.ID, Name: "死链快照", Kind: model.SnapshotKindManual,
		State: model.SnapshotStateCompleted,
	}
	require.NoError(t, db.Create(snap).Error)
	rootBackup, err := backups.RegisterFull(inst.ID, "snapshot-root")
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.InstanceSnapshot{}).Where("id = ?", snap.ID).
		Update("root_backup_id", rootBackup.ID).Error)

	// 人工删除底链（模拟保留策略裁剪或运维误删）。
	require.NoError(t, db.Delete(&model.Backup{}, rootBackup.ID).Error)

	list, err := svc.ListByInstance(inst.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, model.SnapshotBackingMissing, list[0].RootBackupState)
	require.NotEmpty(t, list[0].NotRollableReason, "底链缺失必须给出显式原因")
	require.False(t, list[0].EffectiveRollable(), "死链快照不得显示为可回滚")

	_, err = svc.GetRollableByID(snap.ID)
	require.ErrorIs(t, err, ErrSnapshotNotRollable)
	require.Contains(t, err.Error(), "底层备份", "错误文案必须点明底链缺失")
}

// TestSnapshotRollback_RejectsMissingBackingChain 底链缺失时回滚请求必须前置拒绝。
//
// 与上一条互补：这里走完整 Rollback 入口，断言它**不产生任何副作用**——
// 不得先建 pre_rollback（那会在坏路径上继续消耗磁盘）。
func TestSnapshotRollback_RejectsMissingBackingChain(t *testing.T) {
	svc, backups, db, inst, _ := newSnapshotHarness(t)
	svc.SetAudit(NewAuditService(db))

	snap := &model.InstanceSnapshot{
		InstanceID: inst.ID, Name: "死链快照", Kind: model.SnapshotKindManual,
		State: model.SnapshotStateCompleted,
	}
	require.NoError(t, db.Create(snap).Error)
	rootBackup, err := backups.RegisterFull(inst.ID, "snapshot-root")
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.InstanceSnapshot{}).Where("id = ?", snap.ID).
		Update("root_backup_id", rootBackup.ID).Error)
	require.NoError(t, db.Delete(&model.Backup{}, rootBackup.ID).Error)

	_, err = svc.Rollback(t.Context(), snap.ID, 7)
	require.ErrorIs(t, err, ErrSnapshotNotRollable)

	var preCount int64
	require.NoError(t, db.Model(&model.InstanceSnapshot{}).
		Where("kind = ?", model.SnapshotKindPreRollback).Count(&preCount).Error)
	require.EqualValues(t, 0, preCount, "前置校验失败不得留下 pre_rollback 副作用")
}
