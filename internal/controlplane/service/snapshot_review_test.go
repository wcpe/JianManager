package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestSnapshotCreate_WritesAuditInServiceLayer edge m-3：`instance.snapshot_create`
// 审计必须落在 **service 层**，使 HTTP 端点与 MCP 工具两条入口都覆盖同一动作。
//
// 缺陷现场：该审计原先只在 HTTP handler 里写（router/snapshot.go），而 MCP 工具
// `instance_snapshot_create` 直接调 `SnapshotService.Create`，于是经 MCP 创建快照
// **不产生**该审计——而 API.md 对「创建整机快照」声明了「审计: instance.snapshot_create」，
// 该声明在 MCP 路径下不成立。对比 `instance.snapshot_rollback` 写在 service 层，两条入口都覆盖。
//
// 本测试只调 service（不经过任何 HTTP/MCP 层），断言审计仍然产生——这正是「下沉」的判据。
func TestSnapshotCreate_WritesAuditInServiceLayer(t *testing.T) {
	svc, _, db, inst, _ := newSnapshotHarness(t)
	svc.SetAudit(NewAuditService(db))

	opts := SnapshotCreateOptions{Kind: model.SnapshotKindManual, TriggeredBy: 42}
	_, err := svc.Create(inst.ID, "审计测试快照", opts)
	require.NoError(t, err)

	var count int64
	require.NoError(t, db.Model(&model.AuditLog{}).
		Where("action = ? AND user_id = ?", "instance.snapshot_create", 42).Count(&count).Error)
	require.EqualValues(t, 1, count,
		"创建审计必须由 service 层写入（否则 MCP 入口不产生审计）")
}

// TestSnapshotCreate_NoAuditWhenNotInjected 未注入审计服务时不 panic（可选依赖语义不变）。
func TestSnapshotCreate_NoAuditWhenNotInjected(t *testing.T) {
	svc, _, _, inst, _ := newSnapshotHarness(t)
	require.NotPanics(t, func() {
		_, err := svc.Create(inst.ID, "无审计服务", SnapshotCreateOptions{Kind: model.SnapshotKindManual})
		require.NoError(t, err)
	})
}

// TestSnapshotDelete_KeepsBacklinkSharedByOtherSnapshot R5：删快照时必须先确认底链
// **没有被别的快照共享**。
//
// 缺陷现场：`Delete` 直接 `backups.Delete(rootBackupID)`，而 `BackupService.Delete`
// 只拦「被增量备份引用」，不拦「被别的快照引用」。快照设计上一对一（RegisterFull 每次
// 新建 Backup 行），但 Delete 是公开 API，一旦出现共享（人工改库、或未来引入
// 「同底链多快照」优化），删一个快照会静默毁掉另一个——另一个的行还在、state 仍是
// completed，却已指向被软删的底链（annotateBacking 的「底链缺失」状态正是为此而存在）。
//
// 本测试用人工构造的共享底链覆盖该缺口，断言：本快照行被删、底链存活、另一快照仍可回滚。
func TestSnapshotDelete_KeepsBacklinkSharedByOtherSnapshot(t *testing.T) {
	svc, backups, db, inst, _ := newSnapshotHarness(t)

	shared, err := backups.RegisterFull(inst.ID, "snapshot-shared")
	require.NoError(t, err)

	first := &model.InstanceSnapshot{
		InstanceID: inst.ID, Name: "快照A", Kind: model.SnapshotKindManual,
		State: model.SnapshotStateCompleted, RootBackupID: shared.ID,
	}
	second := &model.InstanceSnapshot{
		InstanceID: inst.ID, Name: "快照B", Kind: model.SnapshotKindManual,
		State: model.SnapshotStateCompleted, RootBackupID: shared.ID,
	}
	require.NoError(t, db.Create(first).Error)
	require.NoError(t, db.Create(second).Error)

	require.NoError(t, svc.Delete(first.ID))

	// 本快照行已删。
	var gone model.InstanceSnapshot
	require.ErrorIs(t, db.First(&gone, first.ID).Error, gorm.ErrRecordNotFound)

	// 底链必须存活：它是第二个快照的唯一数据来源。
	var kept model.Backup
	require.NoError(t, db.First(&kept, shared.ID).Error,
		"被其它快照共享的底链不得随本快照删除，否则会静默毁掉另一个可回滚快照")

	// 第二个快照仍真的可回滚（而非仅 state=completed 的假绿灯）。
	rollable, err := svc.GetRollableByID(second.ID)
	require.NoError(t, err, "共享底链上的另一个快照必须仍可回滚")
	require.Equal(t, model.SnapshotBackingOK, rollable.RootBackupState)
}

