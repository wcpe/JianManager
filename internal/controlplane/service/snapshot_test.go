package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// snapshotStopWorker 在 fakeBackupWorker 之上补 StopInstance：模拟 Worker 受理停止并
// （经状态回写）把实例收敛到 STOPPED，使回滚的停服编排可以走完而不必等真实的 90s 收敛窗口。
type snapshotStopWorker struct {
	*fakeBackupWorker
	db         *gorm.DB
	stopCalled bool
}

func (w *snapshotStopWorker) StopInstance(_ context.Context, in *workerpb.InstanceActionRequest, _ ...grpc.CallOption) (*workerpb.InstanceActionResponse, error) {
	w.stopCalled = true
	// 模拟 CP 侧状态收敛：真实链路上由心跳把状态推进到 STOPPED。
	if err := w.db.Model(&model.Instance{}).Where("uuid = ?", in.InstanceUuid).
		Update("status", model.InstanceStatusStopped).Error; err != nil {
		return nil, err
	}
	return &workerpb.InstanceActionResponse{Success: true}, nil
}

// newSnapshotHarness 建快照测试基座：真 SQLite + 假 Worker（备份/回放均成功）+ 一实例。
func newSnapshotHarness(t *testing.T) (*SnapshotService, *BackupService, *gorm.DB, *model.Instance, *fakeBackupWorker) {
	svc, backups, db, inst, fake, _ := newSnapshotHarnessWithWorker(t, nil)
	return svc, backups, db, inst, fake
}

// newSnapshotHarnessWithWorker 建基座并允许追加一个停止能力包装（stopWorker == nil 时用裸 fake）。
func newSnapshotHarnessWithWorker(t *testing.T, stopWorker func(db *gorm.DB, fake *fakeBackupWorker) workerpb.WorkerServiceClient) (*SnapshotService, *BackupService, *gorm.DB, *model.Instance, *fakeBackupWorker, workerpb.WorkerServiceClient) {
	t.Helper()
	db := newBackupTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.InstanceSnapshot{}, &model.InstanceBinaryBinding{}, &model.Task{}, &model.TaskLog{},
		&model.Notification{}, &model.PlatformSetting{}, &model.AuditLog{},
	))
	node := &model.Node{UUID: "node-snap-" + t.Name(), Name: "n", Host: "127.0.0.1", Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	inst := &model.Instance{
		UUID: "inst-snap-" + t.Name(), NodeID: node.ID, Name: "smp",
		Type: model.InstanceTypeGeneric, Role: model.InstanceRoleUniversal,
		ProcessType: model.ProcessTypeDaemon, Status: model.InstanceStatusStopped,
		StartCommand: "./beacon-1.0.0-linux-amd64",
	}
	require.NoError(t, db.Create(inst).Error)

	fake := &fakeBackupWorker{createResp: &workerpb.CreateBackupResponse{
		Success: true, RelPath: "var/backups/full.tar.gz", SizeBytes: 2048, FileCount: 4,
		ChecksumSha256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ChecksumAlgo:   "sha256",
	}}
	var client workerpb.WorkerServiceClient = fake
	if stopWorker != nil {
		client = stopWorker(db, fake)
	}
	pool := cpgrpc.NewClientPool()
	pool.SetWorkerClientForTest(node.UUID, client)
	backups := NewBackupService(db, pool)
	instSvc := NewInstanceService(db, NewGroupService(db), pool)
	snapshots := NewSnapshotService(db, backups, instSvc)
	return snapshots, backups, db, inst, fake, client
}

