package service

import (
	"context"
	"errors"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

type captureInstanceUpdateWorker struct {
	workerpb.WorkerServiceClient
	requests  []*workerpb.CreateInstanceRequest
	events    []string
	createErr error
}

func (f *captureInstanceUpdateWorker) CreateInstance(_ context.Context, req *workerpb.CreateInstanceRequest, _ ...grpc.CallOption) (*workerpb.CreateInstanceResponse, error) {
	f.requests = append(f.requests, req)
	f.events = append(f.events, "create")
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &workerpb.CreateInstanceResponse{Success: true}, nil
}

func (f *captureInstanceUpdateWorker) RestartInstance(_ context.Context, _ *workerpb.InstanceActionRequest, _ ...grpc.CallOption) (*workerpb.InstanceActionResponse, error) {
	f.events = append(f.events, "restart")
	return &workerpb.InstanceActionResponse{Success: true}, nil
}

func newInstanceUpdateSyncDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}, &model.NodeJDK{}))
	return db
}

// TestInstanceUpdate_SyncsLaunchSpecToOnlineWorker 验证 FR-233：保存启动命令、JDK、环境变量、
// autoRestart 后，CP 必须把数据库中的最新完整规格幂等同步给当前在线 Worker。
func TestInstanceUpdate_SyncsLaunchSpecToOnlineWorker(t *testing.T) {
	db := newInstanceUpdateSyncDB(t)
	node := &model.Node{UUID: "node-fr233-update", Name: "fr233-node", Host: "127.0.0.1", Status: model.NodeStatusOnline, OS: "linux"}
	require.NoError(t, db.Create(node).Error)
	jdk := &model.NodeJDK{NodeID: node.ID, Vendor: "Temurin", MajorVersion: 21, Version: "21.0.4", Arch: "amd64", Path: "/opt/jdk-21"}
	require.NoError(t, db.Create(jdk).Error)
	inst := &model.Instance{
		NodeID: node.ID, Name: "fr233", Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDaemon, StartCommand: "old-command", WorkDir: "var/servers/fr233",
		Status: model.InstanceStatusRunning,
	}
	require.NoError(t, db.Create(inst).Error)

	worker := &captureInstanceUpdateWorker{}
	pool := cpgrpc.NewClientPool()
	pool.SetWorkerClientForTest(node.UUID, worker)
	svc := NewInstanceService(db, NewGroupService(db), pool)
	t.Cleanup(svc.Shutdown)

	command := "  java   -jar   new.jar  "
	autoRestart := true
	env := map[string]string{"JAVA_TOOL_OPTIONS": "-Xmx2G"}
	updated, err := svc.Update(inst.ID, UpdateInstanceFields{
		StartCommand: &command,
		JDKID:        &jdk.ID,
		EnvVars:      &env,
		AutoRestart:  &autoRestart,
	})
	require.NoError(t, err)
	require.Len(t, worker.requests, 1)

	req := worker.requests[0]
	require.Equal(t, updated.StartCommand, req.StartCommand)
	require.Equal(t, "/opt/jdk-21", req.JdkPath)
	require.Equal(t, env, req.EnvVars)
	require.True(t, req.AutoRestart)

	worker.events = nil
	svc.delegateToWorker(updated, "restart")
	require.Equal(t, []string{"create", "restart"}, worker.events, "重启 RPC 前必须先重注册最新规格")
}

// TestDelegateRestart_StopsWhenLaunchSpecRegistrationFails 验证最新规格注册失败时必须取消重启，
// 避免 Worker 继续用缓存旧规格拉起实例，并把明确中文原因写入状态。
func TestDelegateRestart_StopsWhenLaunchSpecRegistrationFails(t *testing.T) {
	db := newInstanceUpdateSyncDB(t)
	node := &model.Node{UUID: "node-fr233-restart-fail", Name: "fr233-fail", Host: "127.0.0.1", Status: model.NodeStatusOnline, OS: "linux"}
	require.NoError(t, db.Create(node).Error)
	inst := &model.Instance{NodeID: node.ID, Name: "fr233-fail", Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDaemon, StartCommand: "new-command", WorkDir: "var/servers/fr233-fail",
		Status: model.InstanceStatusStopping}
	require.NoError(t, db.Create(inst).Error)

	worker := &captureInstanceUpdateWorker{createErr: errors.New("模拟规格同步失败")}
	pool := cpgrpc.NewClientPool()
	pool.SetWorkerClientForTest(node.UUID, worker)
	svc := NewInstanceService(db, NewGroupService(db), pool)
	t.Cleanup(svc.Shutdown)

	svc.delegateToWorker(inst, "restart")
	require.Equal(t, []string{"create"}, worker.events, "注册失败后不得调用 RestartInstance")

	got, err := svc.GetByID(inst.ID)
	require.NoError(t, err)
	require.Equal(t, model.InstanceStatusCrashed, got.Status)
	require.Contains(t, got.StatusReason, "重启前同步最新启动规格失败")
}
