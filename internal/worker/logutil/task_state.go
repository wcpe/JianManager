package logutil

// Rehydrate 任务状态。
//
// 2026-11-10：从已删除的 `logs/archive` 包迁来。
//
// 为何保留：取值对齐 proto `LogTaskState`，RPC 边界（taskStateFromString）
// 必须剥掉 `LOG_TASK_` 前缀后再匹配。即使 Rehydrate 执行器已删，
// 协议层仍需认识这些值——否则历史调用方传进带前缀的值会落入 default →
// 成功回灌被报成 FAILED 且 error 为空，调用方无法用 state 判定成功。

type TaskState string

const (
	TaskQueued    TaskState = "LOG_TASK_QUEUED"
	TaskRunning   TaskState = "LOG_TASK_RUNNING"
	TaskSucceeded TaskState = "LOG_TASK_SUCCEEDED"
	TaskFailed    TaskState = "LOG_TASK_FAILED"
	TaskCancelled TaskState = "LOG_TASK_CANCELLED"
	// TaskStale 表示任务超过租约/超时，与失败语义不同，不得归入 TaskFailed。
	TaskStale TaskState = "LOG_TASK_STALE"
)
