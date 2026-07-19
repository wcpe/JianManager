package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

func TestAssembleBotLoadServices_CreatesSharedProcessServices(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	bundle, err := assembleBotLoadServices(db, cpgrpc.NewClientPool(), "stable-server-secret")
	require.NoError(t, err)
	t.Cleanup(bundle.subscriptions.Close)
	require.NotNil(t, bundle.capacity)
	require.NotNil(t, bundle.reservations)
	require.NotNil(t, bundle.signer)
	require.NotNil(t, bundle.preflight)
	require.NotNil(t, bundle.execution)
	require.NotNil(t, bundle.actionResults)
	require.NotNil(t, bundle.coordinator)
	require.NotNil(t, bundle.subscriptions)
	require.Same(t, bundle.execution, bundle.coordinator.SnapshotReconciler())
	require.Same(t, bundle.execution, bundle.coordinator.RuntimeObserver())
	require.Same(t, bundle.actionResults, bundle.coordinator.ActionEventHandler())
	require.Same(t, bundle.coordinator, bundle.subscriptions.RuntimeCoordinator())
	require.Same(t, bundle.subscriptions, bundle.execution.FleetSubscriptionManager())
}

func TestAssembleBotLoadServices_RejectsMissingStableSecret(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	_, err = assembleBotLoadServices(db, cpgrpc.NewClientPool(), "")
	require.Error(t, err)
}

type mainTestFleetSubscriptions struct {
	targets []service.BotFleetSubscriptionTarget
}

func (s *mainTestFleetSubscriptions) Ensure(target service.BotFleetSubscriptionTarget) {
	s.targets = append(s.targets, target)
}

func (s *mainTestFleetSubscriptions) Restore(targets []service.BotFleetSubscriptionTarget) {
	for _, target := range targets {
		s.Ensure(target)
	}
}

func (*mainTestFleetSubscriptions) StopSession(string) {}

func TestRecoverConnectedBotFleetSubscriptions_FiltersStartupAndReconnectNodes(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}, &model.BotStressSession{}, &model.BotLoadBatch{}))
	nodeA := &model.Node{Name: "node-a", Host: "127.0.0.1", Secret: "secret"}
	nodeB := &model.Node{Name: "node-b", Host: "127.0.0.2", Secret: "secret"}
	require.NoError(t, db.Create(nodeA).Error)
	require.NoError(t, db.Create(nodeB).Error)
	instance := &model.Instance{NodeID: nodeA.ID, Name: "target", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDirect, WorkDir: "var/servers/target", StartCommand: "java"}
	require.NoError(t, db.Create(instance).Error)
	session := &model.BotStressSession{InstanceID: instance.ID, Name: "fleet", NamePrefix: "fleet", BotCount: 2, Status: model.BotStressSessionRunning}
	require.NoError(t, db.Create(session).Error)
	batches := []model.BotLoadBatch{
		{StressSessionID: session.ID, ExecutorNodeID: nodeA.ID, Ordinal: 1, PlannedCount: 1, State: model.BotLoadBatchRunning, IdempotencyKey: "main-recover-a", ConnectStartAt: time.Now().UTC()},
		{StressSessionID: session.ID, ExecutorNodeID: nodeB.ID, Ordinal: 2, PlannedCount: 1, State: model.BotLoadBatchRunning, IdempotencyKey: "main-recover-b", ConnectStartAt: time.Now().UTC()},
	}
	require.NoError(t, db.Create(&batches).Error)

	pool := cpgrpc.NewClientPool()
	pool.SetWorkerClientForTest(nodeA.UUID, nil)
	subscriptions := &mainTestFleetSubscriptions{}
	execution := service.NewBotLoadExecutionService(db, nil, nil, nil, nil, nil, nil)
	execution.SetFleetSubscriptionManager(subscriptions)

	require.NoError(t, recoverConnectedBotFleetSubscriptions(context.Background(), execution, pool))
	require.Len(t, subscriptions.targets, 1)
	require.Equal(t, nodeA.UUID, subscriptions.targets[0].NodeUUID)
	require.NoError(t, recoverConnectedBotFleetSubscriptions(context.Background(), execution, pool, nodeB.UUID))
	require.Len(t, subscriptions.targets, 1)

	pool.SetWorkerClientForTest(nodeB.UUID, nil)
	require.NoError(t, recoverConnectedBotFleetSubscriptions(context.Background(), execution, pool, nodeB.UUID))
	require.Len(t, subscriptions.targets, 2)
	require.Equal(t, nodeB.UUID, subscriptions.targets[1].NodeUUID)
}

func TestRunControlPlaneServer_ClosesSubscriptionsBeforeReturningError(t *testing.T) {
	wantErr := errors.New("listen failed")
	closed := false

	err := runControlPlaneServer(func() error { return wantErr }, func() { closed = true })

	require.ErrorIs(t, err, wantErr)
	require.True(t, closed)
}
