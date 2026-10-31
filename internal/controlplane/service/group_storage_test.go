package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// groupStorageFixture 造一个组 + 若干实例，返回实例列表。
//
// 每个实例可选地挂一份「备份大小」（用于回退口径）与一份「真实工作目录读数」。
func groupStorageFixture(t *testing.T, db *gorm.DB, groupID uint, specs []struct {
	workDirBytes int64
	available    bool
	backupMB     float64
}) []*model.Instance {
	t.Helper()
	insts := make([]*model.Instance, 0, len(specs))
	for i, spec := range specs {
		inst := &model.Instance{
			NodeID: 1, Name: "i" + string(rune('a'+i)),
			Type: model.InstanceTypeGeneric, Role: model.InstanceRoleUniversal,
			ProcessType:           model.ProcessTypeDaemon,
			Status:                model.InstanceStatusRunning,
			StartCommand:          "./beacon",
			WorkDirBytes:          spec.workDirBytes,
			WorkDirBytesAvailable: spec.available,
		}
		require.NoError(t, db.Create(inst).Error)
		if groupID != 0 {
			require.NoError(t, db.Create(&model.GroupInstance{GroupID: groupID, InstanceID: inst.ID}).Error)
		}
		if spec.backupMB > 0 {
			require.NoError(t, db.Create(&model.Backup{
				UUID: "bk-" + inst.UUID, InstanceID: inst.ID, Name: "bk",
				FileSizeMB: spec.backupMB, Status: model.BackupStatusCompleted,
			}).Error)
		}
		insts = append(insts, inst)
	}
	return insts
}

// storageSpec 便于用字面量表达 fixture 输入。
type storageSpec = struct {
	workDirBytes int64
	available    bool
	backupMB     float64
}

const mb = int64(1024 * 1024)

// TestGroupUsedStorageMB_AllLive 全部实时可用：只用工作目录读数，**不看备份大小**。
func TestGroupUsedStorageMB_AllLive(t *testing.T) {
	db := newInstanceTestDB(t)
	groupStorageFixture(t, db, 100, []storageSpec{
		{workDirBytes: 100 * mb, available: true, backupMB: 999}, // 备份大小必须被忽略
		{workDirBytes: 50 * mb, available: true, backupMB: 0},
	})

	got, err := groupUsedStorageMB(db, 100)
	require.NoError(t, err)
	require.Equal(t, float64(150), got)
}

// TestGroupUsedStorageMB_MixedFallback 部分可用：可用实例取实时值，不可用实例回退备份大小。
//
// 这是「逐实例回退」而非「整组二选一」的核心断言：组内常混着刚建的新实例
// （还没跑够一拍心跳）与老实例，整组二选一会让新实例把整组精度拖回备份估算。
func TestGroupUsedStorageMB_MixedFallback(t *testing.T) {
	db := newInstanceTestDB(t)
	groupStorageFixture(t, db, 101, []storageSpec{
		{workDirBytes: 200 * mb, available: true, backupMB: 5},
		{workDirBytes: 0, available: false, backupMB: 40}, // 回退：40 MB
		{workDirBytes: 30 * mb, available: true, backupMB: 7},
	})

	got, err := groupUsedStorageMB(db, 101)
	require.NoError(t, err)
	require.Equal(t, float64(200+30+40), got)
}

// TestGroupUsedStorageMB_NoneAvailableFallsBackToBackups 全部不可用：退回备份估算。
func TestGroupUsedStorageMB_NoneAvailableFallsBackToBackups(t *testing.T) {
	db := newInstanceTestDB(t)
	groupStorageFixture(t, db, 102, []storageSpec{
		{workDirBytes: 0, available: false, backupMB: 12},
		{workDirBytes: 0, available: false, backupMB: 8},
	})

	got, err := groupUsedStorageMB(db, 102)
	require.NoError(t, err)
	require.Equal(t, float64(20), got)
}

// TestGroupUsedStorageMB_UnavailableNotCountedAsZero **回退不能低估**（本 FR 的核心约束）。
//
// Available=false 的实例必须按备份大小计入，而不是 0。若按 0 计，下面这个 1 GB 上限
// 的组会算出「已用 50 MB」并放行创建，实际已占 1.05 GB —— 超卖。
func TestGroupUsedStorageMB_UnavailableNotCountedAsZero(t *testing.T) {
	db := newInstanceTestDB(t)
	groupStorageFixture(t, db, 103, []storageSpec{
		{workDirBytes: 50 * mb, available: true, backupMB: 0},
		{workDirBytes: 0, available: false, backupMB: 1000}, // 1 GB 备份
	})

	got, err := groupUsedStorageMB(db, 103)
	require.NoError(t, err)
	require.Equal(t, float64(50+1000), got,
		"不可用实例必须回退备份大小；按 0 计会低估 1 GB（1000 MB）用量")
}

// TestGroupUsedStorageMB_NeverReportedFallsBack 从未上报过（默认零值）同样回退备份。
//
// 老 Worker / 新建实例走这条路径：work_dir_bytes_available 默认 false。
func TestGroupUsedStorageMB_NeverReportedFallsBack(t *testing.T) {
	db := newInstanceTestDB(t)
	groupStorageFixture(t, db, 104, []storageSpec{
		{workDirBytes: 0, available: false, backupMB: 300},
	})

	got, err := groupUsedStorageMB(db, 104)
	require.NoError(t, err)
	require.Equal(t, float64(300), got)
}

