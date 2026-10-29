package grpc

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// newWorkDirUsageFixture 隔离内存库 + 一条实例（心跳落库）。
func newWorkDirUsageFixture(t *testing.T) (*ControlPlaneHandler, *gorm.DB) {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}))
	node := &model.Node{UUID: "node-workdir", Name: "n", Secret: "s", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	inst := &model.Instance{
		UUID: "i-workdir", NodeID: node.ID, Name: "w",
		Status: model.InstanceStatusRunning,
	}
	require.NoError(t, db.Create(inst).Error)
	return NewControlPlaneHandler(db, NewClientPool()), db
}

// TestApplyWorkDirUsages_AvailableWritesBytes 可用读数：bytes + available + 时刻三列同写。
func TestApplyWorkDirUsages_AvailableWritesBytes(t *testing.T) {
	h, db := newWorkDirUsageFixture(t)

	h.applyWorkDirUsages([]*workerpb.InstanceWorkDirUsage{{
		InstanceUuid: "i-workdir", WorkDirBytes: 5 << 30, Available: true,
	}})

	var got model.Instance
	require.NoError(t, db.Where("uuid = ?", "i-workdir").First(&got).Error)
	require.EqualValues(t, 5<<30, got.WorkDirBytes)
	require.True(t, got.WorkDirBytesAvailable)
	require.NotNil(t, got.WorkDirBytesAt, "成功统计必须记录观测时刻")
}

// TestApplyWorkDirUsages_UnavailableKeepsOldBytes 不可用：只降可用位，**保留旧读数**。
//
// 这是「回退不能低估」在落库层的实现：清零会让配额侧读到 0（若它忽略了 available 位），
// 或让 available=false + bytes=0 的组合在排障时被误读成「真的没占空间」。
// 保留旧值的意义是：它至少是个量级正确的下界，而 available=false 已经让配额侧回退。
func TestApplyWorkDirUsages_UnavailableKeepsOldBytes(t *testing.T) {
	h, db := newWorkDirUsageFixture(t)
	require.NoError(t, db.Model(&model.Instance{}).Where("uuid = ?", "i-workdir").
		Updates(map[string]any{
			"work_dir_bytes":           7 << 30,
			"work_dir_bytes_available": true,
		}).Error)

	h.applyWorkDirUsages([]*workerpb.InstanceWorkDirUsage{{
		InstanceUuid: "i-workdir", Available: false,
	}})

	var got model.Instance
	require.NoError(t, db.Where("uuid = ?", "i-workdir").First(&got).Error)
	require.False(t, got.WorkDirBytesAvailable, "统计失败必须把可用位置 false")
	require.EqualValues(t, 7<<30, got.WorkDirBytes, "不可用不得清空旧读数")
}

// TestApplyWorkDirUsages_MissingInstanceLeavesRowAlone 本拍未带该实例：整行不动。
//
// 老 Worker / 新实例首拍未统计都会走这条路径。写成 false 会把「从未上报」伪装成
// 「上报了但失败」——两者的回退动作一样，但排障含义完全不同。
func TestApplyWorkDirUsages_MissingInstanceLeavesRowAlone(t *testing.T) {
	h, db := newWorkDirUsageFixture(t)
	require.NoError(t, db.Model(&model.Instance{}).Where("uuid = ?", "i-workdir").
		Updates(map[string]any{
			"work_dir_bytes":           9 << 30,
			"work_dir_bytes_available": true,
		}).Error)

	h.applyWorkDirUsages([]*workerpb.InstanceWorkDirUsage{{
		InstanceUuid: "other-instance", WorkDirBytes: 1, Available: true,
	}})

	var got model.Instance
	require.NoError(t, db.Where("uuid = ?", "i-workdir").First(&got).Error)
	require.True(t, got.WorkDirBytesAvailable, "未上报的实例不应被改动")
	require.EqualValues(t, 9<<30, got.WorkDirBytes)
}

// TestApplyWorkDirUsages_NegativeBytesTreatedAsUnavailable 负字节数按不可用处理。
//
// 负值会让组用量比真实值小（低估 → 超卖），必须拦在入库前。
func TestApplyWorkDirUsages_NegativeBytesTreatedAsUnavailable(t *testing.T) {
	h, db := newWorkDirUsageFixture(t)
	require.NoError(t, db.Model(&model.Instance{}).Where("uuid = ?", "i-workdir").
		Updates(map[string]any{
			"work_dir_bytes":           4 << 30,
			"work_dir_bytes_available": true,
		}).Error)

	h.applyWorkDirUsages([]*workerpb.InstanceWorkDirUsage{{
		InstanceUuid: "i-workdir", WorkDirBytes: -1, Available: true,
	}})

	var got model.Instance
	require.NoError(t, db.Where("uuid = ?", "i-workdir").First(&got).Error)
	require.False(t, got.WorkDirBytesAvailable, "负值必须降级为不可用")
	require.EqualValues(t, 4<<30, got.WorkDirBytes, "负值不得覆盖原有正读数")
}

// TestApplyWorkDirUsages_EmptyListNoop 空列表不改任何行（老 Worker 兼容）。
func TestApplyWorkDirUsages_EmptyListNoop(t *testing.T) {
	h, db := newWorkDirUsageFixture(t)
	require.NoError(t, db.Model(&model.Instance{}).Where("uuid = ?", "i-workdir").
		Updates(map[string]any{"work_dir_bytes_available": true, "work_dir_bytes": 3 << 30}).Error)

	h.applyWorkDirUsages(nil)

	var got model.Instance
	require.NoError(t, db.Where("uuid = ?", "i-workdir").First(&got).Error)
	require.True(t, got.WorkDirBytesAvailable)
	require.EqualValues(t, 3<<30, got.WorkDirBytes)
}
