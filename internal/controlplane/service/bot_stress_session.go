package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

var (
	ErrBotStressSessionNotFound = errors.New("压测会话不存在")
	ErrBotStressSessionInvalid  = errors.New("压测会话参数无效")
)

// BotStressSessionService 管理持久化 Bot 压测会话。
type BotStressSessionService struct {
	db     *gorm.DB
	botSvc *BotService
}

// NewBotStressSessionService 创建 Bot 压测会话服务。
func NewBotStressSessionService(db *gorm.DB, botSvc *BotService) *BotStressSessionService {
	return &BotStressSessionService{db: db, botSvc: botSvc}
}

// CreateBotStressSessionRequest 创建压测会话请求。
type CreateBotStressSessionRequest struct {
	InstanceID        uint            `json:"instanceId"`
	Count             int             `json:"count"`
	Behavior          string          `json:"behavior"`
	NamePrefix        string          `json:"namePrefix"`
	Config            json.RawMessage `json:"config"`
	OrchestrationYAML string          `json:"orchestrationYaml"`
}

// BotStressSessionCounts 会话关联 Bot 聚合计数。
type BotStressSessionCounts struct {
	Total    int64            `json:"total"`
	ByStatus map[string]int64 `json:"byStatus"`
}

// BotLoadBatchSummary 是会话响应中的分布式批次摘要，不暴露节点密钥或内部错误细节。
type BotLoadBatchSummary struct {
	ID             uint                    `json:"id"`
	UUID           string                  `json:"uuid"`
	ExecutorNodeID uint                    `json:"executorNodeId"`
	Ordinal        int                     `json:"ordinal"`
	PlannedCount   int                     `json:"plannedCount"`
	AcceptedCount  int                     `json:"acceptedCount"`
	FailedCount    int                     `json:"failedCount"`
	State          model.BotLoadBatchState `json:"state"`
	StartedAt      *time.Time              `json:"startedAt,omitempty"`
	EndedAt        *time.Time              `json:"endedAt,omitempty"`
}

// BotStressSessionView 压测会话响应视图。
type BotStressSessionView struct {
	ID                   uint                           `json:"id"`
	UUID                 string                         `json:"uuid"`
	InstanceID           uint                           `json:"instanceId"`
	Count                int                            `json:"count"`
	Behavior             string                         `json:"behavior"`
	NamePrefix           string                         `json:"namePrefix"`
	Config               json.RawMessage                `json:"config,omitempty"`
	OrchestrationYAML    string                         `json:"orchestrationYaml,omitempty"`
	OrchestrationSummary *BotStressOrchestrationSummary `json:"orchestrationSummary,omitempty"`
	Status               model.BotStressSessionStatus   `json:"status"`
	StartedAt            *time.Time                     `json:"startedAt"`
	StoppedAt            *time.Time                     `json:"stoppedAt"`
	CreatedAt            time.Time                      `json:"createdAt"`
	UpdatedAt            time.Time                      `json:"updatedAt"`
	Counts               BotStressSessionCounts         `json:"counts"`
	Allocations          []BotLoadAllocation            `json:"allocations,omitempty"`
	Batches              []BotLoadBatchSummary          `json:"batches,omitempty"`
}

// BotStressSessionListQuery 会话列表分页参数。
type BotStressSessionListQuery struct {
	Page     int
	PageSize int
}

// BotStressSessionListResult 会话列表结果。
type BotStressSessionListResult struct {
	Items    []BotStressSessionView `json:"items"`
	Total    int64                  `json:"total"`
	Page     int                    `json:"page"`
	PageSize int                    `json:"pageSize"`
}

