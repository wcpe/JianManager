package service

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

func newFR301DB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:fr301_" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.NodeJDK{}, &model.NodeRuntime{}, &model.Instance{}, &model.Asset{}))
	return db
}

// fakeFR301JDKWorker 假 Worker：ListJDKs 回预置清单（Refresh 强制同步路径）。
type fakeFR301JDKWorker struct {
	workerpb.WorkerServiceClient
	jdks []*workerpb.JDKInfo
}

func (f *fakeFR301JDKWorker) ListJDKs(context.Context, *workerpb.ListJDKsRequest, ...grpc.CallOption) (*workerpb.ListJDKsResponse, error) {
	return &workerpb.ListJDKsResponse{Jdks: f.jdks}, nil
}

// 多类型拼装：jdk 行保留引用实例、node_runtimes 行引用恒空（无消费者，不臆造）。
func TestBuildRuntimeMatrix_MultiType(t *testing.T) {
	nodes := []model.Node{
		{ID: 1, Name: "n1", Status: model.NodeStatusOnline},
		{ID: 2, Name: "n2", Status: model.NodeStatusOffline},
	}
	jdks := []model.NodeJDK{{ID: 10, NodeID: 1, Vendor: "Temurin", MajorVersion: 21, Version: "21.0.4", Arch: "x64", Path: "/opt/jdk21", Managed: true}}
	instances := []model.Instance{{ID: 100, Name: "srv-a", NodeID: 1, JDKID: 10, Status: model.InstanceStatusRunning}}
	jdkItems, _ := buildJDKMatrix(nodes, jdks, instances)

	runtimes := []model.NodeRuntime{
		{ID: 5, NodeID: 2, Type: "nodejs", Name: "Node.js 22", Version: "22.17.0", Major: 22, Arch: "x64", Path: "/usr/local/bin/node"},
	}

	items := buildRuntimeMatrix(nodes, jdkItems, runtimes)
	require.Len(t, items, 2)

	jdkRow := items[0]
	require.Equal(t, "jdk", jdkRow.Type)
	require.Equal(t, "Temurin", jdkRow.Name)
	require.Equal(t, 21, jdkRow.MajorVersion)
	require.Equal(t, "n1", jdkRow.NodeName)
	require.True(t, jdkRow.NodeOnline)
	require.Len(t, jdkRow.Instances, 1, "jdk 行应携带既有引用实例")
	require.Equal(t, 1, jdkRow.RefCount)

	nodeRow := items[1]
	require.Equal(t, "nodejs", nodeRow.Type)
	require.Equal(t, "Node.js 22", nodeRow.Name)
	require.Equal(t, 22, nodeRow.MajorVersion)
	require.Equal(t, "n2", nodeRow.NodeName)
	require.False(t, nodeRow.NodeOnline)
	require.Empty(t, nodeRow.Instances, "非 JDK 类型无引用消费者，引用恒空")
	require.Equal(t, 0, nodeRow.RefCount)
}

// Overview 加性扩展：runtimes 多类型矩阵 + 每节点/整体 syncedAt；老字段（JDKs 等）不受影响。
func TestRuntimeAssetsService_Overview_RuntimesAndSyncedAt(t *testing.T) {
	db := newFR301DB(t)
	svc := NewRuntimeAssetsService(db)

	synced := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	node := &model.Node{Name: "node-a", Host: "127.0.0.1", GRPCPort: 1, WSPort: 2, Secret: "s", Status: model.NodeStatusOnline, RuntimeSyncedAt: &synced}
	require.NoError(t, db.Create(node).Error)
	jdk := &model.NodeJDK{NodeID: node.ID, Vendor: "Temurin", MajorVersion: 21, Version: "21.0.4", Arch: "x64", Path: "/opt/jdk21"}
	require.NoError(t, db.Create(jdk).Error)
	rt := &model.NodeRuntime{NodeID: node.ID, Type: "nodejs", Name: "Node.js 22", Version: "22.17.0", Major: 22, Arch: "x64", Path: "/usr/local/bin/node"}
	require.NoError(t, db.Create(rt).Error)
	inst := &model.Instance{NodeID: node.ID, Name: "paper-1", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDaemon, JDKID: jdk.ID, Status: model.InstanceStatusRunning}
	require.NoError(t, db.Create(inst).Error)

	ov, err := svc.Overview()
	require.NoError(t, err)

	// 老字段不变。
	require.Len(t, ov.JDKs, 1)
	require.Equal(t, 1, ov.JDKSummary.InstanceRefs)

	// 新增 runtimes：jdk + nodejs 两行。
	require.Len(t, ov.Runtimes, 2)
	byType := map[string]RuntimeMatrixItem{}
	for _, r := range ov.Runtimes {
		byType[r.Type] = r
	}
	require.Equal(t, "Temurin", byType["jdk"].Name)
	require.Equal(t, 1, byType["jdk"].RefCount)
	require.Equal(t, "Node.js 22", byType["nodejs"].Name)
	require.Equal(t, 0, byType["nodejs"].RefCount)

	// 每节点同步状态 + 整体 syncedAt = 各节点最大值。
	require.Len(t, ov.RuntimeSyncs, 1)
	require.Equal(t, node.ID, ov.RuntimeSyncs[0].NodeID)
	require.NotNil(t, ov.RuntimeSyncs[0].SyncedAt)
	require.WithinDuration(t, synced, *ov.RuntimeSyncs[0].SyncedAt, time.Second)
	require.NotNil(t, ov.SyncedAt)
	require.WithinDuration(t, synced, *ov.SyncedAt, time.Second)
}