// TestGroupUsedStorageMB_EmptyGroup 空组用量为 0。
func TestGroupUsedStorageMB_EmptyGroup(t *testing.T) {
	db := newInstanceTestDB(t)
	got, err := groupUsedStorageMB(db, 105)
	require.NoError(t, err)
	require.Equal(t, float64(0), got)
}

// TestGroupUsedStorageMB_ZeroGroupIDIsZero 无组（0）用量为 0，不做任何查询。
func TestGroupUsedStorageMB_ZeroGroupIDIsZero(t *testing.T) {
	db := newInstanceTestDB(t)
	got, err := groupUsedStorageMB(db, 0)
	require.NoError(t, err)
	require.Equal(t, float64(0), got)
}

// TestGroupUsedStorageMB_SubMBCeilRounded 不足 1 MB 的总量向上取整。
//
// 向下取整会让每个实例都白送 0.x MB：64 个实例就是几十 MB 的系统性低估。
func TestGroupUsedStorageMB_SubMBCeilRounded(t *testing.T) {
	db := newInstanceTestDB(t)
	groupStorageFixture(t, db, 106, []storageSpec{
		{workDirBytes: mb / 4, available: true}, // 0.25 MB
	})

	got, err := groupUsedStorageMB(db, 106)
	require.NoError(t, err)
	require.Equal(t, float64(1), got, "0.25 MB 必须向上取整为 1 MB（向下取整是系统性低估）")
}

// TestQuotaStorageConsistency_BothPathsAgree 创建期口径与运行期口径必须一致（关键约束）。
//
// 两处换口径时只改一处的后果正是「创建时按备份估算判通过、运行期按真实占用判超限」——
// 实例刚建好就被强制停服。本测试把两条路径的用量断言在同一组数据上，任何单边漂移都会失败。
func TestQuotaStorageConsistency_BothPathsAgree(t *testing.T) {
	db := quotaTestDB(t)
	require.NoError(t, db.Create(&model.GroupQuota{GroupID: 200, MaxStorageMB: 1000}).Error)
	groupStorageFixture(t, db, 200, []storageSpec{
		{workDirBytes: 300 * mb, available: true},
		{workDirBytes: 0, available: false, backupMB: 100},
	})

	// 路径 1：配额页（authz.GetQuotaUsage）。
	usage, err := NewAuthzService(db).GetQuotaUsage(200)
	require.NoError(t, err)

	// 路径 2：运行期强制（quota.groupDiskFreeMB 反推已用量）。
	free, err := groupDiskFreeMB(db, 200)
	require.NoError(t, err)

	require.Equal(t, 400, usage.UsedStorageMB, "配额页用量 = 300 实时 + 100 回退")
	require.Equal(t, int64(1000-400), free, "运行期余量必须用同一份用量算：1000 - 400")

	// 路径 3：创建期检查读的是同一个 groupUsedStorageMB。
	usedForCreate, err := groupUsedStorageMB(db, 200)
	require.NoError(t, err)
	require.Equal(t, float64(usage.UsedStorageMB), usedForCreate,
		"创建期与配额页必须得到同一个用量")
}

// TestGetQuotaUsage_SwitchesToLiveWorkDirBytes 配额页在有实时读数时不再用备份估算。
func TestGetQuotaUsage_SwitchesToLiveWorkDirBytes(t *testing.T) {
	db := newInstanceTestDB(t)
	groupStorageFixture(t, db, 107, []storageSpec{
		// 备份 5 MB（旧口径会显示 5），真实占用 800 MB（新口径显示 800）。
		{workDirBytes: 800 * mb, available: true, backupMB: 5},
	})
	require.NoError(t, db.Create(&model.GroupQuota{GroupID: 107, MaxStorageMB: 1000}).Error)

	usage, err := NewAuthzService(db).GetQuotaUsage(107)
	require.NoError(t, err)
	require.Equal(t, 800, usage.UsedStorageMB,
		"有实时读数时必须用工作目录占用，而不是备份大小（旧口径会给出 5）")
}

// TestGroupDiskFreeMB_SwitchesToLiveWorkDirBytes 运行期余量同样切到实时读数。
func TestGroupDiskFreeMB_SwitchesToLiveWorkDirBytes(t *testing.T) {
	db := quotaTestDB(t)
	require.NoError(t, db.Create(&model.GroupQuota{GroupID: 201, MaxStorageMB: 1000}).Error)
	groupStorageFixture(t, db, 201, []storageSpec{
		{workDirBytes: 900 * mb, available: true, backupMB: 5},
	})

	free, err := groupDiskFreeMB(db, 201)
	require.NoError(t, err)
	require.Equal(t, int64(100), free, "余量按实时占用算：1000 - 900")
}

// TestGroupDiskFreeMB_OverQuotaReturnsMinPositive 已超配额时给 1MB 最小正数（0 会被读成「不限」）。
func TestGroupDiskFreeMB_OverQuotaReturnsMinPositive(t *testing.T) {
	db := quotaTestDB(t)
	require.NoError(t, db.Create(&model.GroupQuota{GroupID: 202, MaxStorageMB: 100}).Error)
	groupStorageFixture(t, db, 202, []storageSpec{
		{workDirBytes: 500 * mb, available: true},
	})

	free, err := groupDiskFreeMB(db, 202)
	require.NoError(t, err)
	require.Equal(t, int64(1), free, "超限时必须是 1 而不是 0——0 表示「不限」，强制会失效")
}