// Create 创建持久化压测会话，不立即创建 Bot。
func (s *BotStressSessionService) Create(req CreateBotStressSessionRequest) (*BotStressSessionView, error) {
	config, err := normalizeStressSessionConfig(req.Config)
	if err != nil {
		return nil, err
	}
	if err := s.validateCreateRequest(req); err != nil {
		return nil, err
	}
	behavior, summaryJSON, err := normalizeStressSessionOrchestration(req.Behavior, req.OrchestrationYAML)
	if err != nil {
		return nil, err
	}

	sess := &model.BotStressSession{
		InstanceID:           req.InstanceID,
		Name:                 strings.TrimSpace(req.NamePrefix),
		BotCount:             req.Count,
		Behavior:             behavior,
		NamePrefix:           strings.TrimSpace(req.NamePrefix),
		Config:               config,
		OrchestrationYAML:    orchestrationYAMLForStore(req.OrchestrationYAML),
		OrchestrationSummary: summaryJSON,
		Status:               model.BotStressSessionPending,
	}
	if err := s.db.Create(sess).Error; err != nil {
		return nil, fmt.Errorf("创建压测会话失败: %w", err)
	}
	return s.viewFromSession(*sess)
}

func (s *BotStressSessionService) validateCreateRequest(req CreateBotStressSessionRequest) error {
	if req.InstanceID == 0 || req.Count < 1 || req.Count > maxBatchTargets || strings.TrimSpace(req.NamePrefix) == "" {
		return ErrBotStressSessionInvalid
	}
	if strings.TrimSpace(req.OrchestrationYAML) == "" && strings.TrimSpace(req.Behavior) == "" {
		return ErrBotStressSessionInvalid
	}
	var inst model.Instance
	if err := s.db.Select("id").First(&inst, req.InstanceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrBotStressSessionInvalid
		}
		return fmt.Errorf("查询目标实例失败: %w", err)
	}
	return nil
}

func normalizeStressSessionOrchestration(behavior, orchestrationYAML string) (string, string, error) {
	if strings.TrimSpace(orchestrationYAML) == "" {
		return strings.TrimSpace(behavior), "", nil
	}
	orchestration, summary, err := ParseStressOrchestrationYAML(orchestrationYAML)
	if err != nil {
		return "", "", err
	}
	rawSummary, err := json.Marshal(summary)
	if err != nil {
		return "", "", fmt.Errorf("序列化压测会话编排摘要失败: %w", err)
	}
	return orchestration.Phases[0].Behavior, string(rawSummary), nil
}

func orchestrationYAMLForStore(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	return raw
}

func normalizeStressSessionConfig(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	if !json.Valid(raw) {
		return "", ErrBotStressSessionInvalid
	}
	return string(raw), nil
}

// Get 查询单个压测会话。
func (s *BotStressSessionService) Get(id uint) (*BotStressSessionView, error) {
	sess, err := s.findSession(id)
	if err != nil {
		return nil, err
	}
	view, err := s.viewFromSession(*sess)
	if err != nil {
		return nil, err
	}
	return s.enrichBotLoadView(view, sess)
}

// LoadForBotLoad 加载预检/启停所需的会话、目标实例与目标节点。
func (s *BotStressSessionService) LoadForBotLoad(ctx context.Context, id uint) (*model.BotStressSession, error) {
	var session model.BotStressSession
	if err := s.db.WithContext(ctx).Preload("Instance.Node").First(&session, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBotStressSessionNotFound
		}
		return nil, fmt.Errorf("查询 Bot 负载会话失败: %w", err)
	}
	return &session, nil
}

// IsLegacyBotStressSession 判断是否为仅使用 V1 字段、尚未生成分布式计划的兼容会话。
func IsLegacyBotStressSession(session *model.BotStressSession) bool {
	if session == nil || session.BotCount < 1 || session.BotCount > maxBotLoadBatchSize {
		return false
	}
	if strings.TrimSpace(session.AllocationPlan) != "" || !hasLegacyBotStressConfigShape(session.Config) {
		return false
	}
	return strings.TrimSpace(session.Behavior) != "" || strings.TrimSpace(session.OrchestrationYAML) != ""
}

