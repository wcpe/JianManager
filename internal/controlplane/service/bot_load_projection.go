package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// BotLoadProjectionService 投影 failures/events 读模型（FR-370）。
type BotLoadProjectionService struct {
	db *gorm.DB
}

// NewBotLoadProjectionService 创建投影服务。
func NewBotLoadProjectionService(db *gorm.DB) *BotLoadProjectionService {
	return &BotLoadProjectionService{db: db}
}

// BotLoadFailureView 与规格/前端 BotLoadFailure 对齐。
type BotLoadFailureView struct {
	ID             string `json:"id"`
	RunUUID        string `json:"runUuid"`
	BotUUID        string `json:"botUuid,omitempty"`
	ExecutorNodeID *uint  `json:"executorNodeId,omitempty"`
	ActionRunID    string `json:"actionRunId,omitempty"`
	StepID         string `json:"stepId,omitempty"`
	CommandID      string `json:"commandId,omitempty"`
	Category       string `json:"category"`
	LegacyCategory string `json:"legacyCategory,omitempty"`
	ErrorCode      string `json:"errorCode"`
	Message        string `json:"message"`
	Retryable      bool   `json:"retryable"`
	OccurredAt     string `json:"occurredAt"`
}

// BotLoadFailureListResult 分页失败列表。
type BotLoadFailureListResult struct {
	Items    []BotLoadFailureView `json:"items"`
	Total    int64                `json:"total"`
	Page     int                  `json:"page"`
	PageSize int                  `json:"pageSize"`
}

// BotLoadEventView 最小事件投影（payload 透传 JSON）。
type BotLoadEventView struct {
	EventID        string          `json:"eventId"`
	RunID          uint            `json:"runId"`
	RunUUID        string          `json:"runUuid"`
	Timestamp      string          `json:"timestamp"`
	Type           string          `json:"type"`
	StageIndex     *int            `json:"stageIndex,omitempty"`
	ActionRunID    string          `json:"actionRunId,omitempty"`
	BotUUID        string          `json:"botUuid,omitempty"`
	ExecutorNodeID *uint           `json:"executorNodeId,omitempty"`
	StepID         string          `json:"stepId,omitempty"`
	Payload        json.RawMessage `json:"payload"`
	Legacy         json.RawMessage `json:"legacy,omitempty"`
}

// BotLoadEventListResult 事件分页（含 snapshotEventId）。
type BotLoadEventListResult struct {
	Items           []BotLoadEventView `json:"items"`
	Total           int64              `json:"total"`
	Page            int                `json:"page"`
	PageSize        int                `json:"pageSize"`
	SnapshotEventID string             `json:"snapshotEventId"`
}

// ListFailuresQuery 失败列表筛选。
type ListFailuresQuery struct {
	Page           int
	PageSize       int
	Category       string
	ErrorCode      string
	BotUUID        string
	ExecutorNodeID *uint
	StepID         string
	From, To       *time.Time
}

// ListEventsQuery 事件列表筛选。
type ListEventsQuery struct {
	Page            int
	PageSize        int
	Type            string
	EventID         string
	ActionRunID     string
	BotUUID         string
	ExecutorNodeID  *uint
	StepID          string
	From, To        *time.Time
	SnapshotEventID string
}

