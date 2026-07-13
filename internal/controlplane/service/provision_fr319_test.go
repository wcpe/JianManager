package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// failingDownloadWorker 下载核心必失败的 worker 替身（FR-319 失败路径）。
type failingDownloadWorker struct {
	fakeProvisionWorker
}

func (f *failingDownloadWorker) DownloadCore(_ context.Context, in *workerpb.DownloadCoreRequest, _ ...grpc.CallOption) (*workerpb.DownloadCoreResponse, error) {
	return &workerpb.DownloadCoreResponse{Success: false, Error: "下载超时：源站 200KB/s 挂了"}, nil
}

// newFR319Harness 建 provision 异步测试基座：spongevanilla 假源（免 paper API）+ 任务表。
func newFR319Harness(t *testing.T, worker workerpb.WorkerServiceClient) (*ProvisionService, *TaskService, *model.Node, func()) {
	t.Helper()
	db := newInstanceTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Task{}, &model.TaskLog{}, &model.Notification{}))
	node := &model.Node{UUID: "node-fr319-" + t.Name(), Status: model.NodeStatusOnline, OS: "linux"}
	require.NoError(t, db.Create(node).Error)

	coreRepo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/spongevanilla/maven-metadata.xml" {
			_, _ = w.Write([]byte(`<metadata><versioning><versions><version>1.21.1-12.0.4-RC2665</version></versions></versioning></metadata>`))
			return
		}
		http.NotFound(w, r)
	}))

	pool := cpgrpc.NewClientPool()
	pool.SetWorkerClientForTest(node.UUID, worker)
	instSvc := NewInstanceService(db, NewGroupService(db), pool)
	coreSvc := &CoreService{client: coreRepo.Client(), base: coreRepo.URL, spongeBase: coreRepo.URL}
	svc := NewProvisionService(db, pool, instSvc, coreSvc, nil)
	taskSvc := NewTaskService(db)
	svc.SetTaskService(taskSvc)
	return svc, taskSvc, node, coreRepo.Close
}

func waitTaskTerminal(t *testing.T, taskSvc *TaskService, taskID string) model.Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		task, _, err := taskSvc.Get(nil, taskID)
		require.NoError(t, err)
		if task.State.IsTerminal() {
			return *task
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("任务未在期限内进入终态")
	return model.Task{}
}

// TestProvisionServerAsync_SucceedsViaTask 异步搭建：立即返回实例+任务，后台完成任务终态 succeeded（FR-319）。
func TestProvisionServerAsync_SucceedsViaTask(t *testing.T) {
	svc, taskSvc, node, done := newFR319Harness(t, &fakeProvisionWorker{})
	defer done()

	inst, taskID, err := svc.ProvisionServerAsync(context.Background(), ProvisionServerRequest{
		NodeID: node.ID, Name: "async-ok", CoreType: "spongevanilla", MCVersion: "1.21.1", MemoryMb: 1024,
	}, 1)
	require.NoError(t, err)
	require.NotNil(t, inst)
	require.NotEmpty(t, taskID, "异步模式必须返回 taskId")

	task := waitTaskTerminal(t, taskSvc, taskID)
	require.Equal(t, model.TaskStateSucceeded, task.State)
	require.Equal(t, model.TaskKindProvision, task.Kind)
}

// TestProvisionServerAsync_FailureVisible 下载失败：任务 failed 带错误链 + 实例 statusReason
// 标注「搭建未完成」——空壳实例为什么不可用一目了然（FR-319 核心验收）。
func TestProvisionServerAsync_FailureVisible(t *testing.T) {
	svc, taskSvc, node, done := newFR319Harness(t, &failingDownloadWorker{})
	defer done()

	inst, taskID, err := svc.ProvisionServerAsync(context.Background(), ProvisionServerRequest{
		NodeID: node.ID, Name: "async-fail", CoreType: "spongevanilla", MCVersion: "1.21.1", MemoryMb: 1024,
	}, 1)
	require.NoError(t, err, "同步段应成功（失败在后台任务）")

	task := waitTaskTerminal(t, taskSvc, taskID)
	require.Equal(t, model.TaskStateFailed, task.State)
	require.Contains(t, task.Error, "下载超时", "任务错误应含底层原因")

	var got model.Instance
	require.NoError(t, svc.db.First(&got, inst.ID).Error)
	require.True(t, strings.HasPrefix(got.StatusReason, "搭建未完成："), "statusReason 应标注空壳原因，实际 %q", got.StatusReason)
}

// TestStart_BlockedWhileProvisionInFlight 核心还在下载（provision 任务未终态）时启动被拒（FR-319）。
// 真机复现：异步化后实例秒回 STOPPED 可点启动，点了得 Invalid or corrupt jarfile。
func TestStart_BlockedWhileProvisionInFlight(t *testing.T) {
	db := newInstanceTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Task{}, &model.TaskLog{}, &model.Notification{}))
	node := &model.Node{UUID: "node-gate", Status: model.NodeStatusOnline, OS: "linux"}
	require.NoError(t, db.Create(node).Error)
	pool := cpgrpc.NewClientPool()
	instSvc := NewInstanceService(db, NewGroupService(db), pool)

	inst := &model.Instance{Name: "prov-gate", NodeID: node.ID, Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDaemon, StartCommand: "java -jar server.jar", Status: model.InstanceStatusStopped}
	require.NoError(t, db.Create(inst).Error)

	// 关联本实例的 running provision 任务 → 启动应被拒。
	require.NoError(t, db.Create(&model.Task{TaskID: "t-prov", NodeID: node.ID, InstanceID: inst.ID,
		Kind: model.TaskKindProvision, State: model.TaskStateRunning}).Error)

	err := instSvc.Start(inst.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "搭建中")

	// 任务终态后放行（改 succeeded，闸不再拦；后续因节点未连接在委托层失败，但不再是搭建中错误）。
	require.NoError(t, db.Model(&model.Task{}).Where("task_id = ?", "t-prov").
		Update("state", model.TaskStateSucceeded).Error)
	if err := instSvc.Start(inst.ID); err != nil {
		require.NotContains(t, err.Error(), "搭建中", "任务终态后不应再被搭建中闸拦")
	}
}
