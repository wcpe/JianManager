package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// ErrTaskNotFound 任务不存在。
var ErrTaskNotFound = errors.New("任务不存在")

// TaskService 全局任务中心服务（FR-183，见 ADR-040）。
// 负责：建任务（被 JDKService 等长任务发起方调用）、按归属列任务/查任务+日志、
// 以及把 Worker 经心跳上报的任务快照汇聚落库（IngestSnapshots）+ 终态副作用。
type TaskService struct {
	db *gorm.DB
	// notifications 终态时发站内信（jdk 成功/失败）。可为 nil（不发信，仅落库）。
	notifications *NotificationService
}

// NewTaskService 创建任务服务。
func NewTaskService(db *gorm.DB) *TaskService {
	return &TaskService{db: db}
}

// SetNotificationService 注入站内信服务，用于任务终态发信（FR-183）。
// 在 main 装配阶段调用，避免构造期循环依赖。
func (s *TaskService) SetNotificationService(n *NotificationService) {
	s.notifications = n
}

// CreateTask 登记一条新任务（state=pending），返回其业务 task_id。
// taskID 为调用方生成的 UUID（与下发 Worker 的一致）。
func (s *TaskService) CreateTask(taskID string, nodeID uint, kind, title, detail string, createdBy uint) (*model.Task, error) {
	t := &model.Task{
		TaskID:    taskID,
		NodeID:    nodeID,
		Kind:      kind,
		State:     model.TaskStatePending,
		Progress:  0,
		Title:     title,
		Detail:    detail,
		CreatedBy: createdBy,
	}
	if err := s.db.Create(t).Error; err != nil {
		return nil, fmt.Errorf("创建任务失败: %w", err)
	}
	return t, nil
}

// MarkRunning 把任务置为 running（Worker 受理后由发起方调用）。
func (s *TaskService) MarkRunning(taskID string) error {
	return s.db.Model(&model.Task{}).Where("task_id = ?", taskID).
		Update("state", model.TaskStateRunning).Error
}

// MarkFailed 把任务置为 failed 并记录原因（发起方下发失败时调用，如 Worker RPC 失败）。
// 同时触发失败站内信（与心跳路径同一副作用，经 finalizeTerminal 保证幂等）。
func (s *TaskService) MarkFailed(taskID, reason string) error {
	var t model.Task
	if err := s.db.Where("task_id = ?", taskID).First(&t).Error; err != nil {
		return err
	}
	if t.State.IsTerminal() {
		return nil
	}
	if err := s.db.Model(&model.Task{}).Where("task_id = ?", taskID).
		Updates(map[string]any{"state": model.TaskStateFailed, "error": reason}).Error; err != nil {
		return err
	}
	t.State = model.TaskStateFailed
	t.Error = reason
	s.finalizeTerminal(&t)
	return nil
}

// List 列出任务。非平台管理员只见自己发起的（createdBy）；平台管理员见全部。
// 按创建时间倒序，limit 默认 100。
func (s *TaskService) List(access *UserAccess, limit int) ([]model.Task, error) {
	if limit <= 0 {
		limit = 100
	}
	q := s.db.Model(&model.Task{})
	if access != nil && !access.IsPlatformAdmin {
		q = q.Where("created_by = ?", access.UserID)
	}
	var tasks []model.Task
	if err := q.Order("created_at DESC, id DESC").Limit(limit).Find(&tasks).Error; err != nil {
		return nil, fmt.Errorf("查询任务列表失败: %w", err)
	}
	return tasks, nil
}

// Get 查单个任务（含日志）。非平台管理员只能查自己发起的；越权返回 ErrTaskNotFound（不泄露存在性）。
func (s *TaskService) Get(access *UserAccess, taskID string) (*model.Task, []model.TaskLog, error) {
	var t model.Task
	if err := s.db.Where("task_id = ?", taskID).First(&t).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, ErrTaskNotFound
		}
		return nil, nil, err
	}
	if access != nil && !access.IsPlatformAdmin && t.CreatedBy != access.UserID {
		return nil, nil, ErrTaskNotFound
	}
	var logs []model.TaskLog
	if err := s.db.Where("task_id = ?", taskID).Order("seq ASC").Find(&logs).Error; err != nil {
		return nil, nil, err
	}
	return &t, logs, nil
}

