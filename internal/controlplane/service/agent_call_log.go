package service

import (
	"fmt"
	"log"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// Agent 调用客户端约定值（FR-390）。
const (
	AgentClientMCP     = "mcp"
	AgentClientJmagent = "jmagent"
	AgentClientCurl    = "curl"
	AgentClientUnknown = "unknown"

	// 默认流水保留天数；可经 NewAgentCallLogServiceWithRetention 覆盖。
	DefaultAgentCallLogRetentionDays = 14
	// agentCallErrorMaxLen 失败信息截断上限（与 audit 对齐）。
	agentCallErrorMaxLen = 512
	// agentClientHeaderMaxLen X-JM-Agent-Client 原始值长度上限，防注入/刷库。
	agentClientHeaderMaxLen = 32
)

// 允许的 client 字面量（未知归 unknown）。
var agentClientAllow = map[string]struct{}{
	AgentClientMCP:     {},
	AgentClientJmagent: {},
	AgentClientCurl:    {},
	AgentClientUnknown: {},
}

// AgentCallLogService Agent 调用流水（FR-390）。
// 供 Ops 中间件/handler 与日后 MCP 路径调用 Record；失败只打 WARN 不阻断主请求。
type AgentCallLogService struct {
	db            *gorm.DB
	retentionDays int
}

// NewAgentCallLogService 创建服务（默认保留 14 天）。
func NewAgentCallLogService(db *gorm.DB) *AgentCallLogService {
	return NewAgentCallLogServiceWithRetention(db, DefaultAgentCallLogRetentionDays)
}

// NewAgentCallLogServiceWithRetention 创建服务并指定保留天数（<=0 则用默认 14）。
func NewAgentCallLogServiceWithRetention(db *gorm.DB, retentionDays int) *AgentCallLogService {
	if retentionDays <= 0 {
		retentionDays = DefaultAgentCallLogRetentionDays
	}
	return &AgentCallLogService{db: db, retentionDays: retentionDays}
}

// RetentionDays 当前保留天数。
func (s *AgentCallLogService) RetentionDays() int {
	if s == nil {
		return DefaultAgentCallLogRetentionDays
	}
	return s.retentionDays
}

// AgentCallRecord 写入一条流水所需字段。
type AgentCallRecord struct {
	TokenID    uint
	TokenName  string
	Action     string
	Capability string
	Client     string
	Transport  string
	TargetType string
	TargetID   string
	Success    bool
	Error      string
	LatencyMs  uint
	IP         string
}

// Record 写入一条调用流水。失败返回 error；调用方应只打 WARN 不阻断。
func (s *AgentCallLogService) Record(r AgentCallRecord) error {
	if s == nil || s.db == nil {
		return nil
	}
	errMsg := r.Error
	if len(errMsg) > agentCallErrorMaxLen {
		errMsg = errMsg[:agentCallErrorMaxLen]
	}
	// 禁止把疑似 token 明文写入 error（jmat_ 前缀粗滤）。
	if strings.Contains(errMsg, "jmat_") {
		errMsg = "[已脱敏：含 token 形态字符串]"
	}
	client := NormalizeAgentClient(r.Client)
	row := &model.AgentCallLog{
		TokenID:    r.TokenID,
		TokenName:  r.TokenName,
		Action:     r.Action,
		Capability: r.Capability,
		Client:     client,
		Transport:  r.Transport,
		TargetType: r.TargetType,
		TargetID:   r.TargetID,
		Success:    r.Success,
		Error:      errMsg,
		LatencyMs:  r.LatencyMs,
		IP:         r.IP,
		CreatedAt:  time.Now(),
	}
	// Select 全字段：避免 GORM Create 省略 bool 零值导致 success=false 未落库。
	if err := s.db.Select(
		"TokenID", "TokenName", "Action", "Capability", "Client", "Transport",
		"TargetType", "TargetID", "Success", "Error", "LatencyMs", "IP", "CreatedAt",
	).Create(row).Error; err != nil {
		return fmt.Errorf("记录 agent 调用流水失败: %w", err)
	}
	return nil
}

// RecordSafe 写入流水；失败只 WARN，永不返回 error（handler 用）。
func (s *AgentCallLogService) RecordSafe(r AgentCallRecord) {
	if err := s.Record(r); err != nil {
		log.Printf("[WARN] agent 调用流水写入失败: %v", err)
	}
}

// AgentCallLogFilter 查询过滤条件。
type AgentCallLogFilter struct {
	TokenID  *uint
	Action   *string
	Client   *string
	Success  *bool
	From     *time.Time
	To       *time.Time
	Page     int
	PageSize int
}

// AgentCallLogPage 分页结果。
type AgentCallLogPage struct {
	Items    []model.AgentCallLog `json:"items"`
	Total    int64                `json:"total"`
	Page     int                  `json:"page"`
	PageSize int                  `json:"pageSize"`
}

// List 按条件分页查询，稳定排序 created_at DESC, id DESC。
func (s *AgentCallLogService) List(filter AgentCallLogFilter) (AgentCallLogPage, error) {
	if s == nil || s.db == nil {
		return AgentCallLogPage{}, fmt.Errorf("agent call log service 未初始化")
	}
	page, pageSize := normalizeAgentCallPage(filter.Page, filter.PageSize)
	q := s.query(filter)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return AgentCallLogPage{}, fmt.Errorf("统计 agent 调用流水失败: %w", err)
	}
	var items []model.AgentCallLog
	err := s.query(filter).
		Order("created_at DESC, id DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&items).Error
	if err != nil {
		return AgentCallLogPage{}, fmt.Errorf("查询 agent 调用流水失败: %w", err)
	}
	return AgentCallLogPage{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

// Count24h 统计某 token 最近 24 小时调用次数。
func (s *AgentCallLogService) Count24h(tokenID uint) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	since := time.Now().Add(-24 * time.Hour)
	var n int64
	err := s.db.Model(&model.AgentCallLog{}).
		Where("token_id = ? AND created_at >= ?", tokenID, since).
		Count(&n).Error
	if err != nil {
		return 0, fmt.Errorf("统计 token 24h 调用失败: %w", err)
	}
	return n, nil
}

// Count24hMap 批量统计多个 token 的 24h 调用次数（key=tokenID）。
func (s *AgentCallLogService) Count24hMap(tokenIDs []uint) (map[uint]int64, error) {
	out := make(map[uint]int64, len(tokenIDs))
	if s == nil || s.db == nil || len(tokenIDs) == 0 {
		return out, nil
	}
	since := time.Now().Add(-24 * time.Hour)
	type row struct {
		TokenID uint  `gorm:"column:token_id"`
		Cnt     int64 `gorm:"column:cnt"`
	}
	var rows []row
	err := s.db.Model(&model.AgentCallLog{}).
		Select("token_id, COUNT(*) AS cnt").
		Where("token_id IN ? AND created_at >= ?", tokenIDs, since).
		Group("token_id").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("批量统计 token 24h 调用失败: %w", err)
	}
	for _, r := range rows {
		out[r.TokenID] = r.Cnt
	}
	return out, nil
}

// PurgeExpired 删除超过保留窗口的流水；返回删除行数。
func (s *AgentCallLogService) PurgeExpired() (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	cutoff := time.Now().Add(-time.Duration(s.retentionDays) * 24 * time.Hour)
	res := s.db.Where("created_at < ?", cutoff).Delete(&model.AgentCallLog{})
	if res.Error != nil {
		return 0, fmt.Errorf("清理过期 agent 调用流水失败: %w", res.Error)
	}
	return res.RowsAffected, nil
}

func (s *AgentCallLogService) query(filter AgentCallLogFilter) *gorm.DB {
	q := s.db.Model(&model.AgentCallLog{})
	if filter.TokenID != nil {
		q = q.Where("token_id = ?", *filter.TokenID)
	}
	if filter.Action != nil && *filter.Action != "" {
		q = q.Where("action = ?", *filter.Action)
	}
	if filter.Client != nil && *filter.Client != "" {
		q = q.Where("client = ?", *filter.Client)
	}
	if filter.Success != nil {
		q = q.Where("success = ?", *filter.Success)
	}
	if filter.From != nil {
		q = q.Where("created_at >= ?", *filter.From)
	}
	if filter.To != nil {
		q = q.Where("created_at <= ?", *filter.To)
	}
	return q
}

func normalizeAgentCallPage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	return page, pageSize
}

// NormalizeAgentClient 解析 X-JM-Agent-Client：长度限制 + 白名单，否则 unknown。
func NormalizeAgentClient(raw string) string {
	v := strings.TrimSpace(strings.ToLower(raw))
	if v == "" {
		return AgentClientUnknown
	}
	if len(v) > agentClientHeaderMaxLen {
		return AgentClientUnknown
	}
	// 仅允许字母数字与 -_
	for _, c := range v {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			continue
		}
		return AgentClientUnknown
	}
	if _, ok := agentClientAllow[v]; !ok {
		return AgentClientUnknown
	}
	return v
}