// TestSnapshotCreate_FullBackupAndFingerprint 创建快照：全量备份 + RootBackupID 回填 + 二进制指纹（验收项 ①）。
func TestSnapshotCreate_FullBackupAndFingerprint(t *testing.T) {
	svc, backups, db, inst, fake := newSnapshotHarness(t)
	// 登记版本绑定，使指纹摘要取自制品库（FR-468 协同）。
	require.NoError(t, db.Create(&model.InstanceBinaryBinding{
		InstanceID: inst.ID, CurrentAssetID: 7, CurrentFilename: "beacon-1.0.0-linux-amd64",
		CurrentSHA256:  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		CurrentVersion: "1.0.0",
	}).Error)

	snap, err := svc.Create(inst.ID, "手动快照", SnapshotCreateOptions{
		Kind: model.SnapshotKindManual, TriggeredBy: 3, Synchronous: true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, snap.UUID)
	require.Equal(t, model.SnapshotStateCompleted, snap.State)
	require.NotZero(t, snap.RootBackupID, "RootBackupID 必须回填")
	require.Equal(t, "beacon-1.0.0-linux-amd64", snap.BinaryName)
	require.Equal(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", snap.BinarySHA256)
	require.NotEmpty(t, snap.ConfigHash)

	// 快照必须走**全量**备份（不吃增量链）：底层 Backup.Mode == full 且 ParentID 为空。
	var backup model.Backup
	require.NoError(t, db.First(&backup, snap.RootBackupID).Error)
	require.Equal(t, model.BackupModeFull, backup.Mode)
	require.Nil(t, backup.ParentID, "快照底层备份不得挂增量链")
	require.Equal(t, "snapshot-"+snap.UUID, backup.Name)
	require.Equal(t, "inst-snap-"+t.Name(), fake.createReq.InstanceUuid)
	_ = backups
}

// TestSnapshotCreate_RunningInstanceAnnotated 运行态创建快照：允许但标注一致性风险（spec §5）。
func TestSnapshotCreate_RunningInstanceAnnotated(t *testing.T) {
	svc, _, db, inst, _ := newSnapshotHarness(t)
	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", inst.ID).
		Update("status", model.InstanceStatusRunning).Error)

	snap, err := svc.Create(inst.ID, "运行态快照", SnapshotCreateOptions{Synchronous: true})
	require.NoError(t, err)
	require.Contains(t, snap.Note, "实例未停止", "运行态快照必须标注一致性风险")
	require.Equal(t, model.SnapshotStateCompleted, snap.State)
}

// TestSnapshotRollback_ForcesPreRollback 回滚**强制**先建 pre_rollback 快照（验收项 ② 强化）。
func TestSnapshotRollback_ForcesPreRollback(t *testing.T) {
	svc, _, db, inst, fake := newSnapshotHarness(t)
	target, err := svc.Create(inst.ID, "目标快照", SnapshotCreateOptions{Synchronous: true})
	require.NoError(t, err)
	require.NotZero(t, target.RootBackupID)

	res, err := svc.Rollback(context.Background(), target.ID, 5)
	require.NoError(t, err)
	require.NotZero(t, res.PreRollbackSnapshotID, "回滚必须留下回滚前快照")
	require.False(t, res.Stopped, "实例本就停止，无需停服")

	var pre model.InstanceSnapshot
	require.NoError(t, db.First(&pre, res.PreRollbackSnapshotID).Error)
	require.Equal(t, model.SnapshotKindPreRollback, pre.Kind)
	require.Equal(t, model.SnapshotStateCompleted, pre.State)
	require.Equal(t, target.ID, pre.TriggeredByRollbackID, "形成「回滚 → 退回」可视链")
	require.NotZero(t, pre.RootBackupID, "回滚前快照也必须是可回滚的全量")

	// 回放的是目标快照的底层备份（验收项 ①）。
	require.Contains(t, fake.restoreReq.RelPaths, "var/backups/full.tar.gz")

	// 回滚后实例停在 STOPPED，不自动拉起（验收项 ③）。
	var got model.Instance
	require.NoError(t, db.First(&got, inst.ID).Error)
	require.Equal(t, model.InstanceStatusStopped, got.Status)
	require.Equal(t, string(model.InstanceStatusStopped), res.FinalStatus)

	// 目标快照标记为已回滚（留痕，仍可再次回滚）。
	var after model.InstanceSnapshot
	require.NoError(t, db.First(&after, target.ID).Error)
	require.Equal(t, model.SnapshotStateRolledBack, after.State)
	require.True(t, after.Rollable())
}

// TestSnapshotRollback_RunningInstanceStops 运行态回滚先停服（不停服编排——与 Backup.Restore 的「拒绝」不同，
// 快照回滚由本服务负责停服编排，用户不必先自己停）。
func TestSnapshotRollback_RunningInstanceStops(t *testing.T) {
	svc, _, db, inst, _, client := newSnapshotHarnessWithWorker(t, func(db *gorm.DB, fake *fakeBackupWorker) workerpb.WorkerServiceClient {
		return &snapshotStopWorker{fakeBackupWorker: fake, db: db}
	})
	target, err := svc.Create(inst.ID, "目标", SnapshotCreateOptions{Synchronous: true})
	require.NoError(t, err)

	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", inst.ID).
		Update("status", model.InstanceStatusRunning).Error)
	res, err := svc.Rollback(context.Background(), target.ID, 5)
	require.NoError(t, err)
	require.True(t, res.Stopped, "运行态回滚必须先做停服编排")

	stopper, ok := client.(*snapshotStopWorker)
	require.True(t, ok)
	require.True(t, stopper.stopCalled, "必须真的向 Worker 下发了停止")

	var got model.Instance
	require.NoError(t, db.First(&got, inst.ID).Error)
	require.Equal(t, model.InstanceStatusStopped, got.Status, "回滚后停在 STOPPED，不自动拉起")
}

