package service

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 卡死任务的自动回收（任务队列语义的「自愈」）。
//
// 【为什么必须有这一步】上面的互斥闸门保证了「同节点同组只有一个在跑」。但如果那个
// 任务本身**永远不会结束**，闸门就变成了永久锁——用户再也装不了 JDK，只能求人改库。
// 这不是假想：任务进度靠 Worker 心跳上报（IngestSnapshots），而 Worker 进程重启、
// 节点掉线、任务在 Worker 侧被驱逐，都会让上报永久中断，CP 侧那条记录就永远停在 running。
//
// 判定依据必须是「多久没收到这个任务的进展」，不能用绝对时长：一次正常的大型 JDK 下载
// 可能跑十几分钟且进度长时间不变，按「创建多久」判会误杀正在正常工作的任务，
// 而按「多久没上报」判则是精确的——活着的心跳一直在刷新 updated_at。
//
// 回收动作是置 failed 并写明原因（不是 canceled）：它不是用户要求停止的，
// 而是控制面判定其已失联。用户看到「已失联」能理解，看到「已取消」会误以为是自己或别人点的。

const (
	// taskReaperTick 扫描周期。
	taskReaperTick = time.Minute
	// taskStaleAfter 超过这么久没有进展上报，判定任务失联。
	//
	// 取值依据：Worker 心跳是这个数量级之下的周期（秒到十几秒），运行中的任务
	// 每次心跳都会带快照刷新 updated_at。5 分钟 = 连续几十次心跳都没带上它，
	// 足以排除网络抖动与一次慢查询，又不至于让用户干等太久才能重试。
	//
	// 【这个判定的前提，改动时务必守住】IngestSnapshots 必须**每拍都写一次**任务行
	// （哪怕 progress 与 state 都没变），GORM 才会刷新 updated_at。实测确认：
	// 同一进度值重复 Updates 仍会推进 50ms 级的时间戳。若有人为了省一次写而加了
	// 「值没变就跳过」的优化，这里立刻会开始误杀正在正常下载但进度停滞的任务
	// （大型 JDK 下载在同一百分比停十几分钟是常态）。
	taskStaleAfter = 5 * time.Minute
	// taskCancelStaleAfter 已请求取消但长时间未收到 Worker 终态回执的兜底回收阈值。
	// 给正常取消留出宽限期，同时防止 Worker 重启丢失内存任务表后永久锁死互斥组。
	taskCancelStaleAfter = 10 * time.Minute
)

// managedExclusiveTaskKinds 兜底识别老库升级前 exclusive_group 为空的重型任务类型。
var managedExclusiveTaskKinds = []string{
	model.TaskKindJDKInstall,
	model.TaskKindRuntimeInstall,
	model.TaskKindPkgInstall,
}

// TaskReaper 周期回收失联的在途任务，释放被它们占住的互斥组。
type TaskReaper struct {
	db      *gorm.DB
	tasks   *TaskService
	stopCh  chan struct{}
	started bool
	mu      sync.Mutex
}

// NewTaskReaper 创建回收器。
func NewTaskReaper(db *gorm.DB) *TaskReaper {
	return &TaskReaper{db: db, stopCh: make(chan struct{})}
}

// SetTaskService 注入 TaskService，以便回收进入终态时触发站内信等统一收尾副作用。
func (r *TaskReaper) SetTaskService(tasks *TaskService) {
	r.mu.Lock()
	r.tasks = tasks
	r.mu.Unlock()
}

// Start 启动周期回收（幂等：重复调用不会起第二个循环）。
func (r *TaskReaper) Start() {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return
	}
	r.started = true
	r.mu.Unlock()

	go func() {
		ticker := time.NewTicker(taskReaperTick)
		defer ticker.Stop()
		for {
			select {
			case <-r.stopCh:
				return
			case <-ticker.C:
				r.sweepOnce(time.Now())
			}
		}
	}()
}

// Stop 停止回收。
func (r *TaskReaper) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.started {
		return
	}
	r.started = false
	close(r.stopCh)
}

// sweepOnce 执行一轮回收，返回被回收的数量（供测试断言）。
//
// 只处理**参与互斥组或幂等键**的重型任务（含升级前 exclusive_group 为空的同类老行）；
// 同时覆盖以下三类死锁面：
//  1. running 且 cancel_requested=false 超过 taskStaleAfter 无进展上报 → 置 failed；
//  2. pending 超过 taskStaleAfter 未切入 running（如 CP 崩溃或 MarkRunning 失败）→ 置 failed；
//  3. cancel_requested=true 超过 taskCancelStaleAfter 仍未收到节点取消回执 → 置 canceled。
func (r *TaskReaper) sweepOnce(now time.Time) int {
	staleCutoff := now.Add(-taskStaleAfter)
	cancelCutoff := now.Add(-taskCancelStaleAfter)
	var stale []model.Task
	err := r.db.
		Where("state IN ?", []model.TaskState{model.TaskStatePending, model.TaskStateRunning}).
		Where("(cancel_requested = ? AND updated_at < ?) OR (cancel_requested = ? AND updated_at < ?)",
			false, staleCutoff, true, cancelCutoff).
		Where("exclusive_group <> '' OR idem_key <> '' OR kind IN ?", managedExclusiveTaskKinds).
		Find(&stale).Error
	if err != nil {
		slog.Warn("任务失联扫描失败", "error", err)
		return 0
	}

	r.mu.Lock()
	taskSvc := r.tasks
	r.mu.Unlock()

	reaped := 0
	for i := range stale {
		t := &stale[i]
		targetState := model.TaskStateFailed
		targetCutoff := staleCutoff
		reason := fmt.Sprintf("任务已失联：超过 %s 未收到节点进展上报（节点离线、Worker 重启或任务被中断）。已自动收尾以释放该节点的任务占用，可重新发起。",
			taskStaleAfter)
		if t.CancelRequested {
			targetState = model.TaskStateCanceled
			targetCutoff = cancelCutoff
			reason = fmt.Sprintf("取消请求超时：超过 %s 未收到节点中断确认（节点离线或 Worker 已重启）。已自动收尾以释放该节点的任务占用，可重新发起。",
				taskCancelStaleAfter)
		}
		// 带状态、取消标记与时间戳条件更新：避免与刚刚到达的心跳进展或终态回报并发冲突。
		res := r.db.Model(&model.Task{}).
			Where("task_id = ? AND state = ? AND cancel_requested = ? AND updated_at < ?",
				t.TaskID, t.State, t.CancelRequested, targetCutoff).
			Updates(map[string]any{"state": targetState, "error": reason})
		if res.Error != nil {
			slog.Warn("任务失联收尾失败", "taskId", t.TaskID, "error", res.Error)
			continue
		}
		if res.RowsAffected == 0 {
			continue // 已被心跳刷新或改成终态，跳过
		}
		t.State = targetState
		t.Error = reason
		if taskSvc != nil {
			taskSvc.finalizeTerminal(t)
		}
		reaped++
		slog.Warn("回收失联任务（释放互斥占用）",
			"taskId", t.TaskID, "kind", t.Kind, "nodeId", t.NodeID,
			"targetState", targetState, "title", t.Title, "lastSeen", t.UpdatedAt)
	}
	return reaped
}
