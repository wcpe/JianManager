package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// newRepairTestDB 为坏节点修复服务测试准备独立内存库（按测试名隔离，避免 cache=shared 跨测污染）。
func newRepairTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.NodeJDK{}, &model.Instance{}))
	return db
}

// TestListSuspects_FlagsDedupRenamedAndDuplicates 检测应标出带去重后缀的节点与仍存在的同名活跃节点（ADR-039 §2）。
func TestListSuspects_FlagsDedupRenamedAndDuplicates(t *testing.T) {
	db := newRepairTestDB(t)
	svc := NewNodeRepairService(db)

	// 正常节点：不应被标。
	require.NoError(t, db.Create(&model.Node{Name: "edge-ok", Host: "10.0.0.1", Secret: "s1"}).Error)
	// 迁移去重改名的节点：应被标。
	require.NoError(t, db.Create(&model.Node{Name: "edge-x-dup-7", Host: "10.0.0.2", Secret: "s2"}).Error)
	// 仍存在的同名活跃组：两个都应被标。
	require.NoError(t, db.Create(&model.Node{Name: "edge-dupe", Host: "10.0.0.3", Secret: "s3"}).Error)
	require.NoError(t, db.Create(&model.Node{Name: "edge-dupe", Host: "10.0.0.4", Secret: "s4"}).Error)

	suspects, err := svc.ListSuspects()
	require.NoError(t, err)

	flagged := map[string]bool{}
	for _, s := range suspects {
		flagged[s.Node.Host] = true
	}
	require.False(t, flagged["10.0.0.1"], "正常节点不应被标")
	require.True(t, flagged["10.0.0.2"], "去重改名节点应被标")
	require.True(t, flagged["10.0.0.3"], "同名活跃节点应被标")
	require.True(t, flagged["10.0.0.4"], "同名活跃节点应被标")
}

// TestReenroll_RotatesIdentity 重新 enroll 应轮换 UUID + secret，旧 secret 失效（ADR-039 §2）。
func TestReenroll_RotatesIdentity(t *testing.T) {
	db := newRepairTestDB(t)
	svc := NewNodeRepairService(db)
	node := &model.Node{Name: "edge-a", Host: "10.0.0.1", Secret: "old-secret", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	oldUUID := node.UUID

	res, err := svc.Reenroll(node.ID, true)
	require.NoError(t, err)
	require.NotEqual(t, oldUUID, res.NewUUID, "应轮换出新 UUID")
	require.NotEmpty(t, res.NewSecret)
	require.Equal(t, oldUUID, res.OldUUID)

	var fromDB model.Node
	require.NoError(t, db.First(&fromDB, node.ID).Error)
	require.Equal(t, res.NewUUID, fromDB.UUID)
	require.NotEqual(t, "old-secret", fromDB.Secret, "旧 secret 应失效")
	require.Equal(t, model.NodeStatusOffline, fromDB.Status, "轮换后应置离线待重注册")
}

// TestReenroll_RequiresConfirm 未二次确认拒绝（ADR-039 §2，破坏性操作）。
func TestReenroll_RequiresConfirm(t *testing.T) {
	db := newRepairTestDB(t)
	svc := NewNodeRepairService(db)
	node := &model.Node{Name: "edge-a", Host: "10.0.0.1", Secret: "old", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)

	_, err := svc.Reenroll(node.ID, false)
	require.ErrorIs(t, err, ErrRepairNotConfirmed)

	// 身份未变。
	var fromDB model.Node
	require.NoError(t, db.First(&fromDB, node.ID).Error)
	require.Equal(t, "old", fromDB.Secret)
}

// TestReenroll_NodeNotFound 节点不存在返回 ErrNodeNotFound。
func TestReenroll_NodeNotFound(t *testing.T) {
	db := newRepairTestDB(t)
	svc := NewNodeRepairService(db)
	_, err := svc.Reenroll(999, true)
	require.ErrorIs(t, err, ErrNodeNotFound)
}

// TestOrphanReport_Counts 统计节点上 JDK 与实例数量（只读）。
func TestOrphanReport_Counts(t *testing.T) {
	db := newRepairTestDB(t)
	svc := NewNodeRepairService(db)
	node := &model.Node{Name: "edge-a", Host: "10.0.0.1", Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	require.NoError(t, db.Create(&model.NodeJDK{NodeID: node.ID, Vendor: "temurin", MajorVersion: 21, Version: "21", Arch: "amd64", Path: "/jdk"}).Error)
	require.NoError(t, db.Create(&model.Instance{Name: "i1", NodeID: node.ID}).Error)
	require.NoError(t, db.Create(&model.Instance{Name: "i2", NodeID: node.ID}).Error)

	rep, err := svc.OrphanReport(node.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), rep.JDKCount)
	require.Equal(t, int64(2), rep.InstanceCount)
}

// TestPurgeOrphans_DeletesJDKAndInstances 清理孤儿：删 JDK、软删实例（ADR-039 §2）。
func TestPurgeOrphans_DeletesJDKAndInstances(t *testing.T) {
	db := newRepairTestDB(t)
	svc := NewNodeRepairService(db)
	node := &model.Node{Name: "edge-a", Host: "10.0.0.1", Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	require.NoError(t, db.Create(&model.NodeJDK{NodeID: node.ID, Vendor: "temurin", MajorVersion: 21, Version: "21", Arch: "amd64", Path: "/jdk"}).Error)
	require.NoError(t, db.Create(&model.Instance{Name: "i1", NodeID: node.ID}).Error)

	res, err := svc.PurgeOrphans(node.ID, true)
	require.NoError(t, err)
	require.Equal(t, int64(1), res.JDKDeleted)
	require.Equal(t, int64(1), res.InstancesPurged)

	var jdkCnt, instCnt int64
	db.Model(&model.NodeJDK{}).Where("node_id = ?", node.ID).Count(&jdkCnt)
	db.Model(&model.Instance{}).Where("node_id = ?", node.ID).Count(&instCnt)
	require.Equal(t, int64(0), jdkCnt, "JDK 应被删")
	require.Equal(t, int64(0), instCnt, "实例应被（软）删")
}

// TestPurgeOrphans_RequiresConfirm 未二次确认拒绝，且不删任何数据。
func TestPurgeOrphans_RequiresConfirm(t *testing.T) {
	db := newRepairTestDB(t)
	svc := NewNodeRepairService(db)
	node := &model.Node{Name: "edge-a", Host: "10.0.0.1", Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	require.NoError(t, db.Create(&model.NodeJDK{NodeID: node.ID, Vendor: "temurin", MajorVersion: 21, Version: "21", Arch: "amd64", Path: "/jdk"}).Error)

	_, err := svc.PurgeOrphans(node.ID, false)
	require.ErrorIs(t, err, ErrRepairNotConfirmed)

	var jdkCnt int64
	db.Model(&model.NodeJDK{}).Where("node_id = ?", node.ID).Count(&jdkCnt)
	require.Equal(t, int64(1), jdkCnt, "未确认不得删数据")
}