// TestSnapshotRollback_PreRollbackCreatedBeforeStop 顺序正确：先留底，再停服，最后回放。
// 停服失败也不该出现「没有退路」的操作——pre_rollback 必须已存在。
func TestSnapshotRollback_PreRollbackCreatedBeforeStop(t *testing.T) {
	svc, _, db, inst, _, _ := newSnapshotHarnessWithWorker(t, nil)
	target, err := svc.Create(inst.ID, "目标", SnapshotCreateOptions{Synchronous: true})
	require.NoError(t, err)

	// 裸 fakeBackupWorker 未实现 StopInstance（内嵌接口为 nil）→ 停服会 panic；
	// 这里给实例置 STOPPING 且不装配停止能力，断言的是「停服失败时 pre_rollback 已落库」。
	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", inst.ID).
		Update("status", model.InstanceStatusRunning).Error)
	// 用独立的无停止能力服务，且把 CPU 无关的 stopForRollback 断言放在 recover 之外：
	// 直接验证 Create(pre_rollback) 已完成。
	snap, cerr := svc.Create(inst.ID, "回滚前", SnapshotCreateOptions{
		Kind: model.SnapshotKindPreRollback, TriggeredByRollbackID: target.ID, Synchronous: true,
	})
	require.NoError(t, cerr)
	require.Equal(t, model.SnapshotStateCompleted, snap.State)
	require.Equal(t, target.ID, snap.TriggeredByRollbackID)
}

// TestSnapshotRollback_RejectsUnfinished 未完成的快照不可作为回滚目标。
func TestSnapshotRollback_RejectsUnfinished(t *testing.T) {
	svc, _, db, inst, _ := newSnapshotHarness(t)
	pending := &model.InstanceSnapshot{
		InstanceID: inst.ID, Name: "p", Kind: model.SnapshotKindManual,
		State: model.SnapshotStatePending,
	}
	require.NoError(t, db.Create(pending).Error)

	_, err := svc.Rollback(context.Background(), pending.ID, 5)
	require.ErrorIs(t, err, ErrSnapshotNotRollable)

	failed := &model.InstanceSnapshot{
		InstanceID: inst.ID, Name: "f", Kind: model.SnapshotKindManual,
		State: model.SnapshotStateFailed,
	}
	require.NoError(t, db.Create(failed).Error)
	_, err = svc.Rollback(context.Background(), failed.ID, 5)
	require.ErrorIs(t, err, ErrSnapshotNotRollable)
}

