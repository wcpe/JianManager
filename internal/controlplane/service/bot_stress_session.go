package service

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
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
//
// 保留旧的三选一（behavior / scenario / orchestrationYaml），同时接受命令压测的新字段：
// 前端「新建运行」向导提交的是 commandSchedule + loadProfile + thresholds，没有 behavior。
// 旧 DTO 不认识这几个键，JSON 绑定直接丢弃，随后 validateCreateRequest 因「三个场景源皆空」
// 判参数无效——于是不经模板的普通创建分支必 400。
type CreateBotStressSessionRequest struct {
	InstanceID        uint            `json:"instanceId"`
	Count             int             `json:"count"`
	Behavior          string          `json:"behavior"`
	NamePrefix        string          `json:"namePrefix"`
	Config            json.RawMessage `json:"config"`
	Scenario          json.RawMessage `json:"scenario"`
	OrchestrationYAML string          `json:"orchestrationYaml"`
	// 命令压测（V2 运行向导的非模板分支）：命令计划为必填，负载曲线与阈值为可选调整。
	CommandSchedule json.RawMessage `json:"commandSchedule"`
	LoadProfile     json.RawMessage `json:"loadProfile"`
	Thresholds      json.RawMessage `json:"thresholds"`
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
	ConnectedCount int                     `json:"connectedCount"`
	FailedCount    int                     `json:"failedCount"`
	State          model.BotLoadBatchState `json:"state"`
	StartedAt      *time.Time              `json:"startedAt,omitempty"`
	EndedAt        *time.Time              `json:"endedAt,omitempty"`
}

