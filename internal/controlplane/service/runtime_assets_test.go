package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newRuntimeAssetsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.NodeJDK{}, &model.Instance{}, &model.Asset{}))
	// 每个用例独立清表（cache=shared 共享同库）。
	require.NoError(t, db.Exec("DELETE FROM nodes").Error)
	require.NoError(t, db.Exec("DELETE FROM node_jdks").Error)
	require.NoError(t, db.Exec("DELETE FROM instances").Error)
	require.NoError(t, db.Exec("DELETE FROM assets").Error)
	return db
}

// 直接绑定（jdk_id）的实例进入对应 JDK 的引用清单，binding=direct。
func TestBuildJDKMatrix_DirectBinding(t *testing.T) {
	nodes := []model.Node{{ID: 1, Name: "n1", Status: model.NodeStatusOnline}}
	jdks := []model.NodeJDK{{ID: 10, NodeID: 1, Vendor: "Temurin", MajorVersion: 21, Version: "21.0.4"}}
	instances := []model.Instance{
		{ID: 100, Name: "srv-a", NodeID: 1, JDKID: 10, Status: model.InstanceStatusRunning},
	}

	items, summary := buildJDKMatrix(nodes, jdks, instances)
	require.Len(t, items, 1)
	require.Equal(t, "n1", items[0].NodeName)
	require.True(t, items[0].NodeOnline)
	require.Len(t, items[0].Instances, 1)
	require.Equal(t, 1, items[0].RefCount)
	require.Equal(t, "direct", items[0].Instances[0].Binding)
	require.Equal(t, "srv-a", items[0].Instances[0].Name)

	require.Equal(t, 1, summary.NodeCount)
	require.Equal(t, 1, summary.JDKCount)
	require.Equal(t, 1, summary.ReferencedJDK)
	require.Equal(t, 1, summary.InstanceRefs)
}

// 大版本绑定（jdk_id=0 + java_major_version）解析到同节点同大版本中 id 最大的 JDK，binding=major。
func TestBuildJDKMatrix_MajorBindingResolvesHighestID(t *testing.T) {
	nodes := []model.Node{{ID: 1, Name: "n1", Status: model.NodeStatusOnline}}
	jdks := []model.NodeJDK{
		{ID: 10, NodeID: 1, Vendor: "Temurin", MajorVersion: 21, Version: "21.0.1"},
		{ID: 11, NodeID: 1, Vendor: "Temurin", MajorVersion: 21, Version: "21.0.4"}, // 更高 id，应被选中
	}
	instances := []model.Instance{
		{ID: 100, Name: "srv-major", NodeID: 1, JDKID: 0, JavaMajorVersion: 21, Status: model.InstanceStatusStopped},
	}

	items, summary := buildJDKMatrix(nodes, jdks, instances)
	byID := map[uint]JDKMatrixItem{}
	for _, it := range items {
		byID[it.ID] = it
	}
	require.Len(t, byID[11].Instances, 1, "大版本应解析到 id 最大的 JDK(11)")
	require.Equal(t, "major", byID[11].Instances[0].Binding)
	require.Empty(t, byID[10].Instances, "id 较小的同大版本 JDK 不被解析命中")
	require.Equal(t, 1, summary.InstanceRefs)
	require.Equal(t, 1, summary.ReferencedJDK)
}

// 跨节点：同大版本不串台——A 节点实例不会引用 B 节点的 JDK。
func TestBuildJDKMatrix_DoesNotCrossNodes(t *testing.T) {
	nodes := []model.Node{
		{ID: 1, Name: "n1", Status: model.NodeStatusOnline},
		{ID: 2, Name: "n2", Status: model.NodeStatusOffline},
	}
	jdks := []model.NodeJDK{
		{ID: 10, NodeID: 1, MajorVersion: 21},
		{ID: 20, NodeID: 2, MajorVersion: 21},
	}
	instances := []model.Instance{
		{ID: 100, Name: "on-n1", NodeID: 1, JavaMajorVersion: 21},
	}

	items, _ := buildJDKMatrix(nodes, jdks, instances)
	byID := map[uint]JDKMatrixItem{}
	for _, it := range items {
		byID[it.ID] = it
	}
	require.Len(t, byID[10].Instances, 1)
	require.Empty(t, byID[20].Instances, "B 节点 JDK 不应被 A 节点实例引用")
	require.False(t, byID[20].NodeOnline)
}