// 从未同步的节点 syncedAt 为 nil；无任何同步时整体 SyncedAt 为 nil。
func TestRuntimeAssetsService_Overview_NeverSynced(t *testing.T) {
	db := newFR301DB(t)
	svc := NewRuntimeAssetsService(db)
	node := &model.Node{Name: "node-never", Host: "127.0.0.1", GRPCPort: 1, WSPort: 2, Secret: "s"}
	require.NoError(t, db.Create(node).Error)

	ov, err := svc.Overview()
	require.NoError(t, err)
	require.Len(t, ov.RuntimeSyncs, 1)
	require.Nil(t, ov.RuntimeSyncs[0].SyncedAt)
	require.Nil(t, ov.SyncedAt)
}

// Refresh 失败容忍：在线节点同步成功（清单入库 + syncedAt 前移），离线节点 ok=false
// 且旧数据原样保留（显旧数据语义），整体不返回 error。
func TestRuntimeAssetsService_Refresh_ToleratesFailure(t *testing.T) {
	db := newFR301DB(t)
	pool := cpgrpc.NewClientPool()
	jdkSvc := NewJDKService(db, pool)
	svc := NewRuntimeAssetsService(db)
	svc.SetJDKSync(jdkSvc)

	online := &model.Node{UUID: "u-fr301-ok", Name: "n-ok", Host: "127.0.0.1", GRPCPort: 1, WSPort: 2, Secret: "s", Status: model.NodeStatusOnline}
	offline := &model.Node{UUID: "u-fr301-off", Name: "n-off", Host: "127.0.0.1", GRPCPort: 1, WSPort: 2, Secret: "s", Status: model.NodeStatusOffline}
	require.NoError(t, db.Create(online).Error)
	require.NoError(t, db.Create(offline).Error)
	pool.SetWorkerClientForTest(online.UUID, &fakeFR301JDKWorker{jdks: []*workerpb.JDKInfo{
		{Vendor: "Temurin", MajorVersion: 21, Version: "21.0.4", Arch: "x64", Path: "/opt/jdk21", Managed: true},
	}})
	// 离线节点预置旧库存：刷新失败后必须仍在（容忍显旧）。
	require.NoError(t, db.Create(&model.NodeJDK{NodeID: offline.ID, Vendor: "Zulu", MajorVersion: 17, Version: "17.0.11", Arch: "x64", Path: "/opt/jdk17"}).Error)

	out, err := svc.Refresh()
	require.NoError(t, err, "单节点失败不应让整体报错")
	require.Len(t, out.Results, 2)

	byNode := map[uint]RuntimeRefreshResult{}
	for _, r := range out.Results {
		byNode[r.NodeID] = r
	}
	okRes := byNode[online.ID]
	require.True(t, okRes.OK)
	require.Empty(t, okRes.Error)
	require.NotNil(t, okRes.SyncedAt, "成功节点应带最新同步时间")

	failRes := byNode[offline.ID]
	require.False(t, failRes.OK)
	require.Contains(t, failRes.Error, "节点未连接")
	require.Nil(t, failRes.SyncedAt, "从未同步过的失败节点无时间戳")

	// 在线节点：Worker 清单已入库。
	var onlineCount int64
	require.NoError(t, db.Model(&model.NodeJDK{}).Where("node_id = ?", online.ID).Count(&onlineCount).Error)
	require.Equal(t, int64(1), onlineCount)
	// 离线节点：旧数据原样保留。
	var offlineCount int64
	require.NoError(t, db.Model(&model.NodeJDK{}).Where("node_id = ?", offline.ID).Count(&offlineCount).Error)
	require.Equal(t, int64(1), offlineCount)

	// 整体 syncedAt 取成功节点时间。
	require.NotNil(t, out.SyncedAt)
	require.Equal(t, *okRes.SyncedAt, *out.SyncedAt)
}

// 未装配同步器（main 未 SetJDKSync）时 Refresh 直接报错，不静默吞。
func TestRuntimeAssetsService_Refresh_NoSyncer(t *testing.T) {
	db := newFR301DB(t)
	svc := NewRuntimeAssetsService(db)
	_, err := svc.Refresh()
	require.Error(t, err)
}
