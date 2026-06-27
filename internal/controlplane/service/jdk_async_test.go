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

// fakeInstallWorker 伪 WorkerServiceClient：仅实现 InstallJDK（其余方法走 nil 嵌入，不被调用）。
// 校验 CP 异步路径下发的 task_id 并立即回执，模拟「Worker 启动即返回」。
type fakeInstallWorker struct {
	workerpb.WorkerServiceClient
	gotTaskID string
	blockMs   int // >0 时阻塞，验证 CP 端有 30s 超时但仍不会 20min 卡死
}

func (f *fakeInstallWorker) InstallJDK(_ context.Context, in *workerpb.InstallJDKRequest, _ ...grpc.CallOption) (*workerpb.InstallJDKResponse, error) {
	f.gotTaskID = in.TaskId
	if f.blockMs > 0 {
		time.Sleep(time.Duration(f.blockMs) * time.Millisecond)
	}
	return &workerpb.InstallJDKResponse{Success: true, TaskId: in.TaskId}, nil
}

func newAsyncJDKDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:asyncjdk_" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Node{}, &model.NodeJDK{}, &model.Task{}, &model.TaskLog{}, &model.Notification{},
	))
	return db
}

// InstallAsync：建任务 → 下发携带 task_id → 立即返回 running 任务（不阻塞 20min）。
func TestJDKService_InstallAsync_ReturnsTaskImmediately(t *testing.T) {
	db := newAsyncJDKDB(t)
	pool := cpgrpc.NewClientPool()
	node := &model.Node{UUID: "u-async", Name: "n", Host: "h", GRPCPort: 1, WSPort: 2, Secret: "s"}
	require.NoError(t, db.Create(node).Error)

	fake := &fakeInstallWorker{}
	pool.SetWorkerClientForTest(node.UUID, fake)

	taskSvc := NewTaskService(db)
	taskSvc.SetNotificationService(NewNotificationService(db))
	jdkSvc := NewJDKService(db, pool)
	jdkSvc.SetTaskService(taskSvc)

	start := time.Now()
	task, err := jdkSvc.InstallAsync(node.ID, InstallJDKRequest{Vendor: "Temurin", MajorVersion: 21, Arch: "x64"}, 42)
	require.NoError(t, err)
	require.Less(t, time.Since(start), 5*time.Second, "异步路径应立即返回，不阻塞")

	// 返回的任务为 running，且 task_id 已下发给 Worker。
	require.Equal(t, model.TaskStateRunning, task.State)
	require.NotEmpty(t, task.TaskID)
	require.Equal(t, task.TaskID, fake.gotTaskID, "task_id 应原样下发 Worker")
	require.Equal(t, model.TaskKindJDKInstall, task.Kind)
	require.EqualValues(t, 42, task.CreatedBy)

	// DB 里任务存在且为 running。
	var saved model.Task
	require.NoError(t, db.Where("task_id = ?", task.TaskID).First(&saved).Error)
	require.Equal(t, model.TaskStateRunning, saved.State)

	// 此刻尚无 NodeJDK（落库延迟到心跳终态）。
	var jdkCount int64
	db.Model(&model.NodeJDK{}).Count(&jdkCount)
	require.Zero(t, jdkCount)
}

// 节点离线：不建悬挂任务（建任务前先校验，离线直接 ErrNodeOffline）。
func TestJDKService_InstallAsync_NodeOffline(t *testing.T) {
	db := newAsyncJDKDB(t)
	pool := cpgrpc.NewClientPool()
	node := &model.Node{UUID: "u-off", Name: "n", Host: "h", GRPCPort: 1, WSPort: 2, Secret: "s"}
	require.NoError(t, db.Create(node).Error)

	taskSvc := NewTaskService(db)
	jdkSvc := NewJDKService(db, pool)
	jdkSvc.SetTaskService(taskSvc)

	_, err := jdkSvc.InstallAsync(node.ID, InstallJDKRequest{Vendor: "Temurin", MajorVersion: 21, Arch: "x64"}, 1)
	require.ErrorIs(t, err, ErrNodeOffline)

	var taskCount int64
	db.Model(&model.Task{}).Count(&taskCount)
	require.Zero(t, taskCount, "离线不应建悬挂任务")
}
