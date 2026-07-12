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

// normalizeNodeArch（FR-299）：别名归一为 nodejs dist 命名（x64/arm64，区别于 adoptium aarch64）。
func TestNormalizeNodeArch(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"x64", "x64", false},
		{"amd64", "x64", false},
		{"X86_64", "x64", false},
		{"arm64", "arm64", false},
		{"aarch64", "arm64", false},
		{"armv7l", "armv7l", false},
		{"ppc64le", "ppc64le", false},
		{"", "", false},        // 空放行：Worker 按本机推导
		{"mips64", "", true},   // 未知拒绝
		{"sparc", "", true},
	}
	for _, tt := range tests {
		got, err := normalizeNodeArch(tt.in)
		if tt.wantErr {
			require.ErrorIs(t, err, ErrInvalidRuntimeArch, "arch=%q", tt.in)
			continue
		}
		require.NoError(t, err, "arch=%q", tt.in)
		require.Equal(t, tt.want, got, "arch=%q", tt.in)
	}
}

// fakeRuntimeInstallWorker 伪 WorkerServiceClient：实现 InstallRuntime/RemoveRuntime。
type fakeRuntimeInstallWorker struct {
	workerpb.WorkerServiceClient
	gotInstall *workerpb.InstallRuntimeRequest
	gotRemove  *workerpb.RemoveRuntimeRequest
	removeOK   bool
}

func (f *fakeRuntimeInstallWorker) InstallRuntime(_ context.Context, in *workerpb.InstallRuntimeRequest, _ ...grpc.CallOption) (*workerpb.InstallRuntimeResponse, error) {
	f.gotInstall = in
	return &workerpb.InstallRuntimeResponse{Success: true, TaskId: in.TaskId}, nil
}

func (f *fakeRuntimeInstallWorker) RemoveRuntime(_ context.Context, in *workerpb.RemoveRuntimeRequest, _ ...grpc.CallOption) (*workerpb.RemoveRuntimeResponse, error) {
	f.gotRemove = in
	if !f.removeOK {
		return &workerpb.RemoveRuntimeResponse{Success: false, Error: "boom"}, nil
	}
	return &workerpb.RemoveRuntimeResponse{Success: true}, nil
}

// fixedSettings 固定值 SettingsReader 桩。
type fixedSettings map[string]string

func (m fixedSettings) EffectiveValue(key string) string { return m[key] }

func newFR299DB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:fr299_" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Node{}, &model.NodeJDK{}, &model.NodeRuntime{}, &model.Instance{},
		&model.Task{}, &model.TaskLog{}, &model.Notification{},
	))
	return db
}