// 无任何绑定（jdk_id=0 且 java_major_version=0）的实例不产生引用边。
func TestBuildJDKMatrix_NoBindingIgnored(t *testing.T) {
	nodes := []model.Node{{ID: 1, Name: "n1", Status: model.NodeStatusOnline}}
	jdks := []model.NodeJDK{{ID: 10, NodeID: 1, MajorVersion: 21}}
	instances := []model.Instance{{ID: 100, Name: "generic", NodeID: 1}}

	items, summary := buildJDKMatrix(nodes, jdks, instances)
	require.Empty(t, items[0].Instances)
	require.Equal(t, 0, summary.InstanceRefs)
	require.Equal(t, 0, summary.ReferencedJDK)
}

// 制品按类型分组：占用/去重/冷热统计正确，分组按类型名升序。
func TestGroupAssetsByType_StatsAndOrder(t *testing.T) {
	assets := []model.Asset{
		{ID: 3, Type: model.AssetTypePlugin, Size: 100, RefCount: 2, StorageState: model.AssetStorageHot},
		{ID: 2, Type: model.AssetTypeCore, Size: 500, RefCount: 0, StorageState: model.AssetStorageArchived},
		{ID: 1, Type: model.AssetTypeCore, Size: 300, RefCount: 1, StorageState: model.AssetStorageHot},
	}

	groups, summary := groupAssetsByType(assets)
	require.Len(t, groups, 2)
	// 升序：core < plugin
	require.Equal(t, model.AssetTypeCore, groups[0].Type)
	require.Equal(t, model.AssetTypePlugin, groups[1].Type)

	core := groups[0]
	require.Equal(t, 2, core.Count)
	require.EqualValues(t, 800, core.TotalSize)
	require.Equal(t, 1, core.ReferencedCount)
	require.Equal(t, 1, core.HotCount)
	require.Equal(t, 1, core.ArchivedCount)

	require.Equal(t, 3, summary.AssetCount)
	require.EqualValues(t, 900, summary.TotalSize)
	require.Equal(t, 2, summary.ReferencedCount)
	require.Equal(t, 2, summary.HotCount)
	require.Equal(t, 1, summary.ArchivedCount)
}

// 空集：分组与汇总均为零值，不 panic。
func TestGroupAssetsByType_Empty(t *testing.T) {
	groups, summary := groupAssetsByType(nil)
	require.Empty(t, groups)
	require.Equal(t, AssetSummary{}, summary)
}

// 端到端：经真实 DB 加载并聚合，JDK 引用与制品分组均正确。
func TestRuntimeAssetsService_Overview(t *testing.T) {
	db := newRuntimeAssetsTestDB(t)
	svc := NewRuntimeAssetsService(db)

	node := &model.Node{Name: "node-a", Host: "127.0.0.1", GRPCPort: 1, WSPort: 2, Secret: "s", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	jdk := &model.NodeJDK{NodeID: node.ID, Vendor: "Temurin", MajorVersion: 21, Version: "21.0.4", Arch: "x64", Path: "/opt/jdk21"}
	require.NoError(t, db.Create(jdk).Error)
	inst := &model.Instance{NodeID: node.ID, Name: "paper-1", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDaemon, JDKID: jdk.ID, Status: model.InstanceStatusRunning}
	require.NoError(t, db.Create(inst).Error)
	asset := &model.Asset{Type: model.AssetTypeCore, Name: "paper", SHA256: "a", Size: 1234, StorageState: model.AssetStorageHot}
	require.NoError(t, db.Create(asset).Error)

	ov, err := svc.Overview()
	require.NoError(t, err)
	require.Len(t, ov.JDKs, 1)
	require.Equal(t, "node-a", ov.JDKs[0].NodeName)
	require.Len(t, ov.JDKs[0].Instances, 1)
	require.Equal(t, "paper-1", ov.JDKs[0].Instances[0].Name)
	require.Equal(t, 1, ov.JDKSummary.InstanceRefs)

	require.Len(t, ov.Assets, 1)
	require.Equal(t, model.AssetTypeCore, ov.Assets[0].Type)
	require.EqualValues(t, 1234, ov.Assets[0].TotalSize)
	require.Equal(t, 1, ov.AssetSummary.AssetCount)
}
