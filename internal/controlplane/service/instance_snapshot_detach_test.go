package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// newSnapshotDeleteEnv 建「实例 + 快照 + 快照底链 + 手工备份」的删除测试环境。
func newSnapshotDeleteEnv(t *testing.T) (*InstanceService, *model.Instance, *model.InstanceSnapshot, *model.Backup, *model.Backup) {
	t.Helper()
	svc, pool, node, inst := newDeleteCleanupEnv(t)
	pool.SetWorkerClientForTest(node.UUID, &fakeRemoveWorker{})
	require.NoError(t, svc.db.AutoMigrate(&model.Backup{}, &model.InstanceSnapshot{}))

	// 一条快照（manual）及其 origin=snapshot 底链；一条用户手工备份。
	rootBackup := &model.Backup{
		UUID: "b-snapshot-root", InstanceID: inst.ID, Name: "snapshot-root",
		Origin: model.BackupOriginSnapshot, Status: model.BackupStatusCompleted,
	}
	require.NoError(t, svc.db.Create(rootBackup).Error)
	snap := &model.InstanceSnapshot{
		UUID: "snap-1", InstanceID: inst.ID, Name: "快照1",
		Kind: model.SnapshotKindManual, State: model.SnapshotStateCompleted,
		RootBackupID: rootBackup.ID,
	}
	require.NoError(t, svc.db.Create(snap).Error)

	manual := &model.Backup{
		UUID: "b-manual", InstanceID: inst.ID, Name: "手工备份",
		Origin: model.BackupOriginManual, Status: model.BackupStatusCompleted,
	}
	require.NoError(t, svc.db.Create(manual).Error)
	return svc, inst, snap, rootBackup, manual
}

// TestDeleteInstanceClearsSnapshotsButKeepsManualBackups N-6：删实例必须级联清理快照与其
// **快照专属**底链，而用户手工备份**必须保留**。
//
// 缺陷现场：删实例只级联清 GroupInstance/ServerRegistration/NetworkMember/InstanceCrashSnapshot，
// 快照行与底链全部滞留。而 B-1 让 origin='snapshot' 的底链豁免 backup.retention_days，
// 快照自身的保留策略又只对**现存快照行**生效（按 created_at/条数裁，与实例存活无关）——
// 删实例后这批底链既不被实例清理、也不再被任何策略扫描到，记录只增不减（B-1 的新泄漏路径）。
func TestDeleteInstanceClearsSnapshotsButKeepsManualBackups(t *testing.T) {
	svc, inst, snap, rootBackup, manual := newSnapshotDeleteEnv(t)

	require.NoError(t, svc.Delete(inst.ID))

	// 实例已删。
	var instCnt int64
	require.NoError(t, svc.db.Model(&model.Instance{}).Where("id = ?", inst.ID).Count(&instCnt).Error)
	require.Zero(t, instCnt, "实例记录应已删除")

	// 快照行已清（它已无宿主，留着只会指向被删的实例）。
	var snapCnt int64
	require.NoError(t, svc.db.Model(&model.InstanceSnapshot{}).Where("id = ?", snap.ID).Count(&snapCnt).Error)
	require.Zero(t, snapCnt, "实例快照必须随实例级联清理")

	// 快照专属底链已清——这是 N-6 的核心：它原先既不被实例清理、也不再被任何策略扫描。
	var rootCnt int64
	require.NoError(t, svc.db.Model(&model.Backup{}).Where("id = ?", rootBackup.ID).Count(&rootCnt).Error)
	require.Zero(t, rootCnt, "快照底链必须随实例级联清理（否则无人再管它）")

	// 用户手工备份必须保留：误删等于替用户销毁数据。
	var manualGot model.Backup
	require.NoError(t, svc.db.First(&manualGot, manual.ID).Error, "手工备份不得被实例删除牵连")
	require.Equal(t, model.BackupOriginManual, manualGot.Origin)
}