// InstallAsync：建任务（kind=runtime_install）→ 下发携带 task_id 与归一 arch/镜像 → 返回 running。
func TestRuntimeLibrary_InstallAsync(t *testing.T) {
	db := newFR299DB(t)
	pool := cpgrpc.NewClientPool()
	node := &model.Node{UUID: "u-rt", Name: "n", Host: "h", GRPCPort: 1, WSPort: 2, Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	fake := &fakeRuntimeInstallWorker{}
	pool.SetWorkerClientForTest(node.UUID, fake)

	taskSvc := NewTaskService(db)
	svc := NewRuntimeLibraryService(db, pool, NewJDKService(db, pool))
	svc.SetTaskService(taskSvc)
	svc.SetSettingsReader(fixedSettings{SettingKeyRuntimeMirrorNodeJS: "https://npmmirror.com/mirrors/node"})

	start := time.Now()
	task, err := svc.InstallAsync(node.ID, InstallRuntimeRequest{Type: "nodejs", Major: 22, Arch: "amd64"}, 42)
	require.NoError(t, err)
	require.Less(t, time.Since(start), 5*time.Second, "异步路径应立即返回")
	require.Equal(t, model.TaskStateRunning, task.State)
	require.Equal(t, model.TaskKindRuntimeInstall, task.Kind)
	require.EqualValues(t, 42, task.CreatedBy)

	// 下发参数：task_id 原样、arch 归一 amd64→x64、镜像取平台设置生效值。
	require.NotNil(t, fake.gotInstall)
	require.Equal(t, task.TaskID, fake.gotInstall.TaskId)
	require.Equal(t, "nodejs", fake.gotInstall.Type)
	require.EqualValues(t, 22, fake.gotInstall.Major)
	require.Equal(t, "x64", fake.gotInstall.Arch)
	require.Equal(t, "https://npmmirror.com/mirrors/node", fake.gotInstall.MirrorBase)

	// 此刻尚无 NodeRuntime（落库延迟到心跳终态）。
	var n int64
	db.Model(&model.NodeRuntime{}).Count(&n)
	require.Zero(t, n)
}

// InstallAsync 拒绝集：未知类型 / 未知 arch / 非正 major / 节点离线，均不建悬挂任务。
func TestRuntimeLibrary_InstallAsync_Rejections(t *testing.T) {
	db := newFR299DB(t)
	pool := cpgrpc.NewClientPool()
	node := &model.Node{UUID: "u-rt2", Name: "n", Host: "h", GRPCPort: 1, WSPort: 2, Secret: "s"}
	require.NoError(t, db.Create(node).Error)

	svc := NewRuntimeLibraryService(db, pool, NewJDKService(db, pool))
	svc.SetTaskService(NewTaskService(db))

	_, err := svc.InstallAsync(node.ID, InstallRuntimeRequest{Type: "python", Major: 3}, 1)
	require.ErrorIs(t, err, ErrInvalidRuntimeType)

	_, err = svc.InstallAsync(node.ID, InstallRuntimeRequest{Type: "nodejs", Major: 22, Arch: "mips64"}, 1)
	require.ErrorIs(t, err, ErrInvalidRuntimeArch)

	_, err = svc.InstallAsync(node.ID, InstallRuntimeRequest{Type: "nodejs", Major: 0}, 1)
	require.Error(t, err)

	// 节点离线（未入连接池）。
	_, err = svc.InstallAsync(node.ID, InstallRuntimeRequest{Type: "nodejs", Major: 22}, 1)
	require.ErrorIs(t, err, ErrNodeOffline)

	var taskCount int64
	db.Model(&model.Task{}).Count(&taskCount)
	require.Zero(t, taskCount, "被拒请求不应建悬挂任务")
}

// runtime_install 成功终态：落一条 NodeRuntime（managed=true）+ 发成功站内信（FR-299）。
func TestTaskService_Ingest_RuntimeSuccess_PersistsRuntime(t *testing.T) {
	db := newFR299DB(t)
	svc := NewTaskService(db)
	svc.SetNotificationService(NewNotificationService(db))

	node := &model.Node{UUID: "u-rt3", Name: "n", Host: "h", GRPCPort: 1, WSPort: 2, Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	_, err := svc.CreateTask("task-rt-1", node.ID, model.TaskKindRuntimeInstall, "安装 Node.js 22", "d", 7)
	require.NoError(t, err)
	require.NoError(t, svc.MarkRunning("task-rt-1"))

	result := `{"type":"nodejs","name":"Node.js 22","version":"22.17.0","major":22,"arch":"x64","path":"/data/opt/runtimes/nodejs-22/node-v22.17.0-linux-x64/bin/node","managed":true}`
	require.NoError(t, svc.IngestSnapshots("u-rt3", []*workerpb.TaskSnapshot{{
		TaskId: "task-rt-1", State: "succeeded", Progress: 100, Result: result,
	}}))

	var rt model.NodeRuntime
	require.NoError(t, db.Where("node_id = ? AND type = ?", node.ID, "nodejs").First(&rt).Error)
	require.True(t, rt.Managed, "安装器落库应为托管")
	require.Equal(t, "Node.js 22", rt.Name)
	require.Equal(t, "22.17.0", rt.Version)
	require.Equal(t, 22, rt.Major)
	require.Contains(t, rt.Path, "nodejs-22")

	var notes []model.Notification
	require.NoError(t, db.Where("user_id = ?", uint(7)).Find(&notes).Error)
	require.Len(t, notes, 1)
	require.Equal(t, model.NotificationLevelSuccess, notes[0].Level)
	require.Contains(t, notes[0].Body, "22.17.0")

	// 重复终态快照不重复落库（幂等双保险）。
	require.NoError(t, svc.IngestSnapshots("u-rt3", []*workerpb.TaskSnapshot{{
		TaskId: "task-rt-1", State: "succeeded", Progress: 100, Result: result,
	}}))
	var n int64
	db.Model(&model.NodeRuntime{}).Count(&n)
	require.EqualValues(t, 1, n)
}

// runtime_install 失败终态：不落 NodeRuntime，发失败站内信。
func TestTaskService_Ingest_RuntimeFailure_NoPersist(t *testing.T) {
	db := newFR299DB(t)
	svc := NewTaskService(db)
	svc.SetNotificationService(NewNotificationService(db))

	node := &model.Node{UUID: "u-rt4", Name: "n", Host: "h", GRPCPort: 1, WSPort: 2, Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	_, err := svc.CreateTask("task-rt-2", node.ID, model.TaskKindRuntimeInstall, "安装 Node.js 22", "d", 9)
	require.NoError(t, err)
	require.NoError(t, svc.MarkRunning("task-rt-2"))

	require.NoError(t, svc.IngestSnapshots("u-rt4", []*workerpb.TaskSnapshot{{
		TaskId: "task-rt-2", State: "failed", Error: "下载停滞已中断",
	}}))

	var n int64
	db.Model(&model.NodeRuntime{}).Count(&n)
	require.Zero(t, n)
	var notes []model.Notification
	require.NoError(t, db.Where("user_id = ?", uint(9)).Find(&notes).Error)
	require.Len(t, notes, 1)
	require.Equal(t, model.NotificationLevelError, notes[0].Level)
	require.Contains(t, notes[0].Body, "下载停滞")
}

// Delete 托管 nodejs：先经 Worker RemoveRuntime 删托管目录，成功后删记录（语义对齐 JDK）。
func TestRuntimeLibrary_DeleteManaged_RemovesWorkerFiles(t *testing.T) {
	db := newFR299DB(t)
	pool := cpgrpc.NewClientPool()
	node := &model.Node{UUID: "u-rt5", Name: "n", Host: "h", GRPCPort: 1, WSPort: 2, Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	fake := &fakeRuntimeInstallWorker{removeOK: true}
	pool.SetWorkerClientForTest(node.UUID, fake)

	svc := NewRuntimeLibraryService(db, pool, NewJDKService(db, pool))
	rt := &model.NodeRuntime{NodeID: node.ID, Type: "nodejs", Name: "Node.js 22", Version: "22.17.0", Major: 22, Arch: "x64", Path: "/data/opt/runtimes/nodejs-22/bin/node", Managed: true}
	require.NoError(t, db.Create(rt).Error)

	_, err := svc.Delete(node.ID, rt.ID, "nodejs")
	require.NoError(t, err)
	require.NotNil(t, fake.gotRemove, "托管删除应下发 Worker RemoveRuntime")
	require.Equal(t, "nodejs", fake.gotRemove.Type)
	require.Equal(t, rt.Path, fake.gotRemove.Path)
	var n int64
	db.Model(&model.NodeRuntime{}).Count(&n)
	require.Zero(t, n)
}

// Delete 托管 nodejs：Worker 拒绝删除时保留记录并返回错误（不产出「记录没了文件还在」）。
func TestRuntimeLibrary_DeleteManaged_WorkerRejectKeepsRecord(t *testing.T) {
	db := newFR299DB(t)
	pool := cpgrpc.NewClientPool()
	node := &model.Node{UUID: "u-rt6", Name: "n", Host: "h", GRPCPort: 1, WSPort: 2, Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	pool.SetWorkerClientForTest(node.UUID, &fakeRuntimeInstallWorker{removeOK: false})

	svc := NewRuntimeLibraryService(db, pool, NewJDKService(db, pool))
	rt := &model.NodeRuntime{NodeID: node.ID, Type: "nodejs", Name: "Node.js 22", Version: "22.17.0", Major: 22, Arch: "x64", Path: "/data/opt/runtimes/nodejs-22/bin/node", Managed: true}
	require.NoError(t, db.Create(rt).Error)

	_, err := svc.Delete(node.ID, rt.ID, "nodejs")
	require.Error(t, err)
	var n int64
	db.Model(&model.NodeRuntime{}).Count(&n)
	require.EqualValues(t, 1, n, "Worker 拒绝时记录应保留")
}

// Delete 外部登记 nodejs：不下发 Worker、只删记录（用户自管文件绝不动）。
func TestRuntimeLibrary_DeleteExternal_RecordOnly(t *testing.T) {
	db := newFR299DB(t)
	pool := cpgrpc.NewClientPool()
	node := &model.Node{UUID: "u-rt7", Name: "n", Host: "h", GRPCPort: 1, WSPort: 2, Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	fake := &fakeRuntimeInstallWorker{removeOK: true}
	pool.SetWorkerClientForTest(node.UUID, fake)

	svc := NewRuntimeLibraryService(db, pool, NewJDKService(db, pool))
	rt := &model.NodeRuntime{NodeID: node.ID, Type: "nodejs", Name: "Node.js 20", Version: "20.19.3", Major: 20, Arch: "x64", Path: "/usr/local/bin/node", Managed: false}
	require.NoError(t, db.Create(rt).Error)

	_, err := svc.Delete(node.ID, rt.ID, "nodejs")
	require.NoError(t, err)
	require.Nil(t, fake.gotRemove, "外部登记不得下发 Worker 删文件")
	var n int64
	db.Model(&model.NodeRuntime{}).Count(&n)
	require.Zero(t, n)
}
