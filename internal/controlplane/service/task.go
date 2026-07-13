package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// ErrTaskNotFound 任务不存在。
var ErrTaskNotFound = errors.New("任务不存在")

// ErrTaskAlreadyTerminal 任务已终态，无法强制停止（FR-227）。
var ErrTaskAlreadyTerminal = errors.New("任务已结束，无法停止")

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

// MarkSucceeded 把任务置为 succeeded（CP 侧执行体完成时调用，FR-319）。
// 与 MarkFailed 对称：终态幂等 + 触发 finalizeTerminal 副作用（站内信）。
func (s *TaskService) MarkSucceeded(taskID, result string) error {
	var t model.Task
	if err := s.db.Where("task_id = ?", taskID).First(&t).Error; err != nil {
		return err
	}
	if t.State.IsTerminal() {
		return nil
	}
	if err := s.db.Model(&model.Task{}).Where("task_id = ?", taskID).
		Updates(map[string]any{"state": model.TaskStateSucceeded, "progress": 100, "result": result}).Error; err != nil {
		return err
	}
	t.State = model.TaskStateSucceeded
	t.Result = result
	s.finalizeTerminal(&t)
	return nil
}

// SetStage 更新 CP 侧执行任务的阶段展示（进度百分比 + 详情行，FR-319）。
// 同时把阶段行落 TaskLog（绝对序号自增），任务中心日志区可见完整阶段轨迹。
func (s *TaskService) SetStage(taskID string, progress int, stage string) {
	_ = s.db.Model(&model.Task{}).Where("task_id = ?", taskID).
		Updates(map[string]any{"progress": progress, "detail": stage}).Error
	var maxSeq int64
	_ = s.db.Model(&model.TaskLog{}).Where("task_id = ?", taskID).
		Select("COALESCE(MAX(seq),0)").Scan(&maxSeq).Error
	_ = s.db.Create(&model.TaskLog{TaskID: taskID, Seq: int(maxSeq) + 1, Line: stage}).Error
}

// RunSpec 一次长操作任务化的参数（FR-323 共享底座）。
type RunSpec struct {
	NodeID     uint          // 任务归属节点
	InstanceID uint          // 关联实例（0=无）；非 0 写 task.instance_id（启动闸/关联展示复用 FR-319）
	Kind       string        // TaskKind（import/clone/backup_create/backup_restore/provision）
	Title      string        // 任务标题
	Detail     string        // 初始详情（空=「排队中」）
	CreatedBy  uint          // 发起人（归属隔离 + 终态站内信收件人）
	Timeout    time.Duration // 后台执行超时（0=默认 30min）
}

// RunAsync 把一个长操作跑成后台任务（FR-323 共享底座）：登记任务→CP 后台 goroutine 执行→
// SetStage 阶段进度→MarkSucceeded/Failed→终态站内信。立即返回 taskID（不阻塞请求，独立
// context 不受前端断开影响）。work 收 (ctx, stage)，stage(progress,text) 上报阶段；返回
// (resultJSON, err)。业务副作用（statusReason/Backup record 状态/落库）由 work 自负——
// 底座只管任务生命周期 + instance_id 关联。
func (s *TaskService) RunAsync(spec RunSpec, work func(ctx context.Context, stage func(int, string)) (string, error)) string {
	taskID := uuid.New().String()
	detail := spec.Detail
	if detail == "" {
		detail = "排队中"
	}
	if _, err := s.CreateTask(taskID, spec.NodeID, spec.Kind, spec.Title, detail, spec.CreatedBy); err != nil {
		slog.Error("登记长操作任务失败", "kind", spec.Kind, "title", spec.Title, "error", err)
		return ""
	}
	if spec.InstanceID != 0 {
		_ = s.db.Model(&model.Task{}).Where("task_id = ?", taskID).Update("instance_id", spec.InstanceID).Error
	}
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		_ = s.MarkRunning(taskID)
		result, err := work(ctx, func(p int, text string) { s.SetStage(taskID, p, text) })
		if err != nil {
			slog.Error("长操作任务失败", "kind", spec.Kind, "taskId", taskID, "error", err)
			_ = s.MarkFailed(taskID, err.Error())
			return
		}
		_ = s.MarkSucceeded(taskID, result)
	}()
	return taskID
}

// TaskListFilter 任务列表筛选条件（FR-227）。零值字段表示不限制。
type TaskListFilter struct {
	Kind    string     // 任务种类（如 jdk_install）
	State   string     // 状态（pending/running/succeeded/failed/canceled）
	NodeID  uint       // 节点 id（0=不限）
	Keyword string     // 标题/详情模糊匹配
	Since   *time.Time // 创建时间下界
	Until   *time.Time // 创建时间上界
	Limit   int        // 默认 100
}

