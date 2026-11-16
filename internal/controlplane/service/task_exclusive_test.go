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
