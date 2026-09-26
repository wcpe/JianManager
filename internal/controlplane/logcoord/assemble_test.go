package logcoord

import (
	"context"
	"strconv"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

type fakeConnected struct{ ids []string }

func (f fakeConnected) ConnectedNodes() []string { return f.ids }

// P1：inst:<id> 必须解析到所属 Worker UUID。
func TestProductionTargetResolver_InstanceToWorker(t *testing.T) {
	db := setupAssembleDB(t)
	node := model.Node{UUID: "worker-uuid-1", Name: "n1", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(&node).Error)
	inst := model.Instance{
		UUID: "inst-uuid-1", Name: "srv", NodeID: node.ID,
		Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, StartCommand: "true",
	}
	require.NoError(t, db.Create(&inst).Error)

	nodeSvc := service.NewNodeService(db)
	instSvc := service.NewInstanceService(db, service.NewGroupService(db), cpgrpc.NewClientPool())
	r := NewProductionTargetResolver(nodeSvc, instSvc, fakeConnected{ids: []string{"worker-uuid-1"}}, nil)

	targets, err := r.Resolve(context.Background(), []string{"inst:" + itoa(inst.ID)})
	require.NoError(t, err)
	require.Len(t, targets, 1)
	assert.Equal(t, "inst:"+itoa(inst.ID), targets[0].ID)
	assert.Equal(t, "worker-uuid-1", targets[0].WorkerID)
	assert.Equal(t, ReadyOnline, targets[0].Readiness)
}

// P1：无活跃隧道/节点离线不得标 ReadyOnline。
func TestProductionTargetResolver_OfflineNotReadyOnline(t *testing.T) {
	db := setupAssembleDB(t)
	on := model.Node{UUID: "w-on", Name: "on", Status: model.NodeStatusOnline}
	off := model.Node{UUID: "w-off", Name: "off", Status: model.NodeStatusOffline}
	// 在线但无隧道
	nolink := model.Node{UUID: "w-nolink", Name: "nolink", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(&on).Error)
	require.NoError(t, db.Create(&off).Error)
	require.NoError(t, db.Create(&nolink).Error)

	r := NewProductionTargetResolver(service.NewNodeService(db), nil, fakeConnected{ids: []string{"w-on"}}, nil)
	targets, err := r.Resolve(context.Background(), []string{
		"node:" + itoa(on.ID),
		"node:" + itoa(off.ID),
		"node:" + itoa(nolink.ID),
	})
	require.NoError(t, err)
	by := map[string]Readiness{}
	for _, tg := range targets {
		by[tg.ID] = tg.Readiness
	}
	assert.Equal(t, ReadyOnline, by["node:"+itoa(on.ID)])
	assert.Equal(t, ReadyOffline, by["node:"+itoa(off.ID)])
	assert.Equal(t, ReadyNotReady, by["node:"+itoa(nolink.ID)], "online heartbeat without tunnel must not be ReadyOnline")
}

func TestProductionTargetResolver_AllIncludesNodeWithoutInstances(t *testing.T) {
	db := setupAssembleDB(t)
	n := model.Node{UUID: "worker-node-only", Name: "node-only", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(&n).Error)
	r := NewProductionTargetResolver(service.NewNodeService(db), service.NewInstanceService(db, nil, nil), fakeConnected{ids: []string{"worker-node-only"}}, nil)
	targets, err := r.Resolve(context.Background(), []string{"*"})
	require.NoError(t, err)
	require.Len(t, targets, 1)
	assert.Equal(t, "node:"+itoa(n.ID), targets[0].ID)
}

func setupAssembleDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}))
	return db
}

func itoa(v uint) string {
	return strconv.FormatUint(uint64(v), 10)
}