// ListFailures 从 action_results + command_checkpoints 投影失败项。
func (s *BotLoadProjectionService) ListFailures(ctx context.Context, sessionID uint, q ListFailuresQuery) (*BotLoadFailureListResult, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("投影服务未初始化")
	}
	var sess model.BotStressSession
	if err := s.db.WithContext(ctx).First(&sess, sessionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBotStressSessionNotFound
		}
		return nil, err
	}
	page, pageSize := normalizeProjectionPage(q.Page, q.PageSize)

	// 收集所有失败再过滤分页（规模受会话 bot/occurrence 上限约束；V1 可接受）。
	items := make([]BotLoadFailureView, 0, 64)

	// 1) 动作终态失败
	var actions []model.BotLoadActionResult
	aq := s.db.WithContext(ctx).Where("stress_session_id = ? AND status IN ?", sessionID,
		[]model.BotLoadActionResultStatus{model.BotLoadActionFailed, model.BotLoadActionTimedOut, model.BotLoadActionCancelled})
	if q.ErrorCode != "" {
		aq = aq.Where("error_code = ?", q.ErrorCode)
	}
	if q.StepID != "" {
		aq = aq.Where("step_id = ?", q.StepID)
	}
	if q.From != nil {
		aq = aq.Where("COALESCE(ended_at, started_at) >= ?", q.From.UTC())
	}
	if q.To != nil {
		aq = aq.Where("COALESCE(ended_at, started_at) <= ?", q.To.UTC())
	}
	if err := aq.Order("id DESC").Limit(2000).Find(&actions).Error; err != nil {
		return nil, fmt.Errorf("查询动作失败项失败: %w", err)
	}
	botIDs := make([]uint, 0, len(actions))
	for _, a := range actions {
		botIDs = append(botIDs, a.BotID)
	}
	botUUIDByID := map[uint]string{}
	botExecByID := map[uint]*uint{}
	if len(botIDs) > 0 {
		var bots []model.Bot
		_ = s.db.WithContext(ctx).Select("id, uuid, executor_node_id").Where("id IN ?", botIDs).Find(&bots).Error
		for i := range bots {
			botUUIDByID[bots[i].ID] = bots[i].UUID
			botExecByID[bots[i].ID] = bots[i].ExecutorNodeID
		}
	}
	for _, a := range actions {
		if q.BotUUID != "" && botUUIDByID[a.BotID] != q.BotUUID {
			continue
		}
		if q.ExecutorNodeID != nil {
			ex := botExecByID[a.BotID]
			if ex == nil || *ex != *q.ExecutorNodeID {
				continue
			}
		}
		cat, legacy := ClassifyBotLoadError(a.ErrorCode)
		if q.Category != "" && string(cat) != q.Category {
			continue
		}
		occurred := a.StartedAt
		if a.EndedAt != nil {
			occurred = *a.EndedAt
		}
		items = append(items, BotLoadFailureView{
			ID:             fmt.Sprintf("ar-%d", a.ID),
			RunUUID:        sess.UUID,
			BotUUID:        botUUIDByID[a.BotID],
			ExecutorNodeID: botExecByID[a.BotID],
			ActionRunID:    a.ActionRunID,
			StepID:         a.StepID,
			Category:       string(cat),
			LegacyCategory: legacy,
			ErrorCode:      a.ErrorCode,
			Message:        a.Message,
			Retryable:      isRetryableError(a.ErrorCode),
			OccurredAt:     occurred.UTC().Format(time.RFC3339Nano),
		})
	}

	// 2) 命令 checkpoint 失败
	var cks []model.BotLoadCommandCheckpoint
	cq := s.db.WithContext(ctx).Where("stress_session_id = ? AND status IN ?", sessionID,
		[]model.BotLoadCommandCheckpointStatus{
			model.BotLoadCommandCheckpointFailed,
			model.BotLoadCommandCheckpointTimedOut,
			model.BotLoadCommandCheckpointCancelled,
		})
	if q.ErrorCode != "" {
		cq = cq.Where("error_code = ?", q.ErrorCode)
	}
	if q.StepID != "" {
		cq = cq.Where("step_id = ?", q.StepID)
	}
	if q.BotUUID != "" {
		cq = cq.Where("bot_uuid = ?", q.BotUUID)
	}
	if err := cq.Order("id DESC").Limit(2000).Find(&cks).Error; err != nil {
		return nil, fmt.Errorf("查询命令失败项失败: %w", err)
	}
	for _, ck := range cks {
		code := ck.ErrorCode
		if code == "" {
			switch ck.Status {
			case model.BotLoadCommandCheckpointTimedOut:
				code = "COMMAND_DEADLINE_EXCEEDED"
			case model.BotLoadCommandCheckpointCancelled:
				code = ActionErrorCancelled
			default:
				code = "COMMAND_FAILED"
			}
		}
		cat, legacy := ClassifyBotLoadError(code)
		if q.Category != "" && string(cat) != q.Category {
			continue
		}
		occurred := ck.UpdatedAt
		if ck.EndedAt != nil {
			occurred = *ck.EndedAt
		}
		if q.From != nil && occurred.Before(q.From.UTC()) {
			continue
		}
		if q.To != nil && occurred.After(q.To.UTC()) {
			continue
		}
		items = append(items, BotLoadFailureView{
			ID:             fmt.Sprintf("ck-%d", ck.ID),
			RunUUID:        sess.UUID,
			BotUUID:        ck.BotUUID,
			ActionRunID:    ck.ActionRunID,
			StepID:         ck.StepID,
			CommandID:      ck.CommandID,
			Category:       string(cat),
			LegacyCategory: legacy,
			ErrorCode:      code,
			Message:        string(ck.Status),
			Retryable:      isRetryableError(code),
			OccurredAt:     occurred.UTC().Format(time.RFC3339Nano),
		})
	}

	total := int64(len(items))
	start := (page - 1) * pageSize
	if start > len(items) {
		start = len(items)
	}
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	return &BotLoadFailureListResult{
		Items: items[start:end], Total: total, Page: page, PageSize: pageSize,
	}, nil
}