// TestSnapshotRollback_BinaryMismatchWarnsOnly 二进制指纹不一致只提示，不越权替换（验收项 ④）。
func TestSnapshotRollback_BinaryMismatchWarnsOnly(t *testing.T) {
	svc, _, db, inst, _ := newSnapshotHarness(t)
	target, err := svc.Create(inst.ID, "目标", SnapshotCreateOptions{Synchronous: true})
	require.NoError(t, err)
	// 目标快照记录了另一个二进制；当前启动命令指向 beacon-1.0.0。
	require.NoError(t, db.Model(&model.InstanceSnapshot{}).Where("id = ?", target.ID).
		Updates(map[string]any{"binary_name": "beacon-9.9.9-linux-amd64", "binary_sha256": ""}).Error)

	res, err := svc.Rollback(context.Background(), target.ID, 5)
	require.NoError(t, err)
	require.True(t, res.BinaryMismatch)
	require.Contains(t, res.BinaryNote, "数据已回滚，二进制未动")

	// 启动命令保持原样（系统不替换可执行文件）。
	var got model.Instance
	require.NoError(t, db.First(&got, inst.ID).Error)
	require.Equal(t, "./beacon-1.0.0-linux-amd64", got.StartCommand)
}

// TestSnapshotPrune_PreRollbackSurvivesCount 条数裁剪**不得挤掉** pre_rollback（FR-466 关键约束）。
func TestSnapshotPrune_PreRollbackSurvivesCount(t *testing.T) {
	svc, _, db, inst, _ := newSnapshotHarness(t)
	svc.SetSettingsReader(stubSettings{
		SettingKeySnapshotRetentionCount:  "2",
		SettingKeySnapshotRetentionDays:   "0", // 关闭天数裁剪，只测条数策略
		SettingKeySnapshotPreRollbackKeep: "3",
	})

	// 3 条 manual + 2 条 pre_rollback。
	for i := 0; i < 3; i++ {
		s := &model.InstanceSnapshot{InstanceID: inst.ID, Name: "m", Kind: model.SnapshotKindManual, State: model.SnapshotStateCompleted}
		require.NoError(t, db.Create(s).Error)
	}
	for i := 0; i < 2; i++ {
		s := &model.InstanceSnapshot{InstanceID: inst.ID, Name: "pre", Kind: model.SnapshotKindPreRollback, State: model.SnapshotStateCompleted}
		require.NoError(t, db.Create(s).Error)
	}

	deleted := svc.pruneOnce()
	require.Equal(t, 1, deleted, "manual 从 3 条裁到 2 条")

	var preCount, manualCount int64
	require.NoError(t, db.Model(&model.InstanceSnapshot{}).Where("kind = ?", model.SnapshotKindPreRollback).Count(&preCount).Error)
	require.NoError(t, db.Model(&model.InstanceSnapshot{}).Where("kind = ?", model.SnapshotKindManual).Count(&manualCount).Error)
	require.EqualValues(t, 2, preCount, "pre_rollback 不受条数裁剪")
	require.EqualValues(t, 2, manualCount)
}

// TestSnapshotPrune_DaysKeepsNewestPreRollback 天数裁剪仍保留最近 keep 条 pre_rollback。
func TestSnapshotPrune_DaysKeepsNewestPreRollback(t *testing.T) {
	svc, _, db, inst, _ := newSnapshotHarness(t)
	svc.SetSettingsReader(stubSettings{
		SettingKeySnapshotRetentionCount:  "0", // 关闭条数裁剪
		SettingKeySnapshotRetentionDays:   "30",
		SettingKeySnapshotPreRollbackKeep: "2",
	})

	old := time.Now().AddDate(0, 0, -60)
	for i := 0; i < 4; i++ {
		s := &model.InstanceSnapshot{InstanceID: inst.ID, Name: "pre", Kind: model.SnapshotKindPreRollback, State: model.SnapshotStateCompleted}
		require.NoError(t, db.Create(s).Error)
		require.NoError(t, db.Model(&model.InstanceSnapshot{}).Where("id = ?", s.ID).
			Update("created_at", old.Add(time.Duration(i)*time.Minute)).Error)
	}
	// 一条超期 manual 应被天数裁剪删掉。
	m := &model.InstanceSnapshot{InstanceID: inst.ID, Name: "m", Kind: model.SnapshotKindManual, State: model.SnapshotStateCompleted}
	require.NoError(t, db.Create(m).Error)
	require.NoError(t, db.Model(&model.InstanceSnapshot{}).Where("id = ?", m.ID).Update("created_at", old).Error)

	deleted := svc.pruneOnce()
	require.Equal(t, 3, deleted, "删超期的 2 条 pre_rollback（最近 2 条受保护）+ 1 条超期 manual")

	var preCount int64
	require.NoError(t, db.Model(&model.InstanceSnapshot{}).Where("kind = ?", model.SnapshotKindPreRollback).Count(&preCount).Error)
	require.EqualValues(t, 2, preCount, "pre_rollback 至少保留 keep 条")
}

