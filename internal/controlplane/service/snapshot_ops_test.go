package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestSnapshotRollback_ConcurrentOnlyOneSucceeds M-4：两个并发回滚必须只有一个成功。
//
// 缺陷现场：Rollback 不取 acquireInstanceOperation，两次并发调用会各起一个 RunAsync
// （TaskService 不按实例串行化），两个全量打包 + 覆盖式回放同时作用在同一工作目录上。
// 修复后有两道闸：入队前的「在途任务」检查 + 任务体内的实例互斥锁。
func TestSnapshotRollback_ConcurrentOnlyOneSucceeds(t *testing.T) {
	svc, _, db, inst, _ := newSnapshotHarness(t)
	taskSvc := NewTaskService(db)
	svc.SetTaskService(taskSvc)

	target, err := svc.Create(inst.ID, "并发目标", SnapshotCreateOptions{Synchronous: true})
	require.NoError(t, err)

	// 制造「已有在途任务」：一条 running 的快照回滚任务（模拟另一个并发请求已入队）。
	require.NoError(t, db.Create(&model.Task{
		TaskID: "inflight-1", NodeID: inst.NodeID, InstanceID: inst.ID,
		Kind: model.TaskKindSnapshotRollback, State: model.TaskStateRunning, Title: "在途回滚",
	}).Error)

	_, err = svc.Rollback(context.Background(), target.ID, 5)
	require.ErrorIs(t, err, ErrSnapshotOperationInFlight, "同实例已有在途操作时必须拒绝")

	// 在途任务落终态后应可再次回滚（拒绝不得是永久性的）。
	require.NoError(t, db.Model(&model.Task{}).Where("task_id = ?", "inflight-1").
		Update("state", model.TaskStateSucceeded).Error)
	require.NoError(t, svc.ensureNoInflightOperation(inst.ID))
}

// TestSnapshotRollback_BinaryUpgradeBlocksRollback M-4：二进制升级在途时同样拒绝快照回滚。
//
// 二者都会大范围改写工作目录，必须落在同一互斥域内（否则升级写文件与回滚覆盖式回放交错）。
func TestSnapshotRollback_BinaryUpgradeBlocksRollback(t *testing.T) {
	svc, _, db, inst, _ := newSnapshotHarness(t)
	svc.SetTaskService(NewTaskService(db))
	target, err := svc.Create(inst.ID, "目标", SnapshotCreateOptions{Synchronous: true})
	require.NoError(t, err)

	require.NoError(t, db.Create(&model.Task{
		TaskID: "inflight-up", NodeID: inst.NodeID, InstanceID: inst.ID,
		Kind: model.TaskKindBinaryUpgrade, State: model.TaskStatePending, Title: "在途升级",
	}).Error)

	_, err = svc.Rollback(context.Background(), target.ID, 5)
	require.ErrorIs(t, err, ErrSnapshotOperationInFlight)

	// 其它实例的在途任务不应误伤本实例。
	var other model.Instance
	require.NoError(t, db.Create(&model.Instance{
		UUID: "other-inst", NodeID: inst.NodeID, Name: "other",
		Type: model.InstanceTypeGeneric, Role: model.InstanceRoleUniversal,
		ProcessType: model.ProcessTypeDaemon, Status: model.InstanceStatusStopped,
		StartCommand: "./x",
	}).Error)
	require.NoError(t, db.Model(&model.Task{}).Where("task_id = ?", "inflight-up").
		Update("instance_id", other.ID).Error)
	require.NoError(t, svc.ensureNoInflightOperation(inst.ID))
}

// TestSnapshotRollback_ConcurrentCallsSerialized M-4：并发调用下互斥锁真的串行化执行体。
//
// 直接验证 lockInstanceOperation：若互斥失效，两个 goroutine 会同时进入临界区。
func TestSnapshotRollback_ConcurrentCallsSerialized(t *testing.T) {
	svc, _, _, inst, _ := newSnapshotHarness(t)

	var (
		mu      sync.Mutex
		inside  int
		maxSeen int
		wg      sync.WaitGroup
	)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release := svc.lockInstanceOperation(inst.ID)
			if release == nil {
				return
			}
			defer release()
			mu.Lock()
			inside++
			if inside > maxSeen {
				maxSeen = inside
			}
			mu.Unlock()
			time.Sleep(5 * time.Millisecond)
			mu.Lock()
			inside--
			mu.Unlock()
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("互斥锁导致死锁")
	}
	require.Equal(t, 1, maxSeen, "同一实例的临界区必须串行（maxSeen 应为 1）")
}