// TestSnapshotDelete_RemovesUnsharedBacklink 独占底链照常随快照删除（R5 的对照用例）：
// 防护只针对**共享**，不得把常规删除一并挡住，否则归档只增不减。
func TestSnapshotDelete_RemovesUnsharedBacklink(t *testing.T) {
	svc, backups, db, inst, _ := newSnapshotHarness(t)

	only, err := backups.RegisterFull(inst.ID, "snapshot-only")
	require.NoError(t, err)
	snap := &model.InstanceSnapshot{
		InstanceID: inst.ID, Name: "唯一快照", Kind: model.SnapshotKindManual,
		State: model.SnapshotStateCompleted, RootBackupID: only.ID,
	}
	require.NoError(t, db.Create(snap).Error)

	require.NoError(t, svc.Delete(snap.ID))

	var kept model.Backup
	require.Error(t, db.First(&kept, only.ID).Error, "独占底链必须随快照删除")
}

// TestSnapshotDelete_LastSnapshotOfSharedBacklinkRemovesIt 共享者只剩自己时底链可删：
// 判据是「**除本行外**还有别的快照引用」，最后一个快照删除后底链必须一并清掉。
func TestSnapshotDelete_LastSnapshotOfSharedBacklinkRemovesIt(t *testing.T) {
	svc, backups, db, inst, _ := newSnapshotHarness(t)

	shared, err := backups.RegisterFull(inst.ID, "snapshot-shared")
	require.NoError(t, err)
	for _, name := range []string{"快照A", "快照B"} {
		require.NoError(t, db.Create(&model.InstanceSnapshot{
			InstanceID: inst.ID, Name: name, Kind: model.SnapshotKindManual,
			State: model.SnapshotStateCompleted, RootBackupID: shared.ID,
		}).Error)
	}
	var snaps []model.InstanceSnapshot
	require.NoError(t, db.Where("instance_id = ?", inst.ID).Order("id ASC").Find(&snaps).Error)
	require.Len(t, snaps, 2)

	require.NoError(t, svc.Delete(snaps[0].ID))
	var kept model.Backup
	require.NoError(t, db.First(&kept, shared.ID).Error, "还有一个共享者时必须保留底链")

	require.NoError(t, svc.Delete(snaps[1].ID))
	require.Error(t, db.First(&kept, shared.ID).Error, "最后一个共享者删除后底链应一并清理")
}

// TestAnnotateBacking_FailedSnapshotIsNotRenderedAsMissingBacking R8：归档失败
// （root_backup_id=0 且 state=failed）**不得**被渲染成「底链缺失」。
//
// 缺陷现场：`annotateBacking` 对 `RootBackupID == 0` 一律给 RootBackupState=missing +
// 「快照未关联底层备份（归档未完成）」。而归档失败的行必然没有底链 ID，真实原因是
// **归档失败**（state=failed + failureReason 已表达），把它渲染成「底链缺失」会让排障
// 方向指向「底层备份去哪了」——去找一条从未被创建过的备份。
// 模型注释明确区分了「状态不可回滚」与「底链缺失」两个语义，此处必须同口径。
func TestAnnotateBacking_FailedSnapshotIsNotRenderedAsMissingBacking(t *testing.T) {
	svc, _, db, inst, _ := newSnapshotHarness(t)

	failed := &model.InstanceSnapshot{
		InstanceID: inst.ID, Name: "归档失败", Kind: model.SnapshotKindManual,
		State: model.SnapshotStateFailed, RootBackupID: 0,
		FailureReason: "打包工作目录失败: 归档进程退出码 1",
	}
	require.NoError(t, db.Create(failed).Error)

	list, err := svc.ListByInstance(inst.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, model.SnapshotStateFailed, list[0].State)
	require.Empty(t, list[0].RootBackupState,
		"归档失败不是底链缺失：底链状态应留空（未校验），不得报 missing")
	require.Empty(t, list[0].NotRollableReason,
		"不可回滚的原因由 state/failureReason 表达，不得叠加「底链缺失」文案")
	require.False(t, list[0].EffectiveRollable())
	// 失败原因仍可见（排障依据），证明「不报底链缺失」不等于「信息丢失」。
	require.Contains(t, list[0].FailureReason, "打包工作目录失败")
}