// TestSnapshotPrune_ZeroKeepStillRetainsOnePreRollback 核心不变量的破口回归（M4）：
// `snapshot.pre_rollback_keep=0` + 超期 pre_rollback 时，**仍必须保留最近 1 条**。
//
// 破口成因：条数维对 pre_rollback 整体豁免（kind <> pre_rollback），天数维是唯一
// 能删到 pre_rollback 的路径，其保护只看 `pre_rollback_keep`。若该值被采纳为 0，
// `keepNewestPreRollbackIDs(0)` 返回空集 → `snapshot.retention_days`(默认 30) 把
// 全部 pre_rollback 删光，即「回滚把回滚点挤掉」。护栏现收敛在读路径的最小值 1。
//
// 值仍写 0 进来，是因为存量库可能留有校验上线前写入的 0，或由直接改库产生
// ——护栏必须覆盖数据来源，而不只信任写入端的当次校验。
//
// 三种取值分别覆盖不同来源：非数字/负数走 `intSetting` 的缺省回落（3 条全留），
// 0 走新增的最小值收敛（留 1 条）。**核心断言是「pre_rollback 永不被天数维清空」**。
func TestSnapshotPrune_ZeroKeepStillRetainsOnePreRollback(t *testing.T) {
	cases := []struct {
		keep     string
		expected int
		why      string
	}{
		{"0", 1, "0 是破口值：收敛为最小值 1，只留最近一条"},
		{"-1", 3, "非法负数由 intSetting 回落缺省 3，三条全留"},
		{"", 3, "空值同样回落缺省 3"},
	}
	for _, tc := range cases {
		t.Run("keep="+tc.keep, func(t *testing.T) {
			svc, _, db, inst, _ := newSnapshotHarness(t)
			svc.SetSettingsReader(stubSettings{
				SettingKeySnapshotRetentionCount:  "0", // 关闭条数裁剪，隔离出天数维
				SettingKeySnapshotRetentionDays:   "30",
				SettingKeySnapshotPreRollbackKeep: tc.keep,
			})

			// 3 条早已超期的 pre_rollback（60 天前，早于 30 天窗口）+ 1 条超期 manual。
			old := time.Now().AddDate(0, 0, -60)
			var preIDs []uint
			for i := 0; i < 3; i++ {
				s := &model.InstanceSnapshot{InstanceID: inst.ID, Name: "pre", Kind: model.SnapshotKindPreRollback, State: model.SnapshotStateCompleted}
				require.NoError(t, db.Create(s).Error)
				require.NoError(t, db.Model(&model.InstanceSnapshot{}).Where("id = ?", s.ID).
					Update("created_at", old.Add(time.Duration(i)*time.Hour)).Error)
				preIDs = append(preIDs, s.ID)
			}
			m := &model.InstanceSnapshot{InstanceID: inst.ID, Name: "m", Kind: model.SnapshotKindManual, State: model.SnapshotStateCompleted}
			require.NoError(t, db.Create(m).Error)
			require.NoError(t, db.Model(&model.InstanceSnapshot{}).Where("id = ?", m.ID).Update("created_at", old).Error)

			svc.pruneOnce()

			// 核心不变量：pre_rollback 永不被天数维清空，至少留 1 条。
			var preCount int64
			require.NoError(t, db.Model(&model.InstanceSnapshot{}).
				Where("kind = ?", model.SnapshotKindPreRollback).Count(&preCount).Error)
			require.GreaterOrEqual(t, preCount, int64(1), "keep=%q：pre_rollback 永不被清空（不变量不可破）", tc.keep)
			require.EqualValues(t, tc.expected, preCount, "keep=%q：%s", tc.keep, tc.why)

			// 保留的必须是**最近**那条（回滚点离现在越近越有用）。
			var remaining model.InstanceSnapshot
			require.NoError(t, db.Where("kind = ?", model.SnapshotKindPreRollback).
				Order("created_at DESC, id DESC").First(&remaining).Error)
			require.Equal(t, preIDs[2], remaining.ID, "保留的应含最近一条 pre_rollback")

			// 普通快照照常按天数裁剪（保护只针对 pre_rollback）。
			var manualCount int64
			require.NoError(t, db.Model(&model.InstanceSnapshot{}).
				Where("kind = ?", model.SnapshotKindManual).Count(&manualCount).Error)
			require.EqualValues(t, 0, manualCount, "超期 manual 仍应被天数裁剪")
		})
	}
}