// TestSnapshotRollback_FailedRestoreWritesFailureAudit M-6：回放失败必须写**失败**审计。
//
// 缺陷现场：审计在 RunAsync 返回后立刻记 success=true，任务失败不补写——
// 于是「回滚失败了」在审计里看起来是成功，事后无从追责。
func TestSnapshotRollback_FailedRestoreWritesFailureAudit(t *testing.T) {
	svc, _, db, inst, fake := newSnapshotHarness(t)
	svc.SetAudit(NewAuditService(db))

	target, err := svc.Create(inst.ID, "目标", SnapshotCreateOptions{Synchronous: true})
	require.NoError(t, err)

	// 让回放失败。
	fake.restoreResp = nil
	fake.restoreErr = errors.New("回放失败（测试注入）")

	_, err = svc.Rollback(context.Background(), target.ID, 9)
	require.Error(t, err, "回放失败必须向上报错")

	// 失败审计必须存在，且不得存在成功（failed=false）的回滚审计。
	// 注意语义：AuditLog.Failed=true 表示失败（见模型注释，规避 GORM 零值列坑）。
	var failures, successes int64
	require.NoError(t, db.Model(&model.AuditLog{}).
		Where("action = ? AND failed = ?", "instance.snapshot_rollback", true).Count(&failures).Error)
	require.NoError(t, db.Model(&model.AuditLog{}).
		Where("action = ? AND failed = ?", "instance.snapshot_rollback", false).Count(&successes).Error)
	require.EqualValues(t, 1, failures, "失败回滚必须留下失败审计")
	require.EqualValues(t, 0, successes, "失败的回滚不得记成功审计")

	// 成功路径对照：修复回放后应记一条成功审计且无新增失败。
	fake.restoreErr = nil
	_, err = svc.Rollback(context.Background(), target.ID, 9)
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.AuditLog{}).
		Where("action = ? AND failed = ?", "instance.snapshot_rollback", false).Count(&successes).Error)
	require.EqualValues(t, 1, successes, "成功回滚必须记成功审计（终态写）")
}

// TestSnapshotRollback_AbortCleansPreRollbackAndStatus M-5：停服超时中止后不留 STOPPING 与垃圾 pre_rollback。
//
// 缺陷现场：中止时 ①已建的 pre_rollback 不清理（每失败一条全量快照，且不受条数裁剪）
// ②实例停在 STOPPING 无人回退。
func TestSnapshotRollback_AbortCleansPreRollbackAndStatus(t *testing.T) {
	svc, _, db, inst, _ := newSnapshotHarness(t)
	// 实例置为 RUNNING，但 harness 不装配停止能力 → stopForRollback 直接报「无法自动停服」，
	// 等价于停服失败路径（不需要真等 90s 收敛窗口）。
	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", inst.ID).
		Update("status", model.InstanceStatusRunning).Error)

	target, err := svc.Create(inst.ID, "目标", SnapshotCreateOptions{Synchronous: true})
	require.NoError(t, err)
	// 让实例在读取时呈现 STOPPING（模拟停服未收敛）。
	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", inst.ID).
		Update("status", model.InstanceStatusStopping).Error)

	// 装配一个「Stop 失败」的实例服务：直接调用 cleanupAbortedRollback 覆盖中止清理语义。
	pre, cerr := svc.Create(inst.ID, "回滚前自动快照", SnapshotCreateOptions{
		Kind: model.SnapshotKindPreRollback, Synchronous: true,
	})
	require.NoError(t, cerr)
	svc.cleanupAbortedRollback(inst.ID, pre, errors.New("停服未收敛"))

	// ① pre_rollback 已清理（避免磁盘只增不减）。
	var preCount int64
	require.NoError(t, db.Model(&model.InstanceSnapshot{}).
		Where("id = ?", pre.ID).Count(&preCount).Error)
	require.EqualValues(t, 0, preCount, "中止回滚必须清理本次产生的 pre_rollback")

	// ② 状态回退为 STOPPED 且写明原因（供运维看到「需人工 Kill」）。
	var got model.Instance
	require.NoError(t, db.First(&got, inst.ID).Error)
	require.Equal(t, model.InstanceStatusStopped, got.Status, "STOPPING 残留必须回退")
	require.Contains(t, got.StatusReason, "回滚中止")
	require.Contains(t, got.StatusReason, "Kill")

	_ = target
}

// TestSnapshotRollback_AbortKeepsNonStoppingStatus M-5 边界：实例不是 STOPPING 时不得篡改状态。
//
// cleanupAbortedRollback 只负责「回退 STOPPING 残留」；实例已收敛到 STOPPED/CRASHED 时
// 不该被它覆盖（尤其 CRASHED 是重要事实，不能因一次中止被抹掉）。
func TestSnapshotRollback_AbortKeepsNonStoppingStatus(t *testing.T) {
	svc, _, db, inst, _ := newSnapshotHarness(t)
	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", inst.ID).
		Update("status", model.InstanceStatusCrashed).Error)

	svc.cleanupAbortedRollback(inst.ID, nil, errors.New("x"))

	var got model.Instance
	require.NoError(t, db.First(&got, inst.ID).Error)
	require.Equal(t, model.InstanceStatusCrashed, got.Status, "非 STOPPING 状态不得被回退逻辑改写")
}