func hasLegacyBotStressConfigShape(raw string) bool {
	if strings.TrimSpace(raw) == "" {
		return true
	}
	var config map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &config) != nil {
		return false
	}
	v2OnlyFields := map[string]struct{}{
		"scenario": {}, "scenarioid": {}, "scenariojson": {},
		"loadprofile": {}, "cohorts": {}, "executorpool": {},
		"executorpoolid": {}, "executornodeids": {},
	}
	for field := range config {
		if _, v2Only := v2OnlyFields[strings.ToLower(strings.TrimSpace(field))]; v2Only {
			return false
		}
	}
	return true
}

// List 分页列出压测会话，并按可访问实例集合收敛。
func (s *BotStressSessionService) List(query BotStressSessionListQuery, scopeIDs []uint, scope bool) (*BotStressSessionListResult, error) {
	page := query.Page
	if page < 1 {
		page = 1
	}
	size := query.PageSize
	if size < 1 {
		size = defaultBotPageSize
	}
	if size > maxBotPageSize {
		size = maxBotPageSize
	}

	base := applyStressSessionScope(s.db.Model(&model.BotStressSession{}), scopeIDs, scope)
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, fmt.Errorf("统计压测会话总数失败: %w", err)
	}

	var sessions []model.BotStressSession
	if total > 0 {
		if err := base.Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&sessions).Error; err != nil {
			return nil, fmt.Errorf("查询压测会话列表失败: %w", err)
		}
	}

	items := make([]BotStressSessionView, 0, len(sessions))
	for _, sess := range sessions {
		view, err := s.viewFromSession(sess)
		if err != nil {
			return nil, err
		}
		items = append(items, *view)
	}
	return &BotStressSessionListResult{Items: items, Total: total, Page: page, PageSize: size}, nil
}

func applyStressSessionScope(q *gorm.DB, scopeIDs []uint, scope bool) *gorm.DB {
	if !scope {
		return q
	}
	if len(scopeIDs) == 0 {
		return q.Where("1 = 0")
	}
	return q.Where("instance_id IN ?", scopeIDs)
}