// TestAnnotateBacking_RollableSnapshotWithoutBackingIsMissing R8 的另一半：
// **状态可回滚却没有底链**是真不一致，必须明确报「底链缺失」并给出可回滚性判定。
func TestAnnotateBacking_RollableSnapshotWithoutBackingIsMissing(t *testing.T) {
	svc, _, db, inst, _ := newSnapshotHarness(t)

	orphan := &model.InstanceSnapshot{
		InstanceID: inst.ID, Name: "无底链但状态完成", Kind: model.SnapshotKindManual,
		State: model.SnapshotStateCompleted, RootBackupID: 0,
	}
	require.NoError(t, db.Create(orphan).Error)

	list, err := svc.ListByInstance(inst.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, model.SnapshotBackingMissing, list[0].RootBackupState)
	require.NotEmpty(t, list[0].NotRollableReason, "状态可回滚却无底链必须显式拒绝")
	require.False(t, list[0].EffectiveRollable())

	_, err = svc.GetRollableByID(orphan.ID)
	require.ErrorIs(t, err, ErrSnapshotNotRollable)
}

// TestSweepState_SkipsInFlightSnapshot R3：启动清扫必须跳过**本进程正在推进**的快照。
//
// 缺陷现场：`sweepStartupOrphans` 的判据是全局 `state = 'running'`，不含任何
// 「是不是本进程发起的」信息；它的安全性**只**来自 main.go 里 `Start()`（685）
// 排在 gRPC 监听（931）与 HTTP 监听（950）之前。任何后续改动（把 Start 挪到 r.Run
// 之后、新增第二个 Start 调用点、复用为多进程形态）都会让它静默变成破坏性操作：
// 把正在归档的快照判为 failed，而底层 Backup 可能已在落盘，留下假失败 + 认不出的孤儿归档。
//
// 本测试直接把「在途登记」摆好再调清扫，锁定代码级约束——不依赖任何调用顺序假设。
func TestSweepState_SkipsInFlightSnapshot(t *testing.T) {
	svc, _, db, inst, _ := newSnapshotHarness(t)

	inFlight := &model.InstanceSnapshot{
		InstanceID: inst.ID, Name: "本进程正在归档", Kind: model.SnapshotKindManual,
		State: model.SnapshotStateRunning,
	}
	orphan := &model.InstanceSnapshot{
		InstanceID: inst.ID, Name: "上个进程残留", Kind: model.SnapshotKindManual,
		State: model.SnapshotStateRunning,
	}
	require.NoError(t, db.Create(inFlight).Error)
	require.NoError(t, db.Create(orphan).Error)

	svc.markSnapshotInFlight(inFlight.ID)
	defer svc.clearSnapshotInFlight(inFlight.ID)

	require.Equal(t, 1, svc.sweepStartupOrphans(), "只应清扫不在途的那一条")

	var gotInFlight model.InstanceSnapshot
	require.NoError(t, db.First(&gotInFlight, inFlight.ID).Error)
	require.Equal(t, model.SnapshotStateRunning, gotInFlight.State,
		"本进程在途的快照绝不能被判为孤儿")

	var gotOrphan model.InstanceSnapshot
	require.NoError(t, db.First(&gotOrphan, orphan.ID).Error)
	require.Equal(t, model.SnapshotStateFailed, gotOrphan.State)
	require.Contains(t, gotOrphan.FailureReason, "控制面重启导致归档中断")
}

// TestSweepState_PendingSkipsInFlight 同样的约束覆盖 pending 阶段（两条清扫路径都改过）。
func TestSweepState_PendingSkipsInFlight(t *testing.T) {
	svc, _, db, inst, _ := newSnapshotHarness(t)

	inFlight := &model.InstanceSnapshot{
		InstanceID: inst.ID, Name: "刚登记尚未起跑", Kind: model.SnapshotKindManual,
		State: model.SnapshotStatePending,
	}
	orphan := &model.InstanceSnapshot{
		InstanceID: inst.ID, Name: "残留 pending", Kind: model.SnapshotKindManual,
		State: model.SnapshotStatePending,
	}
	require.NoError(t, db.Create(inFlight).Error)
	require.NoError(t, db.Create(orphan).Error)

	svc.markSnapshotInFlight(inFlight.ID)
	defer svc.clearSnapshotInFlight(inFlight.ID)

	require.Equal(t, 1, svc.sweepStartupPendingOrphans())

	var gotInFlight model.InstanceSnapshot
	require.NoError(t, db.First(&gotInFlight, inFlight.ID).Error)
	require.Equal(t, model.SnapshotStatePending, gotInFlight.State)
}