// ListEvents 读取 append-only 运行事件（snapshotEventId 分页）。
func (s *BotLoadProjectionService) ListEvents(ctx context.Context, sessionID uint, q ListEventsQuery) (*BotLoadEventListResult, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("投影服务未初始化")
	}
	var sess model.BotStressSession
	if err := s.db.WithContext(ctx).First(&sess, sessionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBotStressSessionNotFound
		}
		return nil, err
	}
	page, pageSize := normalizeProjectionPage(q.Page, q.PageSize)

	// 冻结 snapshotEventId
	snapshotID := uint(0)
	if strings.TrimSpace(q.SnapshotEventID) != "" && q.SnapshotEventID != "0" {
		n, err := strconv.ParseUint(q.SnapshotEventID, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%w: snapshotEventId 非法", ErrBotStressSessionInvalid)
		}
		snapshotID = uint(n)
	} else {
		var maxID *uint
		_ = s.db.WithContext(ctx).Model(&model.BotLoadRunEvent{}).
			Where("stress_session_id = ?", sessionID).
			Select("MAX(id)").Scan(&maxID).Error
		if maxID != nil {
			snapshotID = *maxID
		}
	}
	if snapshotID == 0 {
		return &BotLoadEventListResult{
			Items: []BotLoadEventView{}, Total: 0, Page: page, PageSize: pageSize, SnapshotEventID: "0",
		}, nil
	}

	base := s.db.WithContext(ctx).Model(&model.BotLoadRunEvent{}).
		Where("stress_session_id = ? AND id <= ?", sessionID, snapshotID)
	if q.Type != "" {
		base = base.Where("type = ?", q.Type)
	}
	if q.EventID != "" {
		if n, err := strconv.ParseUint(q.EventID, 10, 64); err == nil {
			base = base.Where("id = ?", n)
		}
	}
	if q.ActionRunID != "" {
		base = base.Where("action_run_id = ?", q.ActionRunID)
	}
	if q.BotUUID != "" {
		base = base.Where("bot_uuid = ?", q.BotUUID)
	}
	if q.ExecutorNodeID != nil {
		base = base.Where("executor_node_id = ?", *q.ExecutorNodeID)
	}
	if q.StepID != "" {
		base = base.Where("step_id = ?", q.StepID)
	}
	if q.From != nil {
		base = base.Where("occurred_at >= ?", q.From.UTC())
	}
	if q.To != nil {
		base = base.Where("occurred_at <= ?", q.To.UTC())
	}

	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, fmt.Errorf("统计运行事件失败: %w", err)
	}
	var rows []model.BotLoadRunEvent
	offset := (page - 1) * pageSize
	if err := base.Order("id DESC").Offset(offset).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询运行事件失败: %w", err)
	}
	items := make([]BotLoadEventView, 0, len(rows))
	for _, r := range rows {
		v := BotLoadEventView{
			EventID:    strconv.FormatUint(uint64(r.ID), 10),
			RunID:      sess.ID,
			RunUUID:    r.RunUUID,
			Timestamp:  r.OccurredAt.UTC().Format(time.RFC3339Nano),
			Type:       string(r.Type),
			StageIndex: r.StageIndex,
			Payload:    json.RawMessage(r.PayloadJSON),
		}
		if r.ActionRunID != nil {
			v.ActionRunID = *r.ActionRunID
		}
		if r.BotUUID != nil {
			v.BotUUID = *r.BotUUID
		}
		if r.ExecutorNodeID != nil {
			v.ExecutorNodeID = r.ExecutorNodeID
		}
		if r.StepID != nil {
			v.StepID = *r.StepID
		}
		if r.LegacyJSON != nil && *r.LegacyJSON != "" {
			v.Legacy = json.RawMessage(*r.LegacyJSON)
		}
		if len(v.Payload) == 0 {
			v.Payload = json.RawMessage(`{}`)
		}
		items = append(items, v)
	}
	return &BotLoadEventListResult{
		Items: items, Total: total, Page: page, PageSize: pageSize,
		SnapshotEventID: strconv.FormatUint(uint64(snapshotID), 10),
	}, nil
}