// List 列出任务（按筛选，FR-227）。非平台管理员只见自己发起的（createdBy）；平台管理员见全部。
// 按创建时间倒序，limit 默认 100。
func (s *TaskService) List(access *UserAccess, f TaskListFilter) ([]model.Task, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	q := s.db.Model(&model.Task{})
	if access != nil && !access.IsPlatformAdmin {
		q = q.Where("created_by = ?", access.UserID)
	}
	if f.Kind != "" {
		q = q.Where("kind = ?", f.Kind)
	}
	if f.State != "" {
		q = q.Where("state = ?", f.State)
	}
	if f.NodeID != 0 {
		q = q.Where("node_id = ?", f.NodeID)
	}
	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("title LIKE ? OR detail LIKE ?", like, like)
	}
	if f.Since != nil {
		q = q.Where("created_at >= ?", *f.Since)
	}
	if f.Until != nil {
		q = q.Where("created_at <= ?", *f.Until)
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

// Cancel 请求强制停止任务（FR-227）。权限同 Get（非管理员仅能停自己发起的，越权返回 ErrTaskNotFound）。
// 已终态 → ErrTaskAlreadyTerminal。pending（Worker 未起）或节点离线 → 直接置 canceled（无 Worker 操作可中断）；
// running 在线 → 置 CancelRequested=true，由心跳下发 cancel_task_ids 真中断、Worker 回报 canceled 落终态。
func (s *TaskService) Cancel(access *UserAccess, taskID string) error {
	var t model.Task
	if err := s.db.Where("task_id = ?", taskID).First(&t).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrTaskNotFound
		}
		return err
	}
	if access != nil && !access.IsPlatformAdmin && t.CreatedBy != access.UserID {
		return ErrTaskNotFound
	}
	if t.State.IsTerminal() {
		return ErrTaskAlreadyTerminal
	}
	if t.State == model.TaskStatePending || !s.nodeOnline(t.NodeID) {
		// 无运行中 Worker 操作可中断：直接落 canceled 终态 + 触发副作用。
		if err := s.db.Model(&model.Task{}).Where("task_id = ?", taskID).
			Updates(map[string]any{"state": model.TaskStateCanceled, "cancel_requested": true}).Error; err != nil {
			return err
		}
		t.State = model.TaskStateCanceled
		s.finalizeTerminal(&t)
		return nil
	}
	// running 在线：标记取消意图，等心跳下发 cancel_task_ids 真中断、Worker 回报 canceled。
	return s.db.Model(&model.Task{}).Where("task_id = ?", taskID).
		Update("cancel_requested", true).Error
}

// nodeOnline 报告节点是否在线（DB status，由心跳/离线检测维护）。
func (s *TaskService) nodeOnline(nodeID uint) bool {
	var n model.Node
	if err := s.db.Select("status").First(&n, nodeID).Error; err != nil {
		return false
	}
	return n.Status == model.NodeStatusOnline
}

// PendingCancelTaskIDsByNodeUUID 返回某节点（按 UUID）上「已请求取消且未终态」的任务 id（FR-227），
// 供心跳下发 cancel_task_ids。join 一次查询，避免心跳路径上额外解析节点 id。
func (s *TaskService) PendingCancelTaskIDsByNodeUUID(nodeUUID string) []string {
	var ids []string
	s.db.Model(&model.Task{}).
		Joins("JOIN nodes ON nodes.id = tasks.node_id").
		Where("nodes.uuid = ? AND tasks.cancel_requested = ? AND tasks.state IN ?", nodeUUID, true,
			[]model.TaskState{model.TaskStatePending, model.TaskStateRunning}).
		Pluck("tasks.task_id", &ids)
	return ids
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
	// 被强制停止（FR-227）：发中性「已停止」站内信，不落 JDK、不发「失败」。
	if t.State == model.TaskStateCanceled {
		s.notify(t.CreatedBy, model.NotificationLevelInfo, t.Title+" 已停止", "任务已被强制停止", t.TaskID)
		return
	}
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
	case model.TaskKindRuntimeInstall:
		// 运行时安装（FR-299，首批 Node.js）：成功落 node_runtimes（managed=true）+ 发信。
		if t.State == model.TaskStateSucceeded {
			s.persistRuntimeFromTask(t)
			s.notify(t.CreatedBy, model.NotificationLevelSuccess,
				"运行时安装完成", successRuntimeBody(t), t.TaskID)
		} else {
			s.notify(t.CreatedBy, model.NotificationLevelError,
				"运行时安装失败", failBody(t), t.TaskID)
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

// persistRuntimeFromTask 解析 runtime_install 任务的 result 落一条 model.NodeRuntime
//（managed=true，FR-299）。同 (node,type,path) 已存在则跳过（幂等双保险，同 persistJDKFromTask）。
func (s *TaskService) persistRuntimeFromTask(t *model.Task) {
	var r struct {
		Type    string `json:"type"`
		Name    string `json:"name"`
		Version string `json:"version"`
		Major   int    `json:"major"`
		Arch    string `json:"arch"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal([]byte(t.Result), &r); err != nil || r.Path == "" || r.Type == "" {
		slog.Warn("解析运行时安装结果失败", "taskId", t.TaskID, "error", err)
		return
	}
	var n int64
	s.db.Model(&model.NodeRuntime{}).Where("node_id = ? AND type = ? AND path = ?", t.NodeID, r.Type, r.Path).Count(&n)
	if n > 0 {
		return
	}
	rt := &model.NodeRuntime{
		NodeID:  t.NodeID,
		Type:    r.Type,
		Name:    r.Name,
		Version: r.Version,
		Major:   r.Major,
		Arch:    r.Arch,
		Path:    r.Path,
		Managed: true,
	}
	if err := s.db.Create(rt).Error; err != nil {
		slog.Warn("任务完成落运行时记录失败", "taskId", t.TaskID, "error", err)
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

func successRuntimeBody(t *model.Task) string {
	var r struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Path    string `json:"path"`
	}
	if json.Unmarshal([]byte(t.Result), &r) == nil && r.Version != "" {
		return fmt.Sprintf("%s %s 已安装到 %s", r.Name, r.Version, r.Path)
	}
	return t.Title + " 已完成"
}

func failBody(t *model.Task) string {
	if t.Error != "" {
		return t.Error
	}
	return "任务失败"
}
