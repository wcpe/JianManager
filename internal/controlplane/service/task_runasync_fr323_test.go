package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newRunAsyncDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Task{}, &model.TaskLog{}, &model.Notification{}))
	return db
}

func waitTaskState(t *testing.T, svc *TaskService, taskID string, want model.TaskState) model.Task {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		task, _, err := svc.Get(nil, taskID)
		require.NoError(t, err)
		if task.State == want {
			return *task
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, _, _ := svc.Get(nil, taskID)
	t.Fatalf("任务未在期限内到达 %s，实际 %s", want, got.State)
	return model.Task{}
}

// TestRunAsync_SucceedsInBackground work 成功 → 后台推进到 succeeded + result（FR-323）。
func TestRunAsync_SucceedsInBackground(t *testing.T) {
	svc := NewTaskService(newRunAsyncDB(t))
	done := make(chan struct{})
	taskID := svc.RunAsync(RunSpec{NodeID: 1, Kind: model.TaskKindClone, Title: "克隆 x", CreatedBy: 1},
		func(_ context.Context, stage func(int, string)) (string, error) {
			stage(50, "拷贝工作目录…")
			close(done)
			return `{"ok":true}`, nil
		})
	require.NotEmpty(t, taskID, "必须立即返回 taskID")
	<-done
	task := waitTaskState(t, svc, taskID, model.TaskStateSucceeded)
	require.Equal(t, 100, task.Progress)
	require.Equal(t, `{"ok":true}`, task.Result)
}

// TestRunAsync_FailurePropagates work 报错 → failed + 错误链（FR-323）。
func TestRunAsync_FailurePropagates(t *testing.T) {
	svc := NewTaskService(newRunAsyncDB(t))
	taskID := svc.RunAsync(RunSpec{NodeID: 1, Kind: model.TaskKindBackupCreate, Title: "备份 x", CreatedBy: 1},
		func(_ context.Context, _ func(int, string)) (string, error) {
			return "", errors.New("磁盘满")
		})
	task := waitTaskState(t, svc, taskID, model.TaskStateFailed)
	require.Contains(t, task.Error, "磁盘满")
}

// TestRunAsync_LinksInstance InstanceID 必须随 pending 任务在同一 INSERT 中写入（FR-319/323）。
func TestRunAsync_LinksInstance(t *testing.T) {
	db := newRunAsyncDB(t)
	require.NoError(t, db.Exec(`CREATE TRIGGER require_runasync_instance
		BEFORE INSERT ON tasks
		WHEN NEW.kind = 'import' AND NEW.instance_id != 42
		BEGIN SELECT RAISE(ABORT, 'instance_id 必须随任务创建写入'); END`).Error)
	// 阻止后台抢先推进到 running，使断言稳定观察刚创建的 pending 行。
	require.NoError(t, db.Exec(`CREATE TRIGGER keep_runasync_pending
		BEFORE UPDATE OF state ON tasks
		WHEN NEW.state = 'running'
		BEGIN SELECT RAISE(IGNORE); END`).Error)

	svc := NewTaskService(db)
	block := make(chan struct{})
	taskID := svc.RunAsync(RunSpec{NodeID: 1, InstanceID: 42, Kind: model.TaskKindImport, Title: "导入 x", CreatedBy: 1},
		func(_ context.Context, _ func(int, string)) (string, error) {
			<-block
			return "", nil
		})
	require.NotEmpty(t, taskID, "INSERT 未携带 instance_id 时触发器会拒绝创建")
	var task model.Task
	require.NoError(t, db.Where("task_id = ?", taskID).First(&task).Error)
	require.Equal(t, model.TaskStatePending, task.State)
	require.Equal(t, uint(42), task.InstanceID)
	close(block)
	waitTaskState(t, svc, taskID, model.TaskStateSucceeded)
}

// TestRunAsync_DefaultDetail 空 Detail 落「排队中」（FR-323）。
func TestRunAsync_DefaultDetail(t *testing.T) {
	svc := NewTaskService(newRunAsyncDB(t))
	block := make(chan struct{})
	taskID := svc.RunAsync(RunSpec{NodeID: 1, Kind: model.TaskKindClone, Title: "t", CreatedBy: 1},
		func(_ context.Context, _ func(int, string)) (string, error) { <-block; return "", nil })
	task, _, err := svc.Get(nil, taskID)
	require.NoError(t, err)
	require.Equal(t, "排队中", task.Detail)
	close(block)
}