func normalizeProjectionPage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}

func isRetryableError(code string) bool {
	c := strings.ToUpper(strings.TrimSpace(code))
	switch {
	case strings.Contains(c, "TIMEOUT"), strings.Contains(c, "NETWORK"),
		strings.Contains(c, "ECONN"), strings.Contains(c, "WORKER"),
		c == ActionErrorCancelled:
		return true
	default:
		return false
	}
}

// BotLoadRunBotView 与规格/前端 BotLoadRunBot 对齐。
type BotLoadRunBotView struct {
	ID             uint   `json:"id"`
	UUID           string `json:"uuid"`
	Name           string `json:"name"`
	Status         string `json:"status"`
	ExecutorNodeID *uint  `json:"executorNodeId,omitempty"`
	StepID         string `json:"stepId,omitempty"`
	CommandID      string `json:"commandId,omitempty"`
	ReconnectCount int    `json:"reconnectCount"`
	LastSeenAt     string `json:"lastSeenAt,omitempty"`
	LastError      string `json:"lastError,omitempty"`
}

// BotLoadBotListResult bots 分页。
type BotLoadBotListResult struct {
	Items    []BotLoadRunBotView `json:"items"`
	Total    int64               `json:"total"`
	Page     int                 `json:"page"`
	PageSize int                 `json:"pageSize"`
}

// ListBotsQuery bots 列表筛选。
type ListBotsQuery struct {
	Page           int
	PageSize       int
	Q              string
	Status         string
	ExecutorNodeID *uint
	StepID         string
	ErrorCode      string
}

// ListBots 投影会话关联 Bot 列表（FR-370/372）。
func (s *BotLoadProjectionService) ListBots(ctx context.Context, sessionID uint, q ListBotsQuery) (*BotLoadBotListResult, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("投影服务未初始化")
	}
	var sess model.BotStressSession
	if err := s.db.WithContext(ctx).First(&sess, sessionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBotStressSessionNotFound
		}
		return nil, err
	}
	page, pageSize := normalizeProjectionPage(q.Page, q.PageSize)
	base := s.db.WithContext(ctx).Model(&model.Bot{}).Where("stress_session_id = ?", sessionID)
	if q.Q != "" {
		like := "%" + q.Q + "%"
		base = base.Where("name LIKE ? OR uuid LIKE ?", like, like)
	}
	if q.Status != "" {
		base = base.Where("status = ?", q.Status)
	}
	if q.ExecutorNodeID != nil {
		base = base.Where("executor_node_id = ?", *q.ExecutorNodeID)
	}
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, fmt.Errorf("统计运行 Bot 失败: %w", err)
	}
	var rows []model.Bot
	if err := base.Order("id ASC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询运行 Bot 失败: %w", err)
	}
	items := make([]BotLoadRunBotView, 0, len(rows))
	for _, b := range rows {
		v := BotLoadRunBotView{
			ID: b.ID, UUID: b.UUID, Name: b.Name, Status: string(b.Status),
			ExecutorNodeID: b.ExecutorNodeID, ReconnectCount: b.ReconnectCount,
			LastError: b.LastError,
		}
		if b.LastSeenAt != nil {
			v.LastSeenAt = b.LastSeenAt.UTC().Format(time.RFC3339Nano)
		}
		items = append(items, v)
	}
	return &BotLoadBotListResult{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

// RecentEvents 读取会话最近 N 条事件（按 id DESC），供 SSE history 增量推送。
func (s *BotLoadProjectionService) RecentEvents(ctx context.Context, sessionID uint, limit int) ([]model.BotLoadRunEvent, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("投影服务未初始化")
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var rows []model.BotLoadRunEvent
	if err := s.db.WithContext(ctx).
		Where("stress_session_id = ?", sessionID).
		Order("id DESC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询最近事件失败: %w", err)
	}
	return rows, nil
}