// IngestSnapshots 把 Worker 经心跳上报的任务快照汇聚落库（FR-183，见 ADR-040）。
// 对每条快照：按 task_id upsert Task（更新 state/progress/error/result）+ 幂等追加日志；
// 并在任务**首次**从非终态跃迁为终态时触发副作用（落 NodeJDK / 发站内信）。
// 失败不影响心跳本身，仅记录告警。Worker 心跳侧不会上报未经 CP 建过的任务（task_id 由 CP 下发），
// 故快照对应的 Task 一般已存在；若不存在（异常）则跳过（无归属信息无法建）。
func (s *TaskService) IngestSnapshots(nodeUUID string, snaps []*workerpb.TaskSnapshot) error {
	if len(snaps) == 0 {
		return nil
	}
	for _, snap := range snaps {
		if snap.TaskId == "" {
			continue
		}
		var t model.Task
		err := s.db.Where("task_id = ?", snap.TaskId).First(&t).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// CP 未建过该任务（理论上不应发生，task_id 由 CP 下发）；跳过，缺归属无法补建。
			slog.Warn("心跳上报未知任务，跳过", "taskId", snap.TaskId, "nodeUUID", nodeUUID)
			continue
		} else if err != nil {
			slog.Warn("查询任务失败", "taskId", snap.TaskId, "error", err)
			continue
		}

		wasTerminal := t.State.IsTerminal()
		newState := model.TaskState(snap.State)

		// 幂等追加日志（按 task_id + seq 唯一）：以当前已有日志行数为基准编号续接。
		s.appendLogs(snap.TaskId, snap.RecentLogLines)

		// 已是终态则不再更新状态（避免终态被后续重复快照覆盖）。
		if wasTerminal {
			continue
		}

		updates := map[string]any{
			"state":    newState,
			"progress": clampProgress(int(snap.Progress)),
		}
		if snap.Error != "" {
			updates["error"] = snap.Error
		}
		if snap.Result != "" {
			updates["result"] = snap.Result
		}
		if err := s.db.Model(&model.Task{}).Where("task_id = ?", snap.TaskId).Updates(updates).Error; err != nil {
			slog.Warn("更新任务失败", "taskId", snap.TaskId, "error", err)
			continue
		}

		// 首次进入终态：触发副作用。
		if newState.IsTerminal() {
			t.State = newState
			t.Error = snap.Error
			t.Result = snap.Result
			s.finalizeTerminal(&t)
		}
	}
	return nil
}

// finalizeTerminal 执行任务终态副作用（FR-183，见 ADR-040）。
// 调用方须保证「首次进入终态」（用 DB 旧 state 非终态判定），本方法不再去重。
//   - jdk_install 成功：解析 result→落 model.NodeJDK + 发成功站内信；
//   - 失败：发失败站内信。
func (s *TaskService) finalizeTerminal(t *model.Task) {
	switch t.Kind {
	case model.TaskKindJDKInstall:
		if t.State == model.TaskStateSucceeded {
			s.persistJDKFromTask(t)
			s.notify(t.CreatedBy, model.NotificationLevelSuccess,
				"JDK 安装完成", successJDKBody(t), t.TaskID)
		} else {
			s.notify(t.CreatedBy, model.NotificationLevelError,
				"JDK 安装失败", failBody(t), t.TaskID)
		}
	default:
		// 其它任务类型：仅按成功/失败发通用站内信。
		if t.State == model.TaskStateSucceeded {
			s.notify(t.CreatedBy, model.NotificationLevelSuccess, t.Title+" 完成", "", t.TaskID)
		} else {
			s.notify(t.CreatedBy, model.NotificationLevelError, t.Title+" 失败", failBody(t), t.TaskID)
		}
	}
}