// TestDeleteInstanceKeepsBacklinkReferencedByManualIncremental 被用户手工**增量**备份占为父的
// 底链不得删除（删了就断了用户的增量链），但必须清除 snapshot 标记以回归常规裁剪范围。
//
// 这是 N-6「不误删用户手工备份」的另一面：底链本身也是**用户增量备份的基准**。
func TestDeleteInstanceKeepsBacklinkReferencedByManualIncremental(t *testing.T) {
	svc, inst, _, rootBackup, _ := newSnapshotDeleteEnv(t)

	// 用户基于该底链做了一次手工增量备份（ParentID → 底链）。
	child := &model.Backup{
		UUID: "b-manual-incr", InstanceID: inst.ID, Name: "手工增量",
		Origin: model.BackupOriginManual, Mode: model.BackupModeIncremental,
		ParentID: &rootBackup.ID, Status: model.BackupStatusCompleted,
	}
	require.NoError(t, svc.db.Create(child).Error)

	require.NoError(t, svc.Delete(inst.ID))

	// 底链保留（子备份仍引用它）。
	var got model.Backup
	require.NoError(t, svc.db.First(&got, rootBackup.ID).Error,
		"被子增量备份引用的底链不得删除，否则用户的增量链断裂")
	// 且已降级为普通备份 → 回归 backup.retention_days 裁剪，不再永久豁免。
	require.Equal(t, model.BackupOriginManual, got.Origin,
		"快照行已删，底链不应继续挂着 snapshot 豁免标记")
	// 子备份本身保留（手工备份）。
	var childGot model.Backup
	require.NoError(t, svc.db.First(&childGot, child.ID).Error)
}

// TestDeleteInstanceSnapshotCleanupIsScoped 清理只作用于被删实例：其它实例的快照与备份不受牵连。
func TestDeleteInstanceSnapshotCleanupIsScoped(t *testing.T) {
	svc, inst, snap, rootBackup, _ := newSnapshotDeleteEnv(t)

	other := &model.Instance{
		NodeID: inst.NodeID, Name: "其它实例", Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDaemon, StartCommand: "java -jar other.jar",
		Status: model.InstanceStatusStopped, WorkDir: "var/servers/other",
	}
	require.NoError(t, svc.db.Create(other).Error)
	otherBacklink := &model.Backup{
		UUID: "b-other-root", InstanceID: other.ID, Name: "别家底链",
		Origin: model.BackupOriginSnapshot, Status: model.BackupStatusCompleted,
	}
	require.NoError(t, svc.db.Create(otherBacklink).Error)
	otherSnap := &model.InstanceSnapshot{
		UUID: "snap-other", InstanceID: other.ID, Name: "别家快照",
		Kind: model.SnapshotKindManual, State: model.SnapshotStateCompleted,
		RootBackupID: otherBacklink.ID,
	}
	require.NoError(t, svc.db.Create(otherSnap).Error)

	require.NoError(t, svc.Delete(inst.ID))

	var otherSnapCnt int64
	require.NoError(t, svc.db.Model(&model.InstanceSnapshot{}).Where("id = ?", otherSnap.ID).Count(&otherSnapCnt).Error)
	require.EqualValues(t, 1, otherSnapCnt, "其它实例的快照不得被牵连清理")
	var otherRootCnt int64
	require.NoError(t, svc.db.Model(&model.Backup{}).Where("id = ?", otherBacklink.ID).Count(&otherRootCnt).Error)
	require.EqualValues(t, 1, otherRootCnt, "其它实例的底链不得被牵连清理")
	require.Equal(t, model.BackupOriginSnapshot, mustBackup(t, svc, otherBacklink.ID).Origin,
		"其它实例的底链必须保持 snapshot 豁免标记")

	// 本实例的确实清了。
	var cnt int64
	require.NoError(t, svc.db.Model(&model.InstanceSnapshot{}).Where("id = ?", snap.ID).Count(&cnt).Error)
	require.Zero(t, cnt)
	require.Zero(t, mustBackupCount(t, svc, rootBackup.ID))
}

func mustBackup(t *testing.T, svc *InstanceService, id uint) model.Backup {
	t.Helper()
	var b model.Backup
	require.NoError(t, svc.db.First(&b, id).Error)
	return b
}

func mustBackupCount(t *testing.T, svc *InstanceService, id uint) int64 {
	t.Helper()
	var n int64
	require.NoError(t, svc.db.Model(&model.Backup{}).Where("id = ?", id).Count(&n).Error)
	return n
}
