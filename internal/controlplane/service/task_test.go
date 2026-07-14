package service

import (
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

func newTaskTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// 每个测试独立的内存库（DSN 带测试名），避免共享 cache 跨用例冲突。
	dsn := "file:taskdb_" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Task{}, &model.TaskLog{}, &model.Notification{},
		&model.NodeJDK{}, &model.Node{}, &model.User{},
	))
	return db
}

func newTaskSvc(t *testing.T, db *gorm.DB) *TaskService {
	t.Helper()
	svc := NewTaskService(db)
	svc.SetNotificationService(NewNotificationService(db))
	return svc
}

// jdk_install 成功终态：落一条 NodeJDK + 给发起人发成功站内信。
func TestTaskService_Ingest_JDKSuccess_PersistsJDKAndNotifies(t *testing.T) {
	db := newTaskTestDB(t)
	svc := newTaskSvc(t, db)

	node := &model.Node{UUID: "u1", Name: "n1", Host: "h", GRPCPort: 1, WSPort: 2, Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	_, err := svc.CreateTask("task-1", node.ID, model.TaskKindJDKInstall, "安装 JDK", "detail", 7)
	require.NoError(t, err)
	require.NoError(t, svc.MarkRunning("task-1"))

	result := `{"vendor":"Temurin","majorVersion":21,"version":"21.0.4","arch":"x64","path":"/opt/jdk-21","managed":true}`
	err = svc.IngestSnapshots("u1", []*workerpb.TaskSnapshot{{
		TaskId:         "task-1",
		State:          "succeeded",
		Progress:       100,
		Result:         result,
		RecentLogLines: []string{"0\t下载中 50%", "1\t安装完成"},
	}})
	require.NoError(t, err)

	// Task 落终态。
	var task model.Task
	require.NoError(t, db.Where("task_id = ?", "task-1").First(&task).Error)
	require.Equal(t, model.TaskStateSucceeded, task.State)
	require.Equal(t, 100, task.Progress)

	// NodeJDK 已落库。
	var jdks []model.NodeJDK
	require.NoError(t, db.Where("node_id = ?", node.ID).Find(&jdks).Error)
	require.Len(t, jdks, 1)
	require.Equal(t, "/opt/jdk-21", jdks[0].Path)
	require.True(t, jdks[0].Managed)

	// 给发起人（user 7）发了一条成功站内信。
	var notes []model.Notification
	require.NoError(t, db.Where("user_id = ?", uint(7)).Find(&notes).Error)
	require.Len(t, notes, 1)
	require.Equal(t, model.NotificationLevelSuccess, notes[0].Level)
	require.Equal(t, "task-1", notes[0].TaskID)

	// 日志已落库。
	var logs []model.TaskLog
	require.NoError(t, db.Where("task_id = ?", "task-1").Order("seq ASC").Find(&logs).Error)
	require.Len(t, logs, 2)
	require.Equal(t, "下载中 50%", logs[0].Line)
}

// jdk_install 失败终态：不落 NodeJDK，发失败站内信带 error。
func TestTaskService_Ingest_JDKFailure_Notifies(t *testing.T) {
	db := newTaskTestDB(t)
	svc := newTaskSvc(t, db)

	node := &model.Node{UUID: "u1", Name: "n1", Host: "h", GRPCPort: 1, WSPort: 2, Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	_, err := svc.CreateTask("task-2", node.ID, model.TaskKindJDKInstall, "安装 JDK", "d", 9)
	require.NoError(t, err)
	require.NoError(t, svc.MarkRunning("task-2"))

	err = svc.IngestSnapshots("u1", []*workerpb.TaskSnapshot{{
		TaskId: "task-2", State: "failed", Error: "下载返回 HTTP 404",
	}})
	require.NoError(t, err)

	var jdks int64
	db.Model(&model.NodeJDK{}).Count(&jdks)
	require.Zero(t, jdks, "失败不应落 JDK")

	var notes []model.Notification
	require.NoError(t, db.Where("user_id = ?", uint(9)).Find(&notes).Error)
	require.Len(t, notes, 1)
	require.Equal(t, model.NotificationLevelError, notes[0].Level)
	require.Contains(t, notes[0].Body, "404")
}

// 幂等：重复携带同一终态快照，不重复落 NodeJDK / 不重复发信。
func TestTaskService_Ingest_TerminalIdempotent(t *testing.T) {
	db := newTaskTestDB(t)
	svc := newTaskSvc(t, db)

	node := &model.Node{UUID: "u1", Name: "n1", Host: "h", GRPCPort: 1, WSPort: 2, Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	_, err := svc.CreateTask("task-3", node.ID, model.TaskKindJDKInstall, "安装 JDK", "d", 5)
	require.NoError(t, err)
	require.NoError(t, svc.MarkRunning("task-3"))

	snap := &workerpb.TaskSnapshot{
		TaskId: "task-3", State: "succeeded", Progress: 100,
		Result:         `{"vendor":"Temurin","majorVersion":17,"version":"17.0.12","arch":"x64","path":"/opt/jdk-17"}`,
		RecentLogLines: []string{"0\tok"},
	}
	// 上报三次同一终态。
	require.NoError(t, svc.IngestSnapshots("u1", []*workerpb.TaskSnapshot{snap}))
	require.NoError(t, svc.IngestSnapshots("u1", []*workerpb.TaskSnapshot{snap}))
	require.NoError(t, svc.IngestSnapshots("u1", []*workerpb.TaskSnapshot{snap}))

	var jdks int64
	db.Model(&model.NodeJDK{}).Count(&jdks)
	require.EqualValues(t, 1, jdks, "重复终态只应落一条 JDK")

	var notes int64
	db.Model(&model.Notification{}).Count(&notes)
	require.EqualValues(t, 1, notes, "重复终态只应发一条站内信")

	var logs int64
	db.Model(&model.TaskLog{}).Count(&logs)
	require.EqualValues(t, 1, logs, "重叠日志窗口按 seq 去重，不重复入库")
}

// 日志窗口跨周期重叠：绝对 seq 去重，既不丢行也不重复。
func TestTaskService_AppendLogs_OverlapWindowDedup(t *testing.T) {
	db := newTaskTestDB(t)
	svc := newTaskSvc(t, db)
	node := &model.Node{UUID: "u1", Name: "n1", Host: "h", GRPCPort: 1, WSPort: 2, Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	_, err := svc.CreateTask("task-4", node.ID, model.TaskKindJDKInstall, "t", "d", 1)
	require.NoError(t, err)
	require.NoError(t, svc.MarkRunning("task-4"))

	// 第一拍窗口：seq 0,1,2。
	require.NoError(t, svc.IngestSnapshots("u1", []*workerpb.TaskSnapshot{{
		TaskId: "task-4", State: "running", RecentLogLines: []string{"0\ta", "1\tb", "2\tc"},
	}}))
	// 第二拍窗口：seq 1,2,3（与上拍重叠 1,2）。
	require.NoError(t, svc.IngestSnapshots("u1", []*workerpb.TaskSnapshot{{
		TaskId: "task-4", State: "running", RecentLogLines: []string{"1\tb", "2\tc", "3\td"},
	}}))

	var logs []model.TaskLog
	require.NoError(t, db.Where("task_id = ?", "task-4").Order("seq ASC").Find(&logs).Error)
	require.Len(t, logs, 4, "应去重为 4 行 a,b,c,d")
	require.Equal(t, []string{"a", "b", "c", "d"}, []string{logs[0].Line, logs[1].Line, logs[2].Line, logs[3].Line})
}

// List 归属隔离：非管理员只见自己发起的（total 同口径只数本人），管理员见全部。
func TestTaskService_List_OwnershipScoping(t *testing.T) {
	db := newTaskTestDB(t)
	svc := newTaskSvc(t, db)
	node := &model.Node{UUID: "u1", Name: "n1", Host: "h", GRPCPort: 1, WSPort: 2, Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	_, _ = svc.CreateTask("ta", node.ID, model.TaskKindJDKInstall, "t", "d", 10)
	_, _ = svc.CreateTask("tb", node.ID, model.TaskKindJDKInstall, "t", "d", 20)

	user10 := &UserAccess{UserID: 10, IsPlatformAdmin: false}
	got, total, err := svc.List(user10, TaskListFilter{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.EqualValues(t, 1, total, "非管理员 total 只统计自己发起的")
	require.Equal(t, "ta", got[0].TaskID)

	admin := &UserAccess{UserID: 99, IsPlatformAdmin: true}
	got, total, err = svc.List(admin, TaskListFilter{})
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.EqualValues(t, 2, total)
}

// 强制停止（FR-227）：节点离线 → 直接置 canceled（无 Worker 操作可中断）。
func TestTaskService_Cancel_OfflineNode_Direct(t *testing.T) {
	db := newTaskTestDB(t)
	svc := newTaskSvc(t, db)
	node := &model.Node{UUID: "uoff", Name: "n", Host: "h", GRPCPort: 1, WSPort: 2, Secret: "s", Status: model.NodeStatusOffline}
	require.NoError(t, db.Create(node).Error)
	_, _ = svc.CreateTask("toff", node.ID, model.TaskKindJDKInstall, "t", "d", 5)
	require.NoError(t, svc.MarkRunning("toff"))

	require.NoError(t, svc.Cancel(&UserAccess{UserID: 5}, "toff"))
	var got model.Task
	require.NoError(t, db.Where("task_id = ?", "toff").First(&got).Error)
	require.Equal(t, model.TaskStateCanceled, got.State)
}

// 强制停止（FR-227）：节点在线 + running → 置 CancelRequested、状态留 running，被 PendingCancelTaskIDsByNodeUUID 选出。
func TestTaskService_Cancel_OnlineRunning_RequestsViaHeartbeat(t *testing.T) {
	db := newTaskTestDB(t)
	svc := newTaskSvc(t, db)
	node := &model.Node{UUID: "uon", Name: "n", Host: "h", GRPCPort: 1, WSPort: 2, Secret: "s", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	_, _ = svc.CreateTask("ton", node.ID, model.TaskKindJDKInstall, "t", "d", 5)
	require.NoError(t, svc.MarkRunning("ton"))

	require.NoError(t, svc.Cancel(&UserAccess{UserID: 5}, "ton"))
	var got model.Task
	require.NoError(t, db.Where("task_id = ?", "ton").First(&got).Error)
	require.Equal(t, model.TaskStateRunning, got.State, "在线 running 取消应等心跳真中断，不立即终态")
	require.True(t, got.CancelRequested)
	require.Equal(t, []string{"ton"}, svc.PendingCancelTaskIDsByNodeUUID("uon"))
}

// 强制停止（FR-227）：已终态 → ErrTaskAlreadyTerminal；越权 → ErrTaskNotFound。
func TestTaskService_Cancel_TerminalAndCrossUser(t *testing.T) {
	db := newTaskTestDB(t)
	svc := newTaskSvc(t, db)
	node := &model.Node{UUID: "ut", Name: "n", Host: "h", GRPCPort: 1, WSPort: 2, Secret: "s", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	_, _ = svc.CreateTask("tt", node.ID, model.TaskKindJDKInstall, "t", "d", 5)
	require.NoError(t, svc.MarkFailed("tt", "boom"))
	require.ErrorIs(t, svc.Cancel(&UserAccess{UserID: 5}, "tt"), ErrTaskAlreadyTerminal)

	_, _ = svc.CreateTask("tc", node.ID, model.TaskKindJDKInstall, "t", "d", 5)
	require.NoError(t, svc.MarkRunning("tc"))
	require.ErrorIs(t, svc.Cancel(&UserAccess{UserID: 999, IsPlatformAdmin: false}, "tc"), ErrTaskNotFound)
}

// List 筛选（FR-227）：按 state / keyword 过滤；total 与筛选同口径（FR-337）。
func TestTaskService_List_Filters(t *testing.T) {
	db := newTaskTestDB(t)
	svc := newTaskSvc(t, db)
	node := &model.Node{UUID: "uf", Name: "n", Host: "h", GRPCPort: 1, WSPort: 2, Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	_, _ = svc.CreateTask("f1", node.ID, model.TaskKindJDKInstall, "安装 Temurin", "d", 5)
	_, _ = svc.CreateTask("f2", node.ID, model.TaskKindJDKInstall, "安装 Corretto", "d", 5)
	require.NoError(t, svc.MarkFailed("f2", "x"))
	admin := &UserAccess{UserID: 1, IsPlatformAdmin: true}

	byState, total, err := svc.List(admin, TaskListFilter{State: "failed"})
	require.NoError(t, err)
	require.Len(t, byState, 1)
	require.EqualValues(t, 1, total)
	require.Equal(t, "f2", byState[0].TaskID)

	byKw, total, err := svc.List(admin, TaskListFilter{Keyword: "Temurin"})
	require.NoError(t, err)
	require.Len(t, byKw, 1)
	require.EqualValues(t, 1, total)
	require.Equal(t, "f1", byKw[0].TaskID)
}

// EffectiveWindow（FR-337）：limit 缺省 100、钳制 [1,500]；offset 负值归 0。
func TestTaskListFilter_EffectiveWindow(t *testing.T) {
	tests := []struct {
		name       string
		limit      int
		offset     int
		wantLimit  int
		wantOffset int
	}{
		{"零值缺省", 0, 0, 100, 0},
		{"负 limit 归缺省", -3, 0, 100, 0},
		{"下界 1 保留", 1, 0, 1, 0},
		{"区间内原样", 250, 30, 250, 30},
		{"上界 500 保留", 500, 0, 500, 0},
		{"超上限封顶 500", 501, 0, 500, 0},
		{"负 offset 归 0", 100, -7, 100, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limit, offset := TaskListFilter{Limit: tt.limit, Offset: tt.offset}.EffectiveWindow()
			require.Equal(t, tt.wantLimit, limit)
			require.Equal(t, tt.wantOffset, offset)
		})
	}
}

// List 分页窗口（FR-337）：offset 翻页窗口正确、越界 offset 返回空 items 且 total 不变、
// total 不受窗口影响（Count 与 Find 同筛选不同窗）。
func TestTaskService_List_PaginationWindow(t *testing.T) {
	db := newTaskTestDB(t)
	svc := newTaskSvc(t, db)
	node := &model.Node{UUID: "up", Name: "n", Host: "h", GRPCPort: 1, WSPort: 2, Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	// 依次建 p1..p5：created_at DESC, id DESC 稳定序下最新在前（p5,p4,p3,p2,p1）。
	for i := 1; i <= 5; i++ {
		_, err := svc.CreateTask(fmt.Sprintf("p%d", i), node.ID, model.TaskKindJDKInstall, "t", "d", 5)
		require.NoError(t, err)
	}
	admin := &UserAccess{UserID: 1, IsPlatformAdmin: true}

	// 首窗：limit=2 offset=0 → p5,p4；total=5（不受窗口影响）。
	got, total, err := svc.List(admin, TaskListFilter{Limit: 2})
	require.NoError(t, err)
	require.EqualValues(t, 5, total)
	require.Len(t, got, 2)
	require.Equal(t, "p5", got[0].TaskID)
	require.Equal(t, "p4", got[1].TaskID)

	// 翻窗：offset=2 → p3,p2。
	got, total, err = svc.List(admin, TaskListFilter{Limit: 2, Offset: 2})
	require.NoError(t, err)
	require.EqualValues(t, 5, total)
	require.Len(t, got, 2)
	require.Equal(t, "p3", got[0].TaskID)
	require.Equal(t, "p2", got[1].TaskID)

	// 越界 offset：items 空、total 不变。
	got, total, err = svc.List(admin, TaskListFilter{Limit: 2, Offset: 100})
	require.NoError(t, err)
	require.EqualValues(t, 5, total)
	require.Empty(t, got)

	// 负 offset 归 0：等价首窗。
	got, total, err = svc.List(admin, TaskListFilter{Limit: 2, Offset: -1})
	require.NoError(t, err)
	require.EqualValues(t, 5, total)
	require.Len(t, got, 2)
	require.Equal(t, "p5", got[0].TaskID)

	// 窗口小于命中数时 total 仍为全量：limit=1 + state 筛选组合。
	require.NoError(t, svc.MarkFailed("p1", "x"))
	require.NoError(t, svc.MarkFailed("p2", "x"))
	got, total, err = svc.List(admin, TaskListFilter{State: "failed", Limit: 1})
	require.NoError(t, err)
	require.EqualValues(t, 2, total, "total 为同筛选命中总数，不受 limit 截窗影响")
	require.Len(t, got, 1)
}

// Get 越权：非管理员查别人的任务返回 ErrTaskNotFound（不泄露存在性）。
func TestTaskService_Get_CrossUserHidden(t *testing.T) {
	db := newTaskTestDB(t)
	svc := newTaskSvc(t, db)
	node := &model.Node{UUID: "u1", Name: "n1", Host: "h", GRPCPort: 1, WSPort: 2, Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	_, _ = svc.CreateTask("tc", node.ID, model.TaskKindJDKInstall, "t", "d", 10)

	other := &UserAccess{UserID: 11, IsPlatformAdmin: false}
	_, _, err := svc.Get(other, "tc")
	require.ErrorIs(t, err, ErrTaskNotFound)

	owner := &UserAccess{UserID: 10, IsPlatformAdmin: false}
	task, _, err := svc.Get(owner, "tc")
	require.NoError(t, err)
	require.Equal(t, "tc", task.TaskID)
}

// MarkFailed（CP 下发失败路径）：置 failed 并发失败站内信，幂等不二次发信。
func TestTaskService_MarkFailed_NotifiesOnceTerminal(t *testing.T) {
	db := newTaskTestDB(t)
	svc := newTaskSvc(t, db)
	node := &model.Node{UUID: "u1", Name: "n1", Host: "h", GRPCPort: 1, WSPort: 2, Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	_, _ = svc.CreateTask("td", node.ID, model.TaskKindJDKInstall, "t", "d", 3)

	require.NoError(t, svc.MarkFailed("td", "下发 Worker 失败"))
	// 已终态再 MarkFailed 不应二次发信。
	require.NoError(t, svc.MarkFailed("td", "下发 Worker 失败"))

	var notes int64
	db.Model(&model.Notification{}).Where("user_id = ?", uint(3)).Count(&notes)
	require.EqualValues(t, 1, notes)
}