// BotStressSessionView 压测会话响应视图。
//
// V2 字段（schemaVersion / runState / loadProfile / thresholds / commandSchedule / templateId /
// verdict / maxStableBots / failureSummary / reportSummary）必须原样下发：前端详情页用
// `snapshot.schemaVersion === 2` 判是否渲染 V2 页面，缺它就整页退回旧版 JSON，六个 tab 与
// 会话 SSE Provider 都不会挂上。此前 DTO 漏了这组字段，真实 V2 会话一律被降级。
type BotStressSessionView struct {
	ID                   uint                           `json:"id"`
	UUID                 string                         `json:"uuid"`
	InstanceID           uint                           `json:"instanceId"`
	Count                int                            `json:"count"`
	Behavior             string                         `json:"behavior"`
	NamePrefix           string                         `json:"namePrefix"`
	Config               json.RawMessage                `json:"config,omitempty"`
	Scenario             *ScenarioV2                    `json:"scenario,omitempty"`
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

	// ── V2 运行面（schemaVersion=1 的历史行保持零值） ──
	SchemaVersion  int                    `json:"schemaVersion"`
	TemplateID     *uint                  `json:"templateId,omitempty"`
	RunState       *model.BotLoadRunState `json:"runState,omitempty"`
	CurrentStage   *int                   `json:"currentStage,omitempty"`
	Verdict        *model.BotLoadVerdict  `json:"verdict,omitempty"`
	MaxStableBots  *int                   `json:"maxStableBots,omitempty"`
	FailureSummary string                 `json:"failureSummary,omitempty"`
	ReportSummary  string                 `json:"reportSummary,omitempty"`
	LastError      string                 `json:"lastError,omitempty"`
	Succeeded      int                    `json:"succeeded"`
	Failed         int                    `json:"failed"`
	// LoadProfile/Thresholds/CommandSchedule 以原始 JSON 下发：前端按自己的契约解，
	// 服务端不在这里替它还原对象（也避免两处字段名漂移）。
	LoadProfile     json.RawMessage `json:"loadProfile,omitempty"`
	Thresholds      json.RawMessage `json:"thresholds,omitempty"`
	CommandSchedule json.RawMessage `json:"commandSchedule,omitempty"`
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
//
// 两条创建路径：
//   - 旧路径（behavior / scenario / orchestrationYaml）；
//   - 命令压测（commandSchedule + 可选 loadProfile / thresholds）——前端「新建运行」向导的非模板分支。
//     这条路径直接建 V2 会话（SchemaVersion=2 + runState=pending），否则详情页拿不到 V2 身份。
func (s *BotStressSessionService) Create(req CreateBotStressSessionRequest) (*BotStressSessionView, error) {
	config, err := normalizeStressSessionConfig(req.Config)
	if err != nil {
		return nil, err
	}
	if err := s.validateCreateRequest(req); err != nil {
		return nil, err
	}
	commandLoad := hasScenarioSource(req.CommandSchedule)
	behavior, summaryJSON, err := normalizeStressSessionOrchestration(req.Behavior, req.OrchestrationYAML)
	if err != nil {
		return nil, err
	}
	behavior, scenarioSnapshot, err := normalizeStressSessionScenario(req, behavior)
	if err != nil {
		return nil, err
	}
	if commandLoad {
		// 命令压测不以 behavior 驱动；固定标记，避免下游把它当 legacy behavior 会话。
		behavior = "scenario_v2"
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
		ScenarioSnapshot:     scenarioSnapshot,
		Status:               model.BotStressSessionPending,
		SchemaVersion:        1,
	}
	if commandLoad {
		// 命令计划是执行真源；负载曲线/阈值缺省时留空，由预检阶段用模板默认值补齐。
		sess.SchemaVersion = 2
		sess.CommandScheduleSnap = string(req.CommandSchedule)
		if hasScenarioSource(req.LoadProfile) {
			sess.LoadProfile = string(req.LoadProfile)
		}
		if hasScenarioSource(req.Thresholds) {
			sess.Thresholds = string(req.Thresholds)
		}
		pending := model.BotLoadRunPending
		sess.RunState = &pending
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
	hasScenario := hasScenarioSource(req.Scenario)
	// 命令压测的「运行意图」：有命令计划就算有场景源。
	// 之前只认 scenario / orchestrationYaml / behavior 三者，于是命令压测向导的载荷
	// 在这条校验上必被拒（它本来就不发 behavior）。
	hasCommandLoad := hasScenarioSource(req.CommandSchedule)
	if hasScenario && strings.TrimSpace(req.OrchestrationYAML) != "" {
		return ErrBotStressSessionInvalid
	}
	if !hasScenario && !hasCommandLoad && strings.TrimSpace(req.OrchestrationYAML) == "" && strings.TrimSpace(req.Behavior) == "" {
		return ErrBotStressSessionInvalid
	}
	if hasCommandLoad {
		// 命令计划与 scenario / orchestrationYaml 是两套不相容的场景源，不允许混用。
		if hasScenario || strings.TrimSpace(req.OrchestrationYAML) != "" {
			return ErrBotStressSessionInvalid
		}
		if !json.Valid(req.CommandSchedule) {
			return ErrBotStressSessionInvalid
		}
		if hasScenarioSource(req.LoadProfile) && !json.Valid(req.LoadProfile) {
			return ErrBotStressSessionInvalid
		}
		if hasScenarioSource(req.Thresholds) && !json.Valid(req.Thresholds) {
			return ErrBotStressSessionInvalid
		}
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

func normalizeStressSessionScenario(req CreateBotStressSessionRequest, behavior string) (string, string, error) {
	seed := legacyStressScenarioSeed(req)
	if hasScenarioSource(req.Scenario) {
		scenario, err := parseScenarioRequestSource(req.Scenario)
		if err != nil {
			return "", "", err
		}
		snapshot, err := CanonicalScenarioSnapshot(scenario, false)
		return "scenario_v2", snapshot, err
	}
	if strings.TrimSpace(req.OrchestrationYAML) != "" {
		scenario, err := ConvertStressOrchestrationToScenarioV2(req.OrchestrationYAML, seed)
		if err != nil {
			return behavior, "", nil
		}
		snapshot, err := CanonicalScenarioSnapshot(scenario, true)
		if err != nil {
			return behavior, "", nil
		}
		return behavior, snapshot, nil
	}
	scenario, err := ConvertLegacyBehaviorToScenarioV2(behavior, seed)
	if err != nil {
		return behavior, "", nil
	}
	snapshot, err := CanonicalScenarioSnapshot(scenario, true)
	if err != nil {
		return behavior, "", nil
	}
	return behavior, snapshot, nil
}

func hasScenarioSource(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null"
}

func parseScenarioRequestSource(raw json.RawMessage) (*ScenarioV2, error) {
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, `"`) {
		var source string
		if err := json.Unmarshal(raw, &source); err != nil {
			return nil, scenarioValidationError("$", "scenario 字符串无效")
		}
		return ParseScenarioV2([]byte(source))
	}
	return ParseScenarioV2(raw)
}

func legacyStressScenarioSeed(req CreateBotStressSessionRequest) int64 {
	value := fmt.Sprintf("%d|%d|%s|%s|%s", req.InstanceID, req.Count, strings.TrimSpace(req.NamePrefix), strings.TrimSpace(req.Behavior), req.OrchestrationYAML)
	digest := sha256.Sum256([]byte(value))
	return int64(binary.BigEndian.Uint64(digest[:8]))
}

// ValidateBotLoadScenarioForSession 在容量查询前校验已冻结场景；无 snapshot 的旧会话继续兼容。
func ValidateBotLoadScenarioForSession(session *model.BotStressSession) error {
	if session == nil || strings.TrimSpace(session.ScenarioSnapshot) == "" {
		return nil
	}
	_, err := ParseScenarioSnapshot(session.ScenarioSnapshot)
	return err
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
	if session == nil || session.BotCount < 1 || session.BotCount > maxBotLoadBatchSize || session.Behavior == "scenario_v2" {
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
	// AllowFleetOwned：这批 Bot 的 stress_session_id 正指向**本会话**，
	// 停止压测就是它的合法归属方在收尾。若按「手动越权」口径把它们排除掉，
	// 会话状态会变 stopped 而 Bot 仍是 connected（此前实测：ByStatus[stopped]=0）。
	if _, err := s.botSvc.Batch(BotBatchRequest{Action: BotBatchStop, Filter: &filter, AllowFleetOwned: true}, nil, false); err != nil {
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
		// V2 运行面：schemaVersion 是前端判定 V2 页面的唯一依据，必须如实下发。
		SchemaVersion:  sess.SchemaVersion,
		TemplateID:     sess.TemplateID,
		RunState:       sess.RunState,
		CurrentStage:   sess.CurrentStage,
		Verdict:        sess.Verdict,
		MaxStableBots:  sess.MaxStableBots,
		FailureSummary: sess.FailureSummary,
		ReportSummary:  sess.ReportSummary,
		LastError:      sess.LastError,
		Succeeded:      sess.Succeeded,
		Failed:         sess.Failed,
	}
	// 原始 JSON 字段：只在非空时下发，避免把 "" 当合法 JSON 传给前端。
	if strings.TrimSpace(sess.LoadProfile) != "" {
		view.LoadProfile = json.RawMessage(sess.LoadProfile)
	}
	if strings.TrimSpace(sess.Thresholds) != "" {
		view.Thresholds = json.RawMessage(sess.Thresholds)
	}
	if strings.TrimSpace(sess.CommandScheduleSnap) != "" {
		view.CommandSchedule = json.RawMessage(sess.CommandScheduleSnap)
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
	if strings.TrimSpace(sess.ScenarioSnapshot) != "" {
		scenario, err := projectPublicScenario(sess.ScenarioSnapshot)
		if err != nil {
			return nil, err
		}
		view.Scenario = scenario
	}
	return view, nil
}

func projectPublicScenario(snapshot string) (*ScenarioV2, error) {
	scenario, err := ParseScenarioSnapshot(snapshot)
	if err != nil {
		return nil, err
	}
	for _, cohort := range scenario.Cohorts {
		for _, action := range cohort.Steps {
			if action.LegacyBehavior != nil || action.AttackUntil != nil && action.AttackUntil.LegacyDurationSuccess {
				return nil, nil
			}
		}
	}
	return scenario, nil
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
			PlannedCount: batch.PlannedCount, AcceptedCount: batch.AcceptedCount, ConnectedCount: batch.ConnectedCount, FailedCount: batch.FailedCount,
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