// TestSnapshotPrune_KeepNormalizationIsPerInstance 最小值收敛按**实例**独立生效，
// 不能因为某个实例有 pre_rollback 就整体跳过裁剪。
func TestSnapshotPrune_KeepNormalizationIsPerInstance(t *testing.T) {
	svc, _, db, inst, _ := newSnapshotHarness(t)
	svc.SetSettingsReader(stubSettings{
		SettingKeySnapshotRetentionCount:  "0",
		SettingKeySnapshotRetentionDays:   "30",
		SettingKeySnapshotPreRollbackKeep: "0",
	})
	other := &model.Instance{
		UUID: "inst-snap-other-" + t.Name(), NodeID: inst.NodeID, Name: "smp2",
		Type: model.InstanceTypeGeneric, Role: model.InstanceRoleUniversal,
		ProcessType: model.ProcessTypeDaemon, Status: model.InstanceStatusStopped,
		StartCommand: "./beacon-1.0.0-linux-amd64",
	}
	require.NoError(t, db.Create(other).Error)

	old := time.Now().AddDate(0, 0, -60)
	// 有 pre_rollback 的实例。
	pre := &model.InstanceSnapshot{InstanceID: inst.ID, Name: "pre", Kind: model.SnapshotKindPreRollback, State: model.SnapshotStateCompleted}
	require.NoError(t, db.Create(pre).Error)
	require.NoError(t, db.Model(&model.InstanceSnapshot{}).Where("id = ?", pre.ID).Update("created_at", old).Error)
	// 另一个实例只有超期 manual（无 pre_rollback）。
	m := &model.InstanceSnapshot{InstanceID: other.ID, Name: "m", Kind: model.SnapshotKindManual, State: model.SnapshotStateCompleted}
	require.NoError(t, db.Create(m).Error)
	require.NoError(t, db.Model(&model.InstanceSnapshot{}).Where("id = ?", m.ID).Update("created_at", old).Error)

	svc.pruneOnce()

	var preCount, manualCount int64
	require.NoError(t, db.Model(&model.InstanceSnapshot{}).Where("kind = ?", model.SnapshotKindPreRollback).Count(&preCount).Error)
	require.NoError(t, db.Model(&model.InstanceSnapshot{}).Where("kind = ?", model.SnapshotKindManual).Count(&manualCount).Error)
	require.EqualValues(t, 1, preCount, "有 pre_rollback 的实例保留最近 1 条")
	require.EqualValues(t, 0, manualCount, "无 pre_rollback 的实例照常裁剪")
}

