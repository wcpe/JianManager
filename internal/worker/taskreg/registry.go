// Package taskreg 提供 Worker 侧「运行中长任务内存登记表」（FR-183，见 ADR-040）。
//
// 长任务（如 JDK 安装）启动即返回 task_id 后，后台 goroutine 经本登记表持续更新进度/日志；
// 心跳每周期把表内任务快照随心跳上报给 CP。任务进入终态（succeeded/failed）并被心跳上报后
// 由心跳侧调用 Drop 从表内移除（已落 CP，无需再报）。
//
// 多协程安全：后台执行 goroutine 写、心跳 goroutine 读，全程加锁。
package taskreg

import (
	"context"
	"strconv"
	"sync"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// recentLogCap 单任务在内存里保留的最近日志行数上限（环形截断），避免长任务日志无限膨胀。
const recentLogCap = 50

// logSeqSep 心跳上报日志行时「绝对序号」与「正文」之间的分隔符。
// CP 据此把序号从行里解析出来，按 task_id + 绝对序号幂等追加（重叠窗口不重复入库，见 ADR-040）。
// 用制表符（日志正文出现概率低）。
const logSeqSep = "\t"

// logLine 内存中的一行日志，带全局单调递增的绝对序号（跨整个任务生命周期，不随环形截断重置）。
type logLine struct {
	seq  int
	text string
}

// task 内存中的一条任务状态。
type task struct {
	state    string
	progress int32
	errMsg   string
	result   string
	logs     []logLine
	nextSeq  int // 下一行日志的绝对序号
	// cancel 取消任务执行 context（FR-227）：Cancel 调用它真中断 Worker 操作（如下载），可为 nil。
	cancel context.CancelFunc
}

// Registry 运行中任务的线程安全登记表。零值不可用，须经 New 创建。
type Registry struct {
	mu    sync.Mutex
	tasks map[string]*task
}

// New 创建空登记表。
func New() *Registry {
	return &Registry{tasks: make(map[string]*task)}
}

// Start 登记一个新任务为 running（progress=0）。cancel 用于强制停止时真中断执行（FR-227，可为 nil）。
// 重复 taskID 覆盖。
func (r *Registry) Start(taskID string, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tasks[taskID] = &task{state: string(stateRunning), cancel: cancel}
}

// SetProgress 更新任务进度（0~100）。任务不存在则忽略。
func (r *Registry) SetProgress(taskID string, progress int32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.tasks[taskID]; ok {
		if progress < 0 {
			progress = 0
		} else if progress > 100 {
			progress = 100
		}
		t.progress = progress
	}
}

// AppendLog 追加一行日志，赋予全局单调递增的绝对序号（超过上限则丢弃最旧行，序号不重置）。
// 任务不存在则忽略。
func (r *Registry) AppendLog(taskID, line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.tasks[taskID]; ok {
		t.logs = append(t.logs, logLine{seq: t.nextSeq, text: line})
		t.nextSeq++
		if len(t.logs) > recentLogCap {
			t.logs = t.logs[len(t.logs)-recentLogCap:]
		}
	}
}

// Succeed 把任务置为 succeeded（progress=100），result 为成功结果 JSON。
// 已被 Cancel 置 canceled 的任务不再被覆盖（终态守卫，FR-227）。
func (r *Registry) Succeed(taskID, result string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.tasks[taskID]; ok && t.state == string(stateRunning) {
		t.state = string(stateSucceeded)
		t.progress = 100
		t.result = result
	}
}

// Fail 把任务置为 failed，errMsg 为失败原因。
// 已被 Cancel 置 canceled 的任务不再被覆盖（终态守卫，FR-227）。
func (r *Registry) Fail(taskID, errMsg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.tasks[taskID]; ok && t.state == string(stateRunning) {
		t.state = string(stateFailed)
		t.errMsg = errMsg
	}
}

// Cancel 强制停止任务（FR-227）：调用其执行 context 的 cancel（真中断下载等操作）并置 canceled 终态。
// 仅对运行中任务生效（已终态/不存在返回 false，幂等——心跳可能重复下发同一 id）。
func (r *Registry) Cancel(taskID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tasks[taskID]
	if !ok || t.state != string(stateRunning) {
		return false
	}
	t.state = string(stateCanceled)
	if t.cancel != nil {
		t.cancel()
	}
	return true
}

// Drop 从登记表移除任务（终态被心跳上报后调用）。
func (r *Registry) Drop(taskID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tasks, taskID)
}

// Snapshot 返回当前所有任务的快照（用于心跳上报）。返回的切片与底层状态解耦，可安全跨协程使用。
func (r *Registry) Snapshot() []*workerpb.TaskSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*workerpb.TaskSnapshot, 0, len(r.tasks))
	for id, t := range r.tasks {
		// 每行编码为 "<绝对序号>\t<正文>"，供 CP 按绝对序号幂等追加（重叠窗口去重）。
		logs := make([]string, len(t.logs))
		for i, l := range t.logs {
			logs[i] = strconv.Itoa(l.seq) + logSeqSep + l.text
		}
		out = append(out, &workerpb.TaskSnapshot{
			TaskId:         id,
			State:          t.state,
			Progress:       t.progress,
			Error:          t.errMsg,
			Result:         t.result,
			RecentLogLines: logs,
		})
	}
	return out
}

// 终态/非终态状态字符串常量（与 CP 侧 model.TaskState 取值一致）。
type taskStateStr string

const (
	stateRunning   taskStateStr = "running"
	stateSucceeded taskStateStr = "succeeded"
	stateFailed    taskStateStr = "failed"
	stateCanceled  taskStateStr = "canceled"
)