// TestPruneInstanceOnce_OnlyTouchesTargetInstance R10：回滚后的同步裁剪只处理本实例。
//
// 缺陷现场：`pruneOnce` 全平台扫描（每个有快照的实例 2~3 次查询），而它也被
// **回滚成功后同步调用**——用户在 UI 点一次回滚就触发一轮全平台扫描，
// 成本落在用户等待的任务体内。回滚只改变目标实例的快照集合，其它实例交由周期巡检。
func TestPruneInstanceOnce_OnlyTouchesTargetInstance(t *testing.T) {
	svc, _, db, inst, _ := newSnapshotHarness(t)
	svc.SetSettingsReader(stubSettings{
		SettingKeySnapshotRetentionCount:  "1",
		SettingKeySnapshotRetentionDays:   "0", // 只测条数维
		SettingKeySnapshotPreRollbackKeep: "1",
	})

	other := &model.Instance{
		UUID: "inst-other-" + t.Name(), NodeID: inst.NodeID, Name: "其它实例",
		Type: model.InstanceTypeGeneric, Role: model.InstanceRoleUniversal,
		ProcessType: model.ProcessTypeDaemon, Status: model.InstanceStatusStopped,
		StartCommand: "./beacon",
	}
	require.NoError(t, db.Create(other).Error)

	// 目标实例：2 条 manual（保留最新 1 条 → 裁 1 条）。
	// 其它实例：同样 2 条，但不得被本实例的裁剪波及。
	mk := func(instanceID uint, name string, ageDays int) uint {
		snap := &model.InstanceSnapshot{
			InstanceID: instanceID, Name: name, Kind: model.SnapshotKindManual,
			State: model.SnapshotStateCompleted,
		}
		require.NoError(t, db.Create(snap).Error)
		require.NoError(t, db.Model(&model.InstanceSnapshot{}).Where("id = ?", snap.ID).
			Update("created_at", time.Now().AddDate(0, 0, -ageDays)).Error)
		return snap.ID
	}
	mk(inst.ID, "目标-旧", 2)
	targetNew := mk(inst.ID, "目标-新", 1)
	otherOld := mk(other.ID, "其它-旧", 2)
	otherNew := mk(other.ID, "其它-新", 1)

	require.Equal(t, 1, svc.pruneInstanceOnce(inst.ID), "只应裁掉目标实例的超额快照")

	var cnt int64
	require.NoError(t, db.Model(&model.InstanceSnapshot{}).Where("id = ?", targetNew).Count(&cnt).Error)
	require.EqualValues(t, 1, cnt, "目标实例保留最新一条")
	require.NoError(t, db.Model(&model.InstanceSnapshot{}).Where("id IN ?", []uint{otherOld, otherNew}).Count(&cnt).Error)
	require.EqualValues(t, 2, cnt, "其它实例的快照不得被本实例的裁剪波及")
}

// TestPruneInstanceOnce_ZeroIDIsNoop 传入 0 视为无目标，不做任何事（避免退化成全平台扫描）。
func TestPruneInstanceOnce_ZeroIDIsNoop(t *testing.T) {
	svc, _, db, inst, _ := newSnapshotHarness(t)
	svc.SetSettingsReader(stubSettings{
		SettingKeySnapshotRetentionCount:  "1",
		SettingKeySnapshotRetentionDays:   "0",
		SettingKeySnapshotPreRollbackKeep: "1",
	})
	for i := 0; i < 3; i++ {
		require.NoError(t, db.Create(&model.InstanceSnapshot{
			InstanceID: inst.ID, Name: "s", Kind: model.SnapshotKindManual,
			State: model.SnapshotStateCompleted,
		}).Error)
	}
	require.Zero(t, svc.pruneInstanceOnce(0))
	var cnt int64
	require.NoError(t, db.Model(&model.InstanceSnapshot{}).Count(&cnt).Error)
	require.EqualValues(t, 3, cnt)
}