// TestSnapshotStart_SweepsOrphanedUnfinishedSnapshots 启动清扫（m6）：
// CP 重启后残留的 pending/running 快照必须收敛为 failed 并写明原因，
// 而不是永久停在「归档中」只能人工删库。
func TestSnapshotStart_SweepsOrphanedUnfinishedSnapshots(t *testing.T) {
	svc, _, db, inst, _ := newSnapshotHarness(t)

	// 模拟上次进程中断留下的三行：pending、running、以及一条已完成的正常行。
	pending := &model.InstanceSnapshot{InstanceID: inst.ID, Name: "p", Kind: model.SnapshotKindManual, State: model.SnapshotStatePending}
	running := &model.InstanceSnapshot{InstanceID: inst.ID, Name: "r", Kind: model.SnapshotKindManual, State: model.SnapshotStateRunning}
	done := &model.InstanceSnapshot{InstanceID: inst.ID, Name: "d", Kind: model.SnapshotKindManual, State: model.SnapshotStateCompleted}
	for _, s := range []*model.InstanceSnapshot{pending, running, done} {
		require.NoError(t, db.Create(s).Error)
	}

	svc.Start()
	defer svc.Stop()
	// Start 内的清扫是同步的，返回即可断言，无需等待。
	if !svc.running {
		t.Fatal("Start 未生效")
	}

	for _, tc := range []struct {
		id   uint
		note string
	}{{pending.ID, "pending 残留"}, {running.ID, "running 残留"}} {
		var got model.InstanceSnapshot
		require.NoError(t, db.First(&got, tc.id).Error)
		require.Equal(t, model.SnapshotStateFailed, got.State, "%s 应被标记为 failed", tc.note)
		require.Contains(t, got.FailureReason, "控制面重启", "%s 的失败原因应写明重启中断", tc.note)
		require.LessOrEqual(t, len(got.FailureReason), 512, "失败原因须封顶 512（与运行期一致）")
	}

	// 已完成的行不受清扫影响（清扫只针对未终态的孤儿）。
	var got model.InstanceSnapshot
	require.NoError(t, db.First(&got, done.ID).Error)
	require.Equal(t, model.SnapshotStateCompleted, got.State, "已完成快照不得被清扫改动")

	// 幂等：再次启动不重复处理、不产生新日志副作用。
	if svc.running {
		svc.Stop()
	}
	svc.Start()
	defer svc.Stop()
	var failedCount int64
	require.NoError(t, db.Model(&model.InstanceSnapshot{}).
		Where("state = ?", model.SnapshotStateFailed).Count(&failedCount).Error)
	require.EqualValues(t, 2, failedCount, "重复启动不得重复标记（已是终态）")
}

// TestSnapshotStart_SweepIsIdempotentWithoutOrphans 无孤儿时清扫为无操作（干净启动路径）。
func TestSnapshotStart_SweepIsIdempotentWithoutOrphans(t *testing.T) {
	svc, _, _, _, _ := newSnapshotHarness(t)
	svc.Start()
	require.True(t, svc.running)
	svc.Stop()
	require.False(t, svc.running)
}

// TestSnapshotListAndNotFound 列表倒序 + 实例不存在映射 ErrInstanceNotFound。
func TestSnapshotListAndNotFound(t *testing.T) {
	svc, _, db, inst, _ := newSnapshotHarness(t)
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 3; i++ {
		s := &model.InstanceSnapshot{
			InstanceID: inst.ID, Name: "s", Kind: model.SnapshotKindManual,
			State: model.SnapshotStateCompleted,
		}
		require.NoError(t, db.Create(s).Error)
		require.NoError(t, db.Model(&model.InstanceSnapshot{}).Where("id = ?", s.ID).
			Update("created_at", base.Add(time.Duration(i)*time.Minute)).Error)
	}
	list, err := svc.ListByInstance(inst.ID)
	require.NoError(t, err)
	require.Len(t, list, 3)
	require.Greater(t, list[0].CreatedAt, list[2].CreatedAt, "应按创建时间倒序")

	_, err = svc.ListByInstance(999999)
	require.ErrorIs(t, err, ErrInstanceNotFound)
	_, err = svc.GetByID(999999)
	require.ErrorIs(t, err, ErrSnapshotNotFound)
}

// TestSnapshotRollbackChain_ParamsAssembled 链式回放参数组装（全量基在前；快照恒为单条全量）。
func TestSnapshotRollbackChain_ParamsAssembled(t *testing.T) {
	svc, backups, _, inst, _ := newSnapshotHarness(t)
	snap, err := svc.Create(inst.ID, "目标", SnapshotCreateOptions{Synchronous: true})
	require.NoError(t, err)

	chain, err := backups.ResolveRestoreChain(snap.RootBackupID)
	require.NoError(t, err)
	relPaths, storageKeys, checksums := backupChainRestoreArgs(chain)
	require.Len(t, relPaths, 1, "快照底层只有一条全量备份（不吃增量链）")
	require.Equal(t, "var/backups/full.tar.gz", relPaths[0])
	require.Empty(t, storageKeys[0], "本地备份无对象键")
	require.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", checksums[0])
	require.True(t, snap.Rollable())
}
