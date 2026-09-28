package service

import (
	"fmt"
	"log"
	"sort"
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
	// agentCallActionMaxLen 动作标识截断上限（与 agent_call_logs.action varchar(64) 对齐）。
	// Action 并非服务端常量：MCP 路径对未知工具名会拼 "mcp.tool." + toolName，而 toolName 由
	// 客户端请求体提供（可达 4MB）。不截断则 MySQL 严格模式插入报 data too long，RecordSafe 只打
	// WARN 便放行——该次调用流水整行消失，等于给调用方留下抹掉审计痕迹的口子；SQLite 不校验长度，
	// 超长值会落库并被活动视图回显。故与 Error 同样在写入口兜底。
	agentCallActionMaxLen = 64
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
	// Action 同样截断（按字节，与上面的 error 同口径）：宁可丢尾部字符，也不能让整行流水
	// 因列长超限而写不进去——流水缺失比流水不完整更难发现、也更不利于追责。
	action := r.Action
	if len(action) > agentCallActionMaxLen {
		action = action[:agentCallActionMaxLen]
	}
	client := NormalizeAgentClient(r.Client)
	row := &model.AgentCallLog{
		TokenID:    r.TokenID,
		TokenName:  r.TokenName,
		Action:     action,
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

// TokenActivity 是某 Agent Token 在窗口内的 MCP 活动聚合（ADR-096 决策 6）。
type TokenActivity struct {
	TokenID        uint
	TokenName      string
	TokenPrefix    string
	LastActivityAt time.Time
	LastAction     string
	CallCount      int64
	FailureCount   int64
	ClientIPs      []string
	Clients        map[string]int64
}

// ActivityByToken 按 Token 聚合窗口内的调用流水，按最近活动倒序。
//
// 全部使用跨 SQLite/MySQL 通用 SQL，无方言分支、不依赖 GROUP_CONCAT 或窗口函数：
//   - 计数与失败数走 `COUNT(*)` + `SUM(CASE WHEN success = 0 OR success IS NULL ...)` + `GROUP BY token_id`
//     （与 client_dist_security.go 的聚合同范式）；NULL 计入失败的理由见下方调用处注释。
//   - 索引（原注释称「命中 idx_agent_call_token_created」，不准确，已按实测修正）：本查询**无法**
//     用该复合索引做范围定位——其前导列是 token_id，而三个聚合的谓词只有 created_at >= ?。
//     SQLite 实测（5000 行）计划为 `SCAN agent_call_logs USING INDEX idx_agent_call_token_created`，
//     属整索引扫描：收益仅是按 (token_id, created_at) 的顺序扫描顺带省掉 GROUP BY 的临时 B 树，
//     并非按时间窗 seek；created_at 的单列 idx_agent_call_logs_created_at 反倒没被优化器选中
//     （显式 DROP 后计划不变，已实测）。仍可接受的理由：窗口上界由调用方约束、流水按 14 天保留
//     清理，扫描量有界；不为单条查询改索引定义（索引改动会波及全部写路径）。
//   - 「每个 Token 最近一行」用「子查询取 MAX(created_at) 再自联接回该行」的写法
//     （与 metric.go 的 latest 自联接同范式）——**不**把 MAX(created_at) 扫进 time.Time：
//     SQLite 驱动对 datetime 列返回 string，裸扫描会报 unsupported Scan
//     （同类修复见 quota_metric_source.go）；
//   - 来源分布与去重 IP 由 `GROUP BY token_id, client, ip` 的结果在 Go 侧合并，避免方言相关函数。
//
// TokenName 优先取 agent_tokens 当前值（流水里的 name 是签发时快照），TokenPrefix 只存在于该表。
func (s *AgentCallLogService) ActivityByToken(window time.Duration) ([]TokenActivity, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("agent call log service 未初始化")
	}
	if window <= 0 {
		return nil, fmt.Errorf("活动窗口必须为正: %s", window)
	}
	since := time.Now().Add(-window)

	type aggRow struct {
		TokenID   uint  `gorm:"column:token_id"`
		Cnt       int64 `gorm:"column:cnt"`
		FailCount int64 `gorm:"column:fail_cnt"`
	}
	var aggs []aggRow
	// 失败判定：success 为布尔列（SQLite/MySQL 均落 0/1），字面量 0 即失败；
	// NULL 必须**显式**计入失败——`CASE WHEN success = 0` 对 NULL 求值为 NULL，会落进 ELSE
	// 分支被当成成功，失败数偏小（审计面「全绿」）。三值逻辑下 NULL 既非成功也非失败，
	// 按审计口径从宽取「未确认为成功即失败」，宁多勿漏。
	// 前提：success 列仍是可空列（模型刻意不加 not null，理由见 model.AgentCallLog.Success），
	// 故这里不假设「非 0 即 1」；写法跨 SQLite/MySQL 通用，不依赖 IS NOT 0 等方言细节。
	err := s.db.Model(&model.AgentCallLog{}).
		Select("token_id, COUNT(*) AS cnt, COALESCE(SUM(CASE WHEN success = 0 OR success IS NULL THEN 1 ELSE 0 END), 0) AS fail_cnt").
		Where("created_at >= ?", since).
		Group("token_id").
		Scan(&aggs).Error
	if err != nil {
		return nil, fmt.Errorf("按 Token 聚合 agent 调用流水失败: %w", err)
	}
	out := make([]TokenActivity, 0, len(aggs))
	if len(aggs) == 0 {
		return out, nil
	}

	latestQuery := s.db.Model(&model.AgentCallLog{}).
		Select("token_id, MAX(created_at) AS created_at").
		Where("created_at >= ?", since).
		Group("token_id")
	var latest []model.AgentCallLog
	err = s.db.Table("agent_call_logs AS l").
		Select("l.*").
		Joins("JOIN (?) AS latest ON l.token_id = latest.token_id AND l.created_at = latest.created_at", latestQuery).
		Order("l.created_at DESC, l.id DESC").
		Scan(&latest).Error
	if err != nil {
		return nil, fmt.Errorf("查询各 Token 最近活动失败: %w", err)
	}

	type distRow struct {
		TokenID uint   `gorm:"column:token_id"`
		Client  string `gorm:"column:client"`
		IP      string `gorm:"column:ip"`
		Cnt     int64  `gorm:"column:cnt"`
	}
	var dists []distRow
	err = s.db.Model(&model.AgentCallLog{}).
		Select("token_id, client, ip, COUNT(*) AS cnt").
		Where("created_at >= ?", since).
		Group("token_id, client, ip").
		Scan(&dists).Error
	if err != nil {
		return nil, fmt.Errorf("聚合 agent 调用来源失败: %w", err)
	}

	byToken := make(map[uint]*TokenActivity, len(aggs))
	for _, a := range aggs {
		byToken[a.TokenID] = &TokenActivity{
			TokenID:      a.TokenID,
			CallCount:    a.Cnt,
			FailureCount: a.FailCount,
			Clients:      map[string]int64{},
		}
	}
	// 自联接按 MAX(created_at) 命中：同一 Token 可能有多行时间戳相同，而排序是
	// created_at DESC + id DESC——**首次**命中的才是最新那行。故每个 Token 只写一次，
	// 避免被同刻的旧行（更小 id）覆盖成较旧的 action。
	//
	// 「已写入」用显式标记判定，不能拿 LastAction != "" 当哨兵：action 列虽 not null 但**不禁止空串**，
	// 最新那行 action 恰为空串时哨兵永不置位，同刻更小 id 的旧行会继续写入，把
	// LastActivityAt / LastAction / TokenName 一并覆盖成更旧那行的值（时间与操作自相矛盾）。
	seenLatest := make(map[uint]bool, len(aggs))
	for _, row := range latest {
		it, ok := byToken[row.TokenID]
		if !ok {
			continue
		}
		if seenLatest[row.TokenID] {
			continue
		}
		seenLatest[row.TokenID] = true
		it.LastActivityAt = row.CreatedAt
		it.LastAction = row.Action
		// 兜底：Token 行已被硬删时，仍能显示签发时的名称快照。
		it.TokenName = row.TokenName
	}
	seenIP := make(map[uint]map[string]struct{}, len(aggs))
	for _, d := range dists {
		it, ok := byToken[d.TokenID]
		if !ok {
			continue
		}
		it.Clients[d.Client] += d.Cnt
		if d.IP == "" {
			continue
		}
		set := seenIP[d.TokenID]
		if set == nil {
			set = map[string]struct{}{}
			seenIP[d.TokenID] = set
		}
		if _, dup := set[d.IP]; dup {
			continue
		}
		set[d.IP] = struct{}{}
		it.ClientIPs = append(it.ClientIPs, d.IP)
	}

	ids := make([]uint, 0, len(byToken))
	for id := range byToken {
		ids = append(ids, id)
	}
	var tokens []model.AgentToken
	err = s.db.Model(&model.AgentToken{}).
		Select("id, name, token_prefix").
		Where("id IN ?", ids).
		Find(&tokens).Error
	if err != nil {
		return nil, fmt.Errorf("查询 agent token 失败: %w", err)
	}
	for _, tok := range tokens {
		if it, ok := byToken[tok.ID]; ok {
			it.TokenName = tok.Name
			it.TokenPrefix = tok.TokenPrefix
		}
	}

	for _, it := range byToken {
		sort.Strings(it.ClientIPs)
		out = append(out, *it)
	}
	// 最近活动倒序；同一时刻以 TokenID 降序兜底，保证输出稳定（前端与测试可断言）。
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastActivityAt.Equal(out[j].LastActivityAt) {
			return out[i].TokenID > out[j].TokenID
		}
		return out[i].LastActivityAt.After(out[j].LastActivityAt)
	})
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