// Start 启动压测会话，批量创建关联 Bot。
func (s *BotStressSessionService) Start(id uint) (*BotStressSessionView, error) {
	sess, err := s.findSession(id)
	if err != nil {
		return nil, err
	}
	switch sess.Status {
	case model.BotStressSessionRunning:
		return s.viewFromSession(*sess)
	case model.BotStressSessionStopped:
		return nil, fmt.Errorf("%w: 已停止会话不能再次启动", ErrBotStressSessionInvalid)
	}
	var orchestration *StressOrchestration
	if strings.TrimSpace(sess.OrchestrationYAML) != "" {
		parsed, _, err := ParseStressOrchestrationYAML(sess.OrchestrationYAML)
		if err != nil {
			return nil, err
		}
		orchestration = parsed
	}

	now := time.Now()
	claim := s.db.Model(&model.BotStressSession{}).
		Where("id = ? AND status IN ?", sess.ID, []model.BotStressSessionStatus{model.BotStressSessionPending, model.BotStressSessionError}).
		Updates(map[string]interface{}{
			"status":     model.BotStressSessionRunning,
			"started_at": &now,
			"ended_at":   nil,
		})
	if claim.Error != nil {
		return nil, fmt.Errorf("更新压测会话状态失败: %w", claim.Error)
	}
	if claim.RowsAffected == 0 {
		current, err := s.findSession(id)
		if err != nil {
			return nil, err
		}
		if current.Status == model.BotStressSessionStopped {
			return nil, fmt.Errorf("%w: 已停止会话不能再次启动", ErrBotStressSessionInvalid)
		}
		return s.viewFromSession(*current)
	}

	names, err := s.existingStressSessionBotNames(sess.ID)
	if err != nil {
		_ = s.db.Model(sess).Update("status", model.BotStressSessionError).Error
		return nil, err
	}
	// 结果账本从会话既有计数续算（error 态重启动补建缺口时不清零历史）。
	succeeded, failed := sess.Succeeded, sess.Failed
	for i := 1; i <= sess.BotCount; i++ {
		name := stressSessionBotName(sess.NamePrefix, i)
		if names[name] {
			continue
		}
		behavior := sess.Behavior
		var behaviorConfig json.RawMessage
		if orchestration != nil {
			behavior = "orchestrated"
			behaviorConfig, err = orchestration.BehaviorConfigForBot(i)
			if err != nil {
				_ = s.db.Model(sess).Update("status", model.BotStressSessionError).Error
				return nil, err
			}
		}
		bot, err := s.botSvc.Create(CreateBotRequest{
			InstanceID:      sess.InstanceID,
			Name:            name,
			Config:          sess.Config,
			Behavior:        behavior,
			BehaviorConfig:  behaviorConfig,
			StressSessionID: &sess.ID,
		})
		if err != nil {
			_ = s.db.Model(sess).Update("status", model.BotStressSessionError).Error
			return nil, fmt.Errorf("创建压测 Bot 失败: %w", err)
		}
		names[bot.Name] = true
		// 委托 Worker 失败的 Bot（记录已建但 status=error，如 bot-worker 依赖未装）计入会话失败账本；
		// 此前 Create 吞掉委托失败，20/20 全卡「等待中」而 Failed 恒 0，压测侧零反馈。
		if bot.Status == model.BotStatusError {
			succeeded, failed = accumulateStressBotOutcome(succeeded, failed, false)
			_ = s.db.Model(sess).Updates(map[string]any{"failed": failed, "last_error": bot.LastError}).Error
		} else {
			succeeded, failed = accumulateStressBotOutcome(succeeded, failed, true)
			_ = s.db.Model(sess).Update("succeeded", succeeded).Error
		}
	}

	return s.Get(id)
}

// accumulateStressBotOutcome 累计压测 Bot 创建结果计数（纯函数便于测试）。
func accumulateStressBotOutcome(succeeded, failed int, ok bool) (int, int) {
	if ok {
		return succeeded + 1, failed
	}
	return succeeded, failed + 1
}

func (s *BotStressSessionService) existingStressSessionBotNames(sessionID uint) (map[string]bool, error) {
	var bots []model.Bot
	if err := s.db.Select("name").Where("stress_session_id = ?", sessionID).Find(&bots).Error; err != nil {
		return nil, fmt.Errorf("查询压测 Bot 失败: %w", err)
	}
	names := make(map[string]bool, len(bots))
	for _, bot := range bots {
		names[bot.Name] = true
	}
	return names, nil
}

func stressSessionBotName(prefix string, index int) string {
	return fmt.Sprintf("%s-%03d", prefix, index)
}

// Stop 停止压测会话，将关联 Bot 置为 stopped。
func (s *BotStressSessionService) Stop(id uint) (*BotStressSessionView, error) {
	sess, err := s.findSession(id)
	if err != nil {
		return nil, err
	}
	if sess.Status == model.BotStressSessionStopped {
		return s.viewFromSession(*sess)
	}

	filter := BotFilter{StressSessionID: &sess.ID}
	if _, err := s.botSvc.Batch(BotBatchRequest{Action: BotBatchStop, Filter: &filter}, nil, false); err != nil {
		return nil, err
	}

	now := time.Now()
	if err := s.db.Model(sess).Updates(map[string]interface{}{
		"status":   model.BotStressSessionStopped,
		"ended_at": &now,
	}).Error; err != nil {
		return nil, fmt.Errorf("更新压测会话状态失败: %w", err)
	}
	return s.Get(id)
}

func (s *BotStressSessionService) findSession(id uint) (*model.BotStressSession, error) {
	var sess model.BotStressSession
	if err := s.db.First(&sess, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBotStressSessionNotFound
		}
		return nil, err
	}
	return &sess, nil
}

