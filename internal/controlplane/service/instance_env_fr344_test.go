package service

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

type instanceEnvWorker struct {
	workerpb.WorkerServiceClient
	calls int
	resp  *workerpb.GetInstanceEnvResponse
}

func (w *instanceEnvWorker) GetInstanceEnv(_ context.Context, _ *workerpb.GetInstanceEnvRequest, _ ...grpc.CallOption) (*workerpb.GetInstanceEnvResponse, error) {
	w.calls++
	return w.resp, nil
}

func newInstanceEnvTestService(t *testing.T, status model.NodeStatus, worker *instanceEnvWorker) (*InstanceService, *model.Instance) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}))

	node := &model.Node{UUID: "env-node", Name: "env-node", Host: "127.0.0.1", Secret: "secret", Status: status}
	require.NoError(t, db.Create(node).Error)
	instance := &model.Instance{
		NodeID: node.ID, Name: "env-instance", Type: model.InstanceTypeMinecraftJava,
		Role: model.InstanceRoleBackend, ProcessType: model.ProcessTypeDaemon,
		Status: model.InstanceStatusRunning, StartCommand: "java -jar server.jar",
		EnvVars: `{"FOO":"configured","ONLY_CONFIGURED":"yes"}`,
	}
	require.NoError(t, db.Create(instance).Error)

	pool := cpgrpc.NewClientPool()
	if worker != nil {
		pool.SetWorkerClientForTest(node.UUID, worker)
	}
	svc := NewInstanceService(db, nil, pool)
	t.Cleanup(svc.Shutdown)
	return svc, instance
}

func TestGetInstanceEnv_ReturnsConfiguredAndRuntimeZones(t *testing.T) {
	worker := &instanceEnvWorker{resp: &workerpb.GetInstanceEnvResponse{
		Available: true,
		Env: map[string]string{
			"FOO":       "runtime",
			"JAVA_HOME": "/opt/jdk-21",
			"PATH":      "/opt/jdk-21/bin:/usr/bin",
		},
	}}
	svc, instance := newInstanceEnvTestService(t, model.NodeStatusOnline, worker)

	got, err := svc.GetInstanceEnv(instance.ID)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"FOO": "configured", "ONLY_CONFIGURED": "yes"}, got.Configured)
	require.True(t, got.RuntimeAvailable)
	require.Equal(t, "runtime", got.Runtime["FOO"])
	require.Equal(t, "/opt/jdk-21", got.Runtime["JAVA_HOME"])
	require.Contains(t, got.Runtime["PATH"], "/opt/jdk-21/bin")
	require.Equal(t, 1, worker.calls)
}

func TestGetInstanceEnv_DegradesWhenNodeDisconnected(t *testing.T) {
	svc, instance := newInstanceEnvTestService(t, model.NodeStatusOnline, nil)

	got, err := svc.GetInstanceEnv(instance.ID)
	require.NoError(t, err)
	require.Equal(t, "configured", got.Configured["FOO"])
	require.False(t, got.RuntimeAvailable)
	require.Empty(t, got.Runtime)
	require.Contains(t, got.Note, "节点未连接")
}

func TestGetInstanceEnv_DegradesWhenNodeMarkedOffline(t *testing.T) {
	worker := &instanceEnvWorker{resp: &workerpb.GetInstanceEnvResponse{
		Available: true,
		Env:       map[string]string{"FOO": "stale-runtime"},
	}}
	svc, instance := newInstanceEnvTestService(t, model.NodeStatusOffline, worker)

	got, err := svc.GetInstanceEnv(instance.ID)
	require.NoError(t, err)
	require.Equal(t, "configured", got.Configured["FOO"], "节点离线时 configured 上区仍须可用")
	require.False(t, got.RuntimeAvailable)
	require.Empty(t, got.Runtime)
	require.Contains(t, got.Note, "节点离线")
	require.Zero(t, worker.calls, "节点已标记离线时不得调用连接池中的陈旧 Worker client")
}