// TestSnapshotCreate_RejectedWhenOperationInFlight M-4：创建快照也拒绝在途破坏性操作。
func TestSnapshotCreate_RejectedWhenOperationInFlight(t *testing.T) {
	svc, _, db, inst, _ := newSnapshotHarness(t)
	svc.SetTaskService(NewTaskService(db))

	require.NoError(t, db.Create(&model.Task{
		TaskID: "inflight-create", NodeID: inst.NodeID, InstanceID: inst.ID,
		Kind: model.TaskKindSnapshotCreate, State: model.TaskStateRunning, Title: "在途创建",
	}).Error)

	_, err := svc.Create(inst.ID, "x", SnapshotCreateOptions{Synchronous: true})
	require.ErrorIs(t, err, ErrSnapshotOperationInFlight)
}

// TestSnapshotCreate_QuotaGuard m-2：创建前的数量上限保护。
func TestSnapshotCreate_QuotaGuard(t *testing.T) {
	svc, _, db, inst, _ := newSnapshotHarness(t)
	svc.SetSettingsReader(stubSettings{
		SettingKeySnapshotRetentionDays:   "30",
		SettingKeySnapshotMaxPerInstance:  "2",
		SettingKeySnapshotMaxTotalMB:      "0",
		SettingKeySnapshotPreRollbackKeep: "3",
		SettingKeySnapshotRetentionCount:  "0",
	})

	// 预置 2 条已完成快照（达到上限）。
	for i := 0; i < 2; i++ {
		require.NoError(t, db.Create(&model.InstanceSnapshot{
			InstanceID: inst.ID, Name: "已存在", Kind: model.SnapshotKindManual,
			State: model.SnapshotStateCompleted,
		}).Error)
	}
	_, err := svc.Create(inst.ID, "超限", SnapshotCreateOptions{})
	require.ErrorIs(t, err, ErrSnapshotQuotaExceeded, "达上限必须拒绝创建")

	// 删除一条后恢复可创建。
	var one model.InstanceSnapshot
	require.NoError(t, db.Where("instance_id = ?", inst.ID).First(&one).Error)
	require.NoError(t, db.Delete(&model.InstanceSnapshot{}, one.ID).Error)
	_, err = svc.Create(inst.ID, "恢复", SnapshotCreateOptions{})
	require.NoError(t, err)
}

// TestSnapshotCreate_QuotaGuardFromSettingsService R4 端到端：写成 **SettingsService.Update**
// （真实写入链路，非 stub）后，m-2 的上限检查点必须按新值生效。
//
// 缺陷现场：`Get()` 把 snapshot.max_per_instance / max_total_mb 宣告为可编辑，`Update` 却因
// 二者不在写入白名单而必返 ErrSettingKeyNotWritable → 生效值永远是默认（20 / 0=不限）→
// 设了也没用。本用例把「写设置」与「创建前保护」串起来，锁住这条链路真的通。
func TestSnapshotCreate_QuotaGuardFromSettingsService(t *testing.T) {
	svc, _, db, inst, _ := newSnapshotHarness(t)
	require.NoError(t, db.AutoMigrate(&model.PlatformSetting{}))

	settingsSvc := NewSettingsService(db, testConfig())
	require.NoError(t, settingsSvc.Update(map[string]string{SettingKeySnapshotMaxPerInstance: "1"}),
		"运维必须能通过设置接口设下「单实例快照条数上限」")
	require.NoError(t, settingsSvc.Update(map[string]string{SettingKeySnapshotMaxTotalMB: "0"}))
	svc.SetSettingsReader(settingsSvc)

	// 上限 1：第一条成功，第二条必须被拒（修复前恒为默认 20，此处不会拒）。
	_, err := svc.Create(inst.ID, "第一条", SnapshotCreateOptions{Synchronous: true})
	require.NoError(t, err)
	_, err = svc.Create(inst.ID, "第二条", SnapshotCreateOptions{})
	require.ErrorIs(t, err, ErrSnapshotQuotaExceeded,
		"设 max_per_instance=1 后第二条必须被创建前保护拦住（证明设置真的生效）")

	// 改成 2 后恢复可创建：证明生效值是**当前设置**而非某次快照的固定值。
	require.NoError(t, settingsSvc.Update(map[string]string{SettingKeySnapshotMaxPerInstance: "2"}))
	_, err = svc.Create(inst.ID, "第三条", SnapshotCreateOptions{Synchronous: true})
	require.NoError(t, err)

	// 总量维度独立生效：按单条快照体积（2 条 × 2MiB）设 1MiB 上限 → 必须被拒。
	require.NoError(t, settingsSvc.Update(map[string]string{SettingKeySnapshotMaxTotalMB: "1"}))
	_, err = svc.Create(inst.ID, "第四条", SnapshotCreateOptions{})
	require.ErrorIs(t, err, ErrSnapshotQuotaExceeded,
		"max_total_mb=1 也必须真的生效（默认 0=不限，修复前此处不会拒）")
}