// persistJDKFromTask 解析 jdk_install 任务的 result 落一条 model.NodeJDK（替代原同步路径的落库）。
// 同 path 已存在则跳过（幂等：心跳可能在 Drop 前重复携带终态，但状态已终态不会二次进入此处；
// 双保险按 node+path 去重）。
func (s *TaskService) persistJDKFromTask(t *model.Task) {
	var r struct {
		Vendor       string `json:"vendor"`
		MajorVersion int    `json:"majorVersion"`
		Version      string `json:"version"`
		Arch         string `json:"arch"`
		Path         string `json:"path"`
		Managed      bool   `json:"managed"`
	}
	if err := json.Unmarshal([]byte(t.Result), &r); err != nil || r.Path == "" {
		slog.Warn("解析 JDK 安装结果失败", "taskId", t.TaskID, "error", err)
		return
	}
	var n int64
	s.db.Model(&model.NodeJDK{}).Where("node_id = ? AND path = ?", t.NodeID, r.Path).Count(&n)
	if n > 0 {
		return
	}
	jdk := &model.NodeJDK{
		NodeID:       t.NodeID,
		Vendor:       r.Vendor,
		MajorVersion: r.MajorVersion,
		Version:      r.Version,
		Arch:         r.Arch,
		Path:         r.Path,
		Managed:      true,
	}
	if err := s.db.Create(jdk).Error; err != nil {
		slog.Warn("任务完成落 JDK 记录失败", "taskId", t.TaskID, "error", err)
	}
}

// appendLogs 幂等追加日志行（FR-183，见 ADR-040）。
// 每行编码为 "<绝对序号>\t<正文>"（Worker 侧赋予的全局单调序号）；CP 解析出绝对序号后
// 按 (task_id, seq) 唯一约束 ON CONFLICT DO NOTHING 入库——心跳「最近 N 行」窗口跨周期重叠时，
// 重叠行因绝对序号相同被天然去重，既不丢行也不重复。无法解析序号的行（异常）跳过。
func (s *TaskService) appendLogs(taskID string, lines []string) {
	if len(lines) == 0 {
		return
	}
	now := time.Now()
	logs := make([]model.TaskLog, 0, len(lines))
	for _, raw := range lines {
		seq, text, ok := parseLogLine(raw)
		if !ok {
			continue
		}
		logs = append(logs, model.TaskLog{TaskID: taskID, Seq: seq, Line: text, TS: now})
	}
	if len(logs) == 0 {
		return
	}
	if err := s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&logs).Error; err != nil {
		slog.Debug("追加任务日志失败（容忍）", "taskId", taskID, "error", err)
	}
}

// parseLogLine 解析 "<绝对序号>\t<正文>" 编码的心跳日志行（见 taskreg 编码契约）。
func parseLogLine(raw string) (seq int, text string, ok bool) {
	i := strings.IndexByte(raw, '\t')
	if i <= 0 {
		return 0, "", false
	}
	n, err := strconv.Atoi(raw[:i])
	if err != nil {
		return 0, "", false
	}
	return n, raw[i+1:], true
}

// notify 发一条站内信（注入了 NotificationService 时）。userID=0（系统任务）不发。
func (s *TaskService) notify(userID uint, level model.NotificationLevel, title, body, taskID string) {
	if s.notifications == nil || userID == 0 {
		return
	}
	if err := s.notifications.Create(userID, level, title, body, taskID); err != nil {
		slog.Warn("发送站内信失败", "userId", userID, "taskId", taskID, "error", err)
	}
}

func clampProgress(p int) int {
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}

func successJDKBody(t *model.Task) string {
	var r struct {
		Vendor  string `json:"vendor"`
		Version string `json:"version"`
		Path    string `json:"path"`
	}
	if json.Unmarshal([]byte(t.Result), &r) == nil && r.Version != "" {
		return fmt.Sprintf("%s %s 已安装到 %s", r.Vendor, r.Version, r.Path)
	}
	return t.Title + " 已完成"
}

func failBody(t *model.Task) string {
	if t.Error != "" {
		return t.Error
	}
	return "任务失败"
}
