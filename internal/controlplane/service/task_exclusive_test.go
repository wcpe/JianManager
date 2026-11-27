package service

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 填一条任务，state 可控。
func seedTask(t *testing.T, db *gorm.DB, taskID string, nodeID uint, kind string, state model.TaskState) {
	t.Helper()
	err := db.Create(&model.Task{
		TaskID: taskID, NodeID: nodeID, Kind: kind, State: state, Title: "seed " + kind,
	}).Error
	require.NoError(t, err)
}

// 核心契约：同节点同互斥组只能有一个非终态任务。
//
// 这条用例的每一行都是"会真的出错"的那种断言——例如没有闸门时
// 第二次 AcquireExclusive 会返回 nil，用例立刻红。
func TestTaskService_AcquireExclusive(t *testing.T) {
	cases := []struct {
		name    string
		first   string // 先占位的 kind
		second  string // 再申请安装的 kind
		blocked bool   // 期望被拦
	}{
		{
			name:    "JDK 在跑时再装 JDK：拦",
			first:   model.TaskKindJDKInstall,
			second:  model.TaskKindJDKInstall,
			blocked: true,
		},
		{
			name:    "JDK 在跑时装 Node.js：拦（同一互斥组，都写托管运行时目录）",
			first:   model.TaskKindJDKInstall,
			second:  model.TaskKindRuntimeInstall,
			blocked: true,
		},
		{
			name:    "JDK 在跑时装全局包：不拦（不同互斥组）",
			first:   model.TaskKindJDKInstall,
			second:  model.TaskKindPkgInstall,
			blocked: false,
		},
		{
			name:    "全局包在跑时再装全局包：拦",
			first:   model.TaskKindPkgInstall,
			second:  model.TaskKindPkgInstall,
			blocked: true,
		},
		{
			name:    "备份任务不参与互斥（不写托管运行时目录）",
			first:   model.TaskKindBackupCreate,
			second:  model.TaskKindJDKInstall,
			blocked: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newTaskTestDB(t)
			svc := NewTaskService(db)
			seedTask(t, db, "t-first", 1, tc.first, model.TaskStateRunning)

			err := svc.AcquireExclusive(1, tc.second, "")
			if tc.blocked {
				require.ErrorIs(t, err, ErrTaskExclusiveBusy)
				// 占用者信息要能带出来（前端据此展示"是谁在占着"）。
				var busy *TaskExclusiveBusyError
				require.True(t, errors.As(err, &busy))
				require.NotNil(t, busy.Existing)
				require.Equal(t, tc.first, busy.Existing.Kind)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// 终态必须释放闸门——否则一次失败的安装会永久占住该节点。
//
// 这条最容易写漏：只判 state='running' 就会让 pending 卡死节点，
// 只判非终态却忘了 canceled 就会让「强制停止后无法重试」。
func TestTaskService_AcquireExclusive_TerminalReleases(t *testing.T) {
	for _, state := range []model.TaskState{
		model.TaskStateSucceeded,
		model.TaskStateFailed,
		model.TaskStateCanceled,
	} {
		t.Run(string(state)+" 释放闸门", func(t *testing.T) {
			db := newTaskTestDB(t)
			svc := NewTaskService(db)
			seedTask(t, db, "t-done", 1, model.TaskKindJDKInstall, state)

			require.NoError(t, svc.AcquireExclusive(1, model.TaskKindJDKInstall, ""),
				"已结束的任务不该继续占着该节点")
		})
	}
}

// pending 也必须占位：任务已建但 Worker 还没起（202 之后、心跳之前），
// 此时再放一个进来就等于两个任务排队做同一件事。
func TestTaskService_AcquireExclusive_PendingStillHolds(t *testing.T) {
	db := newTaskTestDB(t)
	svc := NewTaskService(db)
	seedTask(t, db, "t-pending", 1, model.TaskKindRuntimeInstall, model.TaskStatePending)

	require.ErrorIs(t, svc.AcquireExclusive(1, model.TaskKindJDKInstall, ""), ErrTaskExclusiveBusy)
}

// 互斥是**按节点**的：节点 A 在装 JDK 不该挡住节点 B 装 JDK。
func TestTaskService_AcquireExclusive_PerNode(t *testing.T) {
	db := newTaskTestDB(t)
	svc := NewTaskService(db)
	seedTask(t, db, "t-node1", 1, model.TaskKindJDKInstall, model.TaskStateRunning)

	require.ErrorIs(t, svc.AcquireExclusive(1, model.TaskKindJDKInstall, ""), ErrTaskExclusiveBusy)
	require.NoError(t, svc.AcquireExclusive(2, model.TaskKindJDKInstall, ""),
		"别的节点不受影响")
}

// 被拦时给出的文案必须能让人看懂「谁在占着」（前端直接展示这句）。
func TestTaskService_ExclusiveBusyMessage(t *testing.T) {
	db := newTaskTestDB(t)
	svc := NewTaskService(db)
	seedTask(t, db, "t-1", 1, model.TaskKindJDKInstall, model.TaskStateRunning)

	err := svc.AcquireExclusive(1, model.TaskKindJDKInstall, "")
	require.Error(t, err)
	msg := DescribeExclusiveBusy(err)
	require.Contains(t, msg, "已有同类任务在跑")
	require.Contains(t, msg, "seed jdk_install") // 占用者标题
}

// ── 幂等复用 + 数据库约束 ──────────────────────────────────────────────

// AdmitTask 命中幂等键时必须**复用**已有任务（reused=true），而不是报错或新建。
//
// 这是「连点两次安装按钮」的正确语义：第二次点的不是错误请求，
// 它要做的正是第一次已经在做的事。
func TestTaskService_AdmitTask_IdempotentReuse(t *testing.T) {
	db := newTaskTestDB(t)
	svc := NewTaskService(db)
	idem := BuildIdemKey(1, model.TaskKindJDKInstall, "Temurin|21|x64|")

	first, reused1, err := svc.AdmitTask("t-jdk-1", TaskAdmitSpec{
		NodeID: 1, Kind: model.TaskKindJDKInstall, Title: "安装 JDK Temurin 21", IdemKey: idem,
	})
	require.NoError(t, err)
	require.False(t, reused1, "首次提交不该被标记为复用")

	second, reused2, err := svc.AdmitTask("t-jdk-2", TaskAdmitSpec{
		NodeID: 1, Kind: model.TaskKindJDKInstall, Title: "安装 JDK Temurin 21", IdemKey: idem,
	})
	require.NoError(t, err, "同参数的重复提交不是错误")
	require.True(t, reused2, "同参数的重复提交必须复用")
	require.Equal(t, first.TaskID, second.TaskID, "复用的必须是同一个任务")

	// 只能有一条记录：不能因为并发了就多出一条幽灵任务。
	var count int64
	require.NoError(t, db.Model(&model.Task{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

// 不同参数 → 不同幂等键 → 被互斥组拦住（不是复用）。
//
// 这两条语义必须区分开：同参数是"已经在做了"（复用），
// 不同参数是"抢同一块盘"（拒绝）。混成一个会让用户以为改了版本号就能装。
func TestTaskService_AdmitTask_DifferentParamsBlockedByGroup(t *testing.T) {
	db := newTaskTestDB(t)
	svc := NewTaskService(db)

	_, _, err := svc.AdmitTask("t-jdk-21", TaskAdmitSpec{
		NodeID: 1, Kind: model.TaskKindJDKInstall, Title: "安装 JDK Temurin 21",
		IdemKey: BuildIdemKey(1, model.TaskKindJDKInstall, "Temurin|21|x64|"),
	})
	require.NoError(t, err)

	_, _, err = svc.AdmitTask("t-jdk-17", TaskAdmitSpec{
		NodeID: 1, Kind: model.TaskKindJDKInstall, Title: "安装 JDK Temurin 17",
		IdemKey: BuildIdemKey(1, model.TaskKindJDKInstall, "Temurin|17|x64|"),
	})
	require.ErrorIs(t, err, ErrTaskExclusiveBusy, "不同版本也抢同一块托管目录，必须拒绝")
}

// 幂等键只在**非终态**时复用：上一次装完了，再点一次就该是新的安装。
func TestTaskService_AdmitTask_TerminalDoesNotReuse(t *testing.T) {
	db := newTaskTestDB(t)
	svc := NewTaskService(db)
	idem := BuildIdemKey(1, model.TaskKindJDKInstall, "Temurin|21|x64|")

	_, reused, err := svc.AdmitTask("t-1", TaskAdmitSpec{
		NodeID: 1, Kind: model.TaskKindJDKInstall, Title: "装 21", IdemKey: idem,
	})
	require.NoError(t, err)
	require.False(t, reused)
	require.NoError(t, db.Model(&model.Task{}).Where("task_id = ?", "t-1").
		Update("state", model.TaskStateSucceeded).Error)

	_, reused2, err := svc.AdmitTask("t-2", TaskAdmitSpec{
		NodeID: 1, Kind: model.TaskKindJDKInstall, Title: "装 21", IdemKey: idem,
	})
	require.NoError(t, err)
	require.False(t, reused2, "上一次已结束，本次是重新安装，不该复用")
}

// 数据库层必须真的挡住并发——这是应用层检查挡不住的那个窗口。
//
// 【这条测试为什么必须绕过 AdmitTask 直接写库】AdmitTask 的第一层检查会先返回
// 业务错误，走不到约束那里。要验证约束本身存在，只能直接 INSERT 两条同组非终态任务：
// 第二条必须被 DB 拒绝。如果把这个索引删掉，这条用例立刻红。
func TestTaskExclusiveIndex_BlocksConcurrentInsert(t *testing.T) {
	db := newTaskTestDB(t)
	require.NoError(t, db.Exec(
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_test_excl ON tasks (node_id, exclusive_group) "+
			"WHERE state IN ('pending','running') AND exclusive_group <> ''").Error)

	require.NoError(t, db.Create(&model.Task{
		TaskID: "a", NodeID: 1, Kind: model.TaskKindJDKInstall,
		State: model.TaskStateRunning, ExclusiveGroup: "runtime_install", Title: "first",
	}).Error)

	err := db.Create(&model.Task{
		TaskID: "b", NodeID: 1, Kind: model.TaskKindRuntimeInstall,
		State: model.TaskStatePending, ExclusiveGroup: "runtime_install", Title: "second",
	}).Error
	require.Error(t, err, "同节点同组两条非终态任务必须被数据库拒绝")
	require.True(t, isUniqueViolation(err), "错误要能被识别成唯一约束冲突，才能翻译成人话")

	// 终态共存不受约束：历史记录必须能大量堆。
	require.NoError(t, db.Model(&model.Task{}).Where("task_id = ?", "a").
		Update("state", model.TaskStateSucceeded).Error)
	require.NoError(t, db.Create(&model.Task{
		TaskID: "c", NodeID: 1, Kind: model.TaskKindJDKInstall,
		State: model.TaskStateRunning, ExclusiveGroup: "runtime_install", Title: "third",
	}).Error, "上一个已终态时必须能再起一个")

	// 不同节点不受约束。
	require.NoError(t, db.Create(&model.Task{
		TaskID: "d", NodeID: 2, Kind: model.TaskKindJDKInstall,
		State: model.TaskStateRunning, ExclusiveGroup: "runtime_install", Title: "other node",
	}).Error)
}

// 不参与互斥的任务（备份/快照/克隆…）必须完全不受影响。
func TestTaskExclusiveIndex_NonExclusiveKindsUnaffected(t *testing.T) {
	db := newTaskTestDB(t)
	require.NoError(t, db.Exec(
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_test_excl2 ON tasks (node_id, exclusive_group) "+
			"WHERE state IN ('pending','running') AND exclusive_group <> ''").Error)

	for i, taskID := range []string{"b1", "b2", "b3"} {
		require.NoError(t, db.Create(&model.Task{
			TaskID: taskID, NodeID: 1, Kind: model.TaskKindBackupCreate,
			State: model.TaskStateRunning, Title: "备份",
		}).Error, "第 %d 条备份任务不该被拦", i+1)
	}
}
