package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func TestInstanceCreateAndReconcilePersistHolderLifecycle(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}, &model.LogTargetHolder{}))
	nodeA := &model.Node{UUID: "worker-a", Name: "a", Host: "127.0.0.1", OS: "linux", Arch: "amd64"}
	nodeB := &model.Node{UUID: "worker-b", Name: "b", Host: "127.0.0.2", OS: "linux", Arch: "amd64"}
	require.NoError(t, db.Create(nodeA).Error)
	require.NoError(t, db.Create(nodeB).Error)
	holders := NewLogTargetHolderService(db)
	instances := NewInstanceService(db, NewGroupService(db), cpgrpc.NewClientPool())
	instances.SetLogTargetHolderService(holders)
	created, err := instances.Create(CreateInstanceRequest{NodeID: nodeA.ID, Name: "holder-test",
		Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDaemon, StartCommand: "echo test"})
	require.NoError(t, err)
	rows, err := holders.ListForInstance(created.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.True(t, rows[0].Current)
	require.Equal(t, nodeA.UUID, rows[0].WorkerUUID)
	require.Contains(t, rows[0].SourceGeneration, created.UUID)
	request, err := instances.buildCreateInstanceRequest(created)
	require.NoError(t, err)
	require.Equal(t, rows[0].SourceGeneration, request.LogSourceGeneration)
	require.Equal(t, rows[0].StorageNamespace, request.LogTargetId)
	require.Equal(t, "STDIO_PRIMARY", request.LogAcquireMode)

	require.NoError(t, db.Model(created).Update("node_id", nodeB.ID).Error)
	created.NodeID = nodeB.ID
	require.NoError(t, holders.ReconcileInstances([]model.Instance{*created}, []model.Node{*nodeA, *nodeB}, time.Now().UTC()))
	rows, err = holders.ListForInstance(created.ID)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.True(t, rows[0].Current)
	require.Equal(t, nodeB.UUID, rows[0].WorkerUUID)
	require.False(t, rows[1].Current)
	require.Equal(t, nodeA.UUID, rows[1].WorkerUUID)
	require.NotNil(t, rows[1].ValidTo)
}