func (s *BotStressSessionService) viewFromSession(sess model.BotStressSession) (*BotStressSessionView, error) {
	if err := s.refreshSessionBotStatuses(sess.ID); err != nil {
		return nil, err
	}
	counts, err := s.countBots(sess.ID)
	if err != nil {
		return nil, err
	}
	view := &BotStressSessionView{
		ID:                sess.ID,
		UUID:              sess.UUID,
		InstanceID:        sess.InstanceID,
		Count:             sess.BotCount,
		Behavior:          sess.Behavior,
		NamePrefix:        sess.NamePrefix,
		OrchestrationYAML: sess.OrchestrationYAML,
		Status:            sess.Status,
		StartedAt:         sess.StartedAt,
		StoppedAt:         sess.EndedAt,
		CreatedAt:         sess.CreatedAt,
		UpdatedAt:         sess.UpdatedAt,
		Counts:            counts,
	}
	if sess.Config != "" {
		view.Config = json.RawMessage(sess.Config)
	}
	if sess.OrchestrationSummary != "" {
		var summary BotStressOrchestrationSummary
		if err := json.Unmarshal([]byte(sess.OrchestrationSummary), &summary); err != nil {
			return nil, fmt.Errorf("解析压测会话编排摘要失败: %w", err)
		}
		view.OrchestrationSummary = &summary
	}
	return view, nil
}

func (s *BotStressSessionService) enrichBotLoadView(view *BotStressSessionView, session *model.BotStressSession) (*BotStressSessionView, error) {
	if strings.TrimSpace(session.AllocationPlan) != "" {
		plan, err := DecodeBotLoadAllocationPlan(session.AllocationPlan)
		if err != nil {
			return nil, err
		}
		view.Allocations = append([]BotLoadAllocation(nil), plan.Allocations...)
	}
	var batches []model.BotLoadBatch
	if err := s.db.Where("stress_session_id = ?", session.ID).Order("ordinal ASC").Find(&batches).Error; err != nil {
		return nil, fmt.Errorf("查询 Bot 负载批次摘要失败: %w", err)
	}
	view.Batches = make([]BotLoadBatchSummary, 0, len(batches))
	for _, batch := range batches {
		view.Batches = append(view.Batches, BotLoadBatchSummary{
			ID: batch.ID, UUID: batch.UUID, ExecutorNodeID: batch.ExecutorNodeID, Ordinal: batch.Ordinal,
			PlannedCount: batch.PlannedCount, AcceptedCount: batch.AcceptedCount, FailedCount: batch.FailedCount,
			State: batch.State, StartedAt: batch.StartedAt, EndedAt: batch.EndedAt,
		})
	}
	return view, nil
}

func (s *BotStressSessionService) refreshSessionBotStatuses(sessionID uint) error {
	var bots []model.Bot
	if err := s.db.Preload("Instance.Node").Preload("ExecutorNode").Where("stress_session_id = ?", sessionID).Find(&bots).Error; err != nil {
		return fmt.Errorf("刷新压测 Bot 状态失败: %w", err)
	}
	s.botSvc.refreshStatuses(bots)
	return nil
}

func (s *BotStressSessionService) countBots(sessionID uint) (BotStressSessionCounts, error) {
	counts := BotStressSessionCounts{ByStatus: map[string]int64{}}
	type row struct {
		Status string
		Cnt    int64
	}
	var rows []row
	if err := s.db.Model(&model.Bot{}).
		Select("status, COUNT(*) AS cnt").
		Where("stress_session_id = ?", sessionID).
		Group("status").
		Scan(&rows).Error; err != nil {
		return counts, fmt.Errorf("统计压测会话 Bot 失败: %w", err)
	}
	for _, r := range rows {
		counts.Total += r.Cnt
		counts.ByStatus[r.Status] = r.Cnt
	}
	return counts, nil
}
