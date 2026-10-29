package service

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 对外只读统计契约的服务层实现。
//
// 边界（最重要，见 docs/specs/external-stats-contract/spec.md §2）：
// **只出通道与基础设施维度，不出任何业务指标**。
// 本文件不得出现任何需要理解 domain 语义的字段；即使将来要按 domain 分组，
// 也必须在文档标注「domain 为不透明标签，平台不解释其语义」。

// 契约上限常量（spec §4.2 / §4.3 / §5）。
const (
	// StatsMaxPageSize 分页上限。超过返回 PAGE_SIZE_EXCEEDED，**刻意不静默截断**——
	// 静默截断会让调用方以为拿到了全部数据。
	StatsMaxPageSize = 200
	// StatsDefaultPageSize 分页缺省值。
	StatsDefaultPageSize = 100
	// StatsMetricsMaxRange 指标接口（instances / nodes）时间窗上限。
	StatsMetricsMaxRange = 30 * 24 * time.Hour
	// StatsMetricsMaxRangeLabel 是上方上限的对外可读形式（TIME_RANGE_EXCEEDED 的 maxRange）。
	StatsMetricsMaxRangeLabel = "720h"
	// StatsLogsMaxRange 日志检索时间窗上限（日志量大，过宽会拖慢查询）。
	StatsLogsMaxRange = 7 * 24 * time.Hour
	// StatsLogsMaxRangeLabel 是上方上限的对外可读形式。
	StatsLogsMaxRangeLabel = "168h"
	// StatsMetricsDefaultRange 指标接口时间窗缺省值。
	StatsMetricsDefaultRange = time.Hour
	// StatsLogsDefaultRange 日志接口时间窗缺省值。
	StatsLogsDefaultRange = time.Hour
	// StatsRawMaxRange 原始样本档允许的最大时间窗（spec §5：raw 表只用于
	// `resolution=raw` 且时间窗 < 1 小时）。
	StatsRawMaxRange = time.Hour
	// StatsRawMaxRangeLabel 是上方上限的对外可读形式。
	StatsRawMaxRangeLabel = "1h"
	// StatsCrashMaxDays 崩溃趋势窗口上限（天）。
	StatsCrashMaxDays = 365
	// StatsCrashDefaultDays 崩溃趋势窗口缺省值（天）。
	StatsCrashDefaultDays = 30
)

// 契约错误码（与 docs/openapi/components/errors.yaml 对齐）。
const (
	// StatsCodePageSizeExceeded pageSize 超过上限。
	StatsCodePageSizeExceeded = "PAGE_SIZE_EXCEEDED"
	// StatsCodeTimeRangeExceeded 时间窗超过该接口上限。
	StatsCodeTimeRangeExceeded = "TIME_RANGE_EXCEEDED"
	// StatsCodeInvalidArgument 入参形状非法（枚举外取值、解析失败等）。
	StatsCodeInvalidArgument = "INVALID_ARGUMENT"
	// StatsCodeRateLimited 触发限流。
	StatsCodeRateLimited = "RATE_LIMITED"
	// StatsCodeUnauthorized 认证失败（Token 缺失、无效或已过期）。
	StatsCodeUnauthorized = "UNAUTHORIZED"
	// StatsCodeForbidden 权限不足（Token scope 不覆盖目标资源）。
	StatsCodeForbidden = "FORBIDDEN"
	// StatsCodeInternal 内部错误（细节只进日志，不进响应体）。
	StatsCodeInternal = "INTERNAL_ERROR"
)

// StatsError 契约错误。Code 供调用方分支处理，附加字段按错误码填充。
type StatsError struct {
	// Code 稳定错误码（见上方常量）。
	Code string
	// Message 人可读描述，仅供展示与排障。
	Message string
	// MaxPageSize 仅在 PAGE_SIZE_EXCEEDED 时有意义。
	MaxPageSize int
	// MaxRange 仅在 TIME_RANGE_EXCEEDED 时有意义。
	MaxRange string
}

// Error 实现 error 接口。
func (e *StatsError) Error() string {
	if e == nil {
		return ""
	}
	return e.Code + ": " + e.Message
}

// ── 响应结构（严格对齐 docs/openapi/components/schemas.yaml）────────────

// StatsNodeCounts 节点计数。
type StatsNodeCounts struct {
	Total       int `json:"total"`
	Online      int `json:"online"`
	Maintenance int `json:"maintenance"`
}

// StatsInstanceCounts 实例计数。
type StatsInstanceCounts struct {
	Total   int `json:"total"`
	Running int `json:"running"`
	Stopped int `json:"stopped"`
	Crashed int `json:"crashed"`
}

// StatsResources 资源水位。
type StatsResources struct {
	CPUCores      int     `json:"cpuCores"`
	CPUUsagePct   float64 `json:"cpuUsagePct"`
	MemoryTotalMb int64   `json:"memoryTotalMb"`
	MemoryUsedMb  int64   `json:"memoryUsedMb"`
}

// StatsProbeCounts 探针连接情况。
type StatsProbeCounts struct {
	Connected    int `json:"connected"`
	Disconnected int `json:"disconnected"`
}

// StatsOverview 集群总览（无时间窗，返回当前快照）。
type StatsOverview struct {
	Nodes     StatsNodeCounts     `json:"nodes"`
	Instances StatsInstanceCounts `json:"instances"`
	Resources StatsResources      `json:"resources"`
	Probes    StatsProbeCounts    `json:"probes"`
}

// StatsMetricPoint 曲线上的一个点。
type StatsMetricPoint struct {
	T time.Time `json:"t"`
	V float64   `json:"v"`
}

// StatsMetricSeries 一条指标序列。instanceId / nodeId 按维度择一填充。
type StatsMetricSeries struct {
	InstanceID uint               `json:"instanceId,omitempty"`
	NodeID     uint               `json:"nodeId,omitempty"`
	Metric     string             `json:"metric"`
	Points     []StatsMetricPoint `json:"points"`
}

// StatsPagination 分页信息。
type StatsPagination struct {
	Page     int `json:"page"`
	PageSize int `json:"pageSize"`
	Total    int `json:"total"`
}

// StatsMetricSeriesResponse 指标时序响应。
type StatsMetricSeriesResponse struct {
	Series     []StatsMetricSeries `json:"series"`
	Pagination StatsPagination     `json:"pagination"`
}

// StatsLogEntry 日志条目（不含玩家敏感信息，脱敏由采集侧负责）。
type StatsLogEntry struct {
	Timestamp  time.Time `json:"timestamp"`
	Level      string    `json:"level"`
	InstanceID uint      `json:"instanceId"`
	NodeID     uint      `json:"nodeId"`
	Message    string    `json:"message"`
	Source     string    `json:"source"`
}

// StatsLogQueryResponse 日志检索响应。
type StatsLogQueryResponse struct {
	Entries    []StatsLogEntry `json:"entries"`
	Pagination StatsPagination `json:"pagination"`
}

// StatsCrashDay 按天的崩溃计数。
type StatsCrashDay struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

// StatsCrashReason 按根因分类的崩溃计数。
type StatsCrashReason struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// StatsCrashTrendResponse 崩溃趋势响应。
type StatsCrashTrendResponse struct {
	ByDay    []StatsCrashDay    `json:"byDay"`
	ByReason []StatsCrashReason `json:"byReason"`
}

// StatsChangeSummary 在途变更摘要。**不含配置内容。**
type StatsChangeSummary struct {
	ID           uint       `json:"id"`
	Title        string     `json:"title"`
	Status       string     `json:"status"`
	TotalTargets int        `json:"totalTargets"`
	Succeeded    int        `json:"succeeded"`
	Failed       int        `json:"failed"`
	Deferred     int        `json:"deferred"`
	CreatedAt    time.Time  `json:"createdAt"`
	StartedAt    *time.Time `json:"startedAt,omitempty"`
}

// StatsChangeListResponse 在途变更列表响应。
type StatsChangeListResponse struct {
	Changes    []StatsChangeSummary `json:"changes"`
	Pagination StatsPagination      `json:"pagination"`
}

// ── 可见范围 ──────────────────────────────────────────────────────────

// StatsScope 调用者可见范围。
//
// Scoped=false 表示不收敛（平台管理员）；Scoped=true 时只返回
// InstanceIDs / NodeIDs 内的数据。**空的 InstanceIDs 表示「什么都看不到」**，
// 绝不能退化成「不加过滤」——那正是越权读。
//
// AllNodes 用于「持 node.read 但只持部分实例读权限」的账号：实例收敛仍生效，
// 节点维度不收敛。它与 `NodeIDs 为空` 是两种不同语义，故必须显式区分。
type StatsScope struct {
	// AllowAll 显式声明「不受限」（平台管理员 / 内部调用）。
	//
	// **零值必须是「什么都看不到」，而不是「什么都看得到」。**
	// 此前用的是 `Scoped bool`：`!Scoped` 即不收敛，于是 `StatsScope{}`（零值、
	// 以及任何忘记构造范围的调用方）天然获得全平台可见性——权限原语用 fail-open
	// 默认值是危险的：漏一次赋值就是一次数据越界，而且**不会有任何报错**。
	// 改成显式的 AllowAll 之后，忘记赋值只会让用户看不到数据（可发现的故障），
	// 而不是看到别人的数据（不可发现的越界）。
	AllowAll bool

	Scoped      bool
	InstanceIDs []uint
	NodeIDs     []uint
	AllNodes    bool
}

// Unscoped 返回不收敛的范围（平台管理员 / 内部调用）。
//
// 名字保留是为了不动既有调用点；语义现在由 AllowAll 表达。
func Unscoped() StatsScope { return StatsScope{AllowAll: true, AllNodes: true} }

// allowsAnyInstance 报告该范围是否至少能看到一个实例。
//
// 零值 → false（看不到任何实例）。
func (s StatsScope) allowsAnyInstance() bool {
	if s.AllowAll {
		return true
	}
	return len(s.InstanceIDs) > 0
}

// allowsAllNodes 报告节点维度是否不收敛。
//
// 零值 → false（节点维度也收敛到空）。
func (s StatsScope) allowsAllNodes() bool {
	if s.AllowAll {
		return true
	}
	return s.AllNodes
}

// allowsNode 报告单个节点是否可见。
func (s StatsScope) allowsNode(id uint) bool {
	if s.allowsAllNodes() {
		return true
	}
	for _, n := range s.NodeIDs {
		if n == id {
			return true
		}
	}
	return false
}

// ── 查询参数 ──────────────────────────────────────────────────────────

// StatsMetricQuery 指标时序查询参数（instances / nodes 共用）。
type StatsMetricQuery struct {
	// InstanceIDs 显式指定的实例 ID（instances 接口）；空表示该范围内全部。
	InstanceIDs []uint
	// NodeIDs 显式指定的节点 ID（nodes 接口）；空表示该范围内全部。
	NodeIDs []uint
	// From / To 查询区间（已归一为 UTC）。
	From time.Time
	To   time.Time
	// Resolution 归一后的实际档位（raw / 5m / 1h）。
	Resolution StatsResolution
	Page       int
	PageSize   int
}

// StatsLogQuery 日志检索参数。
type StatsLogQuery struct {
	InstanceID *uint
	NodeID     *uint
	Level      string
	Keyword    string
	From       time.Time
	To         time.Time
	Page       int
	PageSize   int
}

// StatsCrashQuery 崩溃趋势查询参数。
type StatsCrashQuery struct {
	// InstanceID 非 0 时按实例过滤。
	InstanceID uint
	Days       int
}

// StatsChangeQuery 在途变更查询参数。
type StatsChangeQuery struct {
	// Statuses 会话状态集合（已由 ParseStatsChangeStatuses 映射）；空表示缺省在途状态。
	Statuses []model.RollingState
	Page     int
	PageSize int
}

// StatsResolution 归一后的预聚合档位。
//
// 契约（common.yaml#/Resolution）声明的枚举是 raw/15s/1m/5m/1h，但存储层只有
// 原始表 + 5m/1h 两档预聚合。因此 15s/1m 统一落到 5m 档（见 NormalizeStatsResolution）
// ——**绝不回落原始表**，因为「除 raw 外不许打 raw 表」是 spec §5 的硬约束。
type StatsResolution string

const (
	// StatsResolutionRaw 原始样本档（仅允许时间窗 < 1h）。
	StatsResolutionRaw StatsResolution = "raw"
	// StatsResolution5m 5 分钟预聚合档。
	StatsResolution5m StatsResolution = "5m"
	// StatsResolution1h 1 小时预聚合档。
	StatsResolution1h StatsResolution = "1h"
)

// NormalizeStatsResolution 把契约 resolution 归一为实际查询档位。
//
// span 用于判定 raw 档的时间窗约束：`raw` 且 span >= 1h 时返回 TIME_RANGE_EXCEEDED
// （maxRange=1h），而不是静默降级到预聚合档——降级会让调用方以为拿到的是原始采样。
func NormalizeStatsResolution(requested string, span time.Duration) (StatsResolution, *StatsError) {
	switch strings.ToLower(strings.TrimSpace(requested)) {
	case "", "5m", "1m", "15s":
		// 15s / 1m 无独立预聚合档，统一落到 5m（存储层最小预聚合粒度）。
		return StatsResolution5m, nil
	case "1h":
		return StatsResolution1h, nil
	case "raw":
		if span >= StatsRawMaxRange {
			return "", &StatsError{
				Code:     StatsCodeTimeRangeExceeded,
				Message:  "resolution=raw 仅允许查询 1 小时以内的区间",
				MaxRange: StatsRawMaxRangeLabel,
			}
		}
		return StatsResolutionRaw, nil
	default:
		return "", &StatsError{
			Code:    StatsCodeInvalidArgument,
			Message: "resolution 必须为 raw、15s、1m、5m 或 1h",
		}
	}
}

// NormalizeStatsPagination 归一并校验分页参数。pageSize 超上限返回 PAGE_SIZE_EXCEEDED。
func NormalizeStatsPagination(page, pageSize int) (int, int, *StatsError) {
	if page <= 0 {
		page = 1
	}
	if pageSize == 0 {
		pageSize = StatsDefaultPageSize
	}
	if pageSize < 0 {
		return 0, 0, &StatsError{Code: StatsCodeInvalidArgument, Message: "pageSize 必须为正整数"}
	}
	if pageSize > StatsMaxPageSize {
		return 0, 0, &StatsError{
			Code:        StatsCodePageSizeExceeded,
			Message:     fmt.Sprintf("pageSize 超过上限 %d", StatsMaxPageSize),
			MaxPageSize: StatsMaxPageSize,
		}
	}
	return page, pageSize, nil
}

// NormalizeStatsTimeRange 归一时间窗：缺省回退 defaultSpan，跨度超过 max 返回 TIME_RANGE_EXCEEDED。
func NormalizeStatsTimeRange(from, to *time.Time, defaultSpan, maxSpan time.Duration, maxLabel string, now time.Time) (time.Time, time.Time, *StatsError) {
	end := now.UTC()
	if to != nil {
		end = to.UTC()
	}
	start := end.Add(-defaultSpan)
	if from != nil {
		start = from.UTC()
	}
	if !end.After(start) {
		return time.Time{}, time.Time{}, &StatsError{
			Code:    StatsCodeInvalidArgument,
			Message: "to 必须晚于 from",
		}
	}
	if end.Sub(start) > maxSpan {
		return time.Time{}, time.Time{}, &StatsError{
			Code:     StatsCodeTimeRangeExceeded,
			Message:  fmt.Sprintf("查询时间窗超过该接口上限 %s", maxLabel),
			MaxRange: maxLabel,
		}
	}
	return start, end, nil
}

// NormalizeStatsCrashDays 归一崩溃趋势窗口天数。超出 [1,365] 返回 TIME_RANGE_EXCEEDED，
// **不静默收敛**（与 CrashSnapshotService.TrendByInstance 的内部收敛语义不同：
// 对外契约要求明确拒绝）。
func NormalizeStatsCrashDays(days int) (int, *StatsError) {
	if days == 0 {
		return StatsCrashDefaultDays, nil
	}
	if days < 1 {
		return 0, &StatsError{Code: StatsCodeInvalidArgument, Message: "days 必须 >= 1"}
	}
	if days > StatsCrashMaxDays {
		return 0, &StatsError{
			Code:     StatsCodeTimeRangeExceeded,
			Message:  fmt.Sprintf("统计窗口超过上限 %d 天", StatsCrashMaxDays),
			MaxRange: fmt.Sprintf("%dh", StatsCrashMaxDays*24),
		}
	}
	return days, nil
}

// ── 服务 ──────────────────────────────────────────────────────────────

// StatsService 对外只读统计契约。
type StatsService struct {
	db *gorm.DB
	// probeConnected 探针连接判定。nil 时一律视为未连入（仅影响 overview 的 probes 计数）。
	probeConnected func(instanceUUID string) bool
	// limiter 每 token 每分钟限流（spec §4.4）。
	limiter *StatsRateLimiter
}

// NewStatsService 创建统计服务。
func NewStatsService(db *gorm.DB) *StatsService {
	return &StatsService{db: db, limiter: NewStatsRateLimiter(StatsRateLimitPerMinute, time.Minute)}
}

// SetProbeConnected 注入探针连接判定（生产由 PlayerEventService.IsProbeConnected 提供）。
func (s *StatsService) SetProbeConnected(fn func(instanceUUID string) bool) {
	s.probeConnected = fn
}

// Limiter 暴露限流器，供路由层在鉴权后按 token 计费。
func (s *StatsService) Limiter() *StatsRateLimiter { return s.limiter }

// StatsScopeForAgent 从 Agent 主体解析可见范围。
//
// V1：仅显式实例 ID；V2：显式实例 ∪ 授权节点上的当前实例（与 ListAccessibleInstances 同口径）。
// 节点范围取「可见实例所在节点 ∪ 显式授权节点」，且显式授权节点只在 V2 生效。
func (s *StatsService) StatsScopeForAgent(p *AgentPrincipal) (StatsScope, error) {
	if p == nil {
		return StatsScope{Scoped: true}, nil
	}
	q := s.db.Model(&model.Instance{}).Select("id", "node_id")
	switch {
	case p.PolicyVersion == AgentPolicyVersionV2:
		hasInst := len(p.ScopedInstanceIDs) > 0
		hasNode := len(p.ScopedNodeIDs) > 0
		switch {
		case hasInst && hasNode:
			q = q.Where("id IN ? OR node_id IN ?", p.ScopedInstanceIDs, p.ScopedNodeIDs)
		case hasInst:
			q = q.Where("id IN ?", p.ScopedInstanceIDs)
		case hasNode:
			q = q.Where("node_id IN ?", p.ScopedNodeIDs)
		default:
			return StatsScope{Scoped: true, InstanceIDs: []uint{}, NodeIDs: []uint{}}, nil
		}
	default:
		if len(p.ScopedInstanceIDs) == 0 {
			return StatsScope{Scoped: true, InstanceIDs: []uint{}, NodeIDs: []uint{}}, nil
		}
		q = q.Where("id IN ?", p.ScopedInstanceIDs)
	}
	var rows []struct {
		ID     uint
		NodeID uint
	}
	if err := q.Find(&rows).Error; err != nil {
		return StatsScope{}, fmt.Errorf("查询 Agent 可见实例失败: %w", err)
	}
	instIDs := make([]uint, 0, len(rows))
	nodeSet := make(map[uint]struct{}, len(rows))
	for _, r := range rows {
		instIDs = append(instIDs, r.ID)
		nodeSet[r.NodeID] = struct{}{}
	}
	if p.PolicyVersion == AgentPolicyVersionV2 {
		for _, id := range p.ScopedNodeIDs {
			nodeSet[id] = struct{}{}
		}
	}
	nodeIDs := make([]uint, 0, len(nodeSet))
	for id := range nodeSet {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Slice(nodeIDs, func(i, j int) bool { return nodeIDs[i] < nodeIDs[j] })
	return StatsScope{Scoped: true, InstanceIDs: instIDs, NodeIDs: nodeIDs}, nil
}

// StatsScopeForUser 从用户授权上下文解析可见范围。
//
// 平台管理员不收敛；其余用户收敛到可访问实例集合，节点范围取
// 「可见实例所在节点 ∪ 平台级节点读权限」——节点指标本身是宿主机资源而非实例数据，
// 但把全部节点暴露给仅持部分实例读权限的账号会让「哪些机器存在」变成可枚举信息，
// 故默认仍以可见实例所在节点为界；持 node.read / node.manage 的用户可看全部节点。
func (s *StatsService) StatsScopeForUser(access *UserAccess, authz *AuthzService, canReadAllNodes bool) (StatsScope, error) {
	if access == nil {
		return StatsScope{}, fmt.Errorf("缺少授权上下文")
	}
	if access.IsPlatformAdmin {
		return Unscoped(), nil
	}
	if authz == nil {
		return StatsScope{Scoped: true, InstanceIDs: []uint{}, NodeIDs: []uint{}}, nil
	}
	ids, scoped, err := authz.AccessibleInstanceIDs(access)
	if err != nil {
		return StatsScope{}, err
	}
	if !scoped {
		return Unscoped(), nil
	}
	scope := StatsScope{Scoped: true, InstanceIDs: ids, NodeIDs: []uint{}}
	if canReadAllNodes {
		scope.AllNodes = true
		return scope, nil
	}
	if len(ids) == 0 {
		return scope, nil
	}
	var rows []struct{ NodeID uint }
	if err := s.db.Model(&model.Instance{}).Select("node_id").Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return StatsScope{}, fmt.Errorf("查询可见节点失败: %w", err)
	}
	nodeSet := map[uint]struct{}{}
	for _, r := range rows {
		nodeSet[r.NodeID] = struct{}{}
	}
	nodeIDs := make([]uint, 0, len(nodeSet))
	for id := range nodeSet {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Slice(nodeIDs, func(i, j int) bool { return nodeIDs[i] < nodeIDs[j] })
	scope.NodeIDs = nodeIDs
	return scope, nil
}

// Overview 返回集群总览快照（无时间窗）。
func (s *StatsService) Overview(scope StatsScope) (StatsOverview, error) {
	out := StatsOverview{}

	// 节点维度：Scoped 时收敛到可见节点集合。
	nq := s.db.Model(&model.Node{})
	if !scope.allowsAllNodes() {
		if len(scope.NodeIDs) == 0 {
			nq = nq.Where("1 = 0")
		} else {
			nq = nq.Where("id IN ?", scope.NodeIDs)
		}
	}
	var nodes []model.Node
	if err := nq.Select("id", "status", "maintenance", "cpu_cores", "cpu_usage", "memory_mb", "memory_used_mb").
		Find(&nodes).Error; err != nil {
		return out, fmt.Errorf("查询节点总览失败: %w", err)
	}
	var cpuSum float64
	var cpuCount int
	for _, n := range nodes {
		out.Nodes.Total++
		if n.Status == model.NodeStatusOnline {
			out.Nodes.Online++
		}
		if n.Maintenance {
			out.Nodes.Maintenance++
		}
		// 资源水位只统计在线节点：离线节点的读数是陈旧值，计入会虚高容量。
		if n.Status != model.NodeStatusOnline {
			continue
		}
		out.Resources.CPUCores += n.CPUCores
		out.Resources.MemoryTotalMb += n.MemoryMB
		out.Resources.MemoryUsedMb += n.MemoryUsedMB
		cpuSum += float64(n.CPUUsage)
		cpuCount++
	}
	if cpuCount > 0 {
		out.Resources.CPUUsagePct = cpuSum / float64(cpuCount)
	}

	// 实例维度。
	iq := s.db.Model(&model.Instance{})
	if scope.Scoped {
		if len(scope.InstanceIDs) == 0 {
			iq = iq.Where("1 = 0")
		} else {
			iq = iq.Where("id IN ?", scope.InstanceIDs)
		}
	}
	var instances []model.Instance
	if err := iq.Select("id", "uuid", "status").Find(&instances).Error; err != nil {
		return out, fmt.Errorf("查询实例总览失败: %w", err)
	}
	for _, inst := range instances {
		out.Instances.Total++
		switch inst.Status {
		case model.InstanceStatusRunning:
			out.Instances.Running++
		case model.InstanceStatusStopped:
			out.Instances.Stopped++
		case model.InstanceStatusCrashed:
			out.Instances.Crashed++
		}
		if s.probeConnected != nil && s.probeConnected(inst.UUID) {
			out.Probes.Connected++
		} else {
			out.Probes.Disconnected++
		}
	}
	return out, nil
}

// QueryInstanceSeries 查询实例资源指标时序。
func (s *StatsService) QueryInstanceSeries(q StatsMetricQuery, scope StatsScope) (StatsMetricSeriesResponse, error) {
	return s.querySeries(model.MetricScopeInstance, q, scope)
}

// QueryNodeSeries 查询节点资源指标时序。
func (s *StatsService) QueryNodeSeries(q StatsMetricQuery, scope StatsScope) (StatsMetricSeriesResponse, error) {
	return s.querySeries(model.MetricScopeNode, q, scope)
}

// querySeries 是实例/节点两个维度的共同实现。
//
// 数据来源严格按档位分流（spec §5）：
//   - raw  → metric_sample_raws（仅在 span < 1h 时可达，见 NormalizeStatsResolution）
//   - 5m   → metric_rollup_5ms
//   - 1h   → metric_rollup_1hs
//
// 分页作用在**序列**上（total = 序列总数），只对当前页的序列查点位，
// 因此单页的查询条数与 pageSize 相关、与实例总数无关（无 N+1 点查询）。
func (s *StatsService) querySeries(kind model.MetricScope, q StatsMetricQuery, scope StatsScope) (StatsMetricSeriesResponse, error) {
	out := StatsMetricSeriesResponse{
		Series:     []StatsMetricSeries{},
		Pagination: StatsPagination{Page: q.Page, PageSize: q.PageSize},
	}
	if !scope.allowsAnyInstance() {
		return out, nil
	}

	// 1) 解析目标维度到序列过滤条件。instance 维度按 instance_id（UUID），node 维度按 node_uuid。
	var (
		instanceUUIDs []string
		nodeUUIDs     []string
	)
	allowedInstIDs := map[uint]struct{}{}
	if scope.Scoped {
		for _, id := range scope.InstanceIDs {
			allowedInstIDs[id] = struct{}{}
		}
	}
	switch kind {
	case model.MetricScopeInstance:
		ids := q.InstanceIDs
		switch {
		case len(ids) == 0:
			// 缺省范围：不收敛时不过滤；收敛时取可见实例集合。
			if scope.Scoped {
				ids = scope.InstanceIDs
				if len(ids) == 0 {
					return out, nil
				}
			}
		case scope.Scoped:
			// 显式目标必须落在可见集合内，否则视为不可见（不泄露存在性）。
			filtered := make([]uint, 0, len(ids))
			for _, id := range ids {
				if _, ok := allowedInstIDs[id]; ok {
					filtered = append(filtered, id)
				}
			}
			if len(filtered) == 0 {
				return out, nil
			}
			ids = filtered
		}
		if len(ids) > 0 {
			var err error
			instanceUUIDs, err = s.instanceUUIDs(ids)
			if err != nil {
				return out, err
			}
			if len(instanceUUIDs) == 0 {
				return out, nil
			}
		}
	default: // node
		nodeIDs := q.NodeIDs
		switch {
		case len(nodeIDs) == 0:
			// 缺省范围：不收敛时不过滤；收敛时取可见节点集合。
			if !scope.allowsAllNodes() {
				nodeIDs = scope.NodeIDs
				if len(nodeIDs) == 0 {
					return out, nil
				}
			}
		case !scope.allowsAllNodes():
			// 显式目标必须落在可见节点集合内。
			filtered := make([]uint, 0, len(nodeIDs))
			for _, id := range nodeIDs {
				if scope.allowsNode(id) {
					filtered = append(filtered, id)
				}
			}
			if len(filtered) == 0 {
				return out, nil
			}
			nodeIDs = filtered
		}
		if len(nodeIDs) > 0 {
			var err error
			nodeUUIDs, err = s.nodeUUIDs(nodeIDs)
			if err != nil {
				return out, err
			}
			if len(nodeUUIDs) == 0 {
				return out, nil
			}
		}
	}

	// 2) 查序列清单（只取 identity 所需列）。
	sq := s.db.Model(&model.MetricSeries{}).Where("scope = ?", kind)
	if kind == model.MetricScopeInstance {
		if instanceUUIDs != nil {
			sq = sq.Where("instance_id IN ?", instanceUUIDs)
		}
	} else {
		if nodeUUIDs != nil {
			sq = sq.Where("node_uuid IN ?", nodeUUIDs)
		}
	}
	var all []model.MetricSeries
	if err := sq.Order("id").Find(&all).Error; err != nil {
		return out, fmt.Errorf("查询指标序列失败: %w", err)
	}
	out.Pagination.Total = len(all)
	if len(all) == 0 {
		return out, nil
	}

	// 3) 按分页切出当前页序列。
	start := (q.Page - 1) * q.PageSize
	if start >= len(all) {
		return out, nil
	}
	end := start + q.PageSize
	if end > len(all) {
		end = len(all)
	}
	pageRows := all[start:end]

	// 4) 按档位查点位（一次 IN 查询，分批避免超长 SQL）。
	seriesIDs := make([]uint, 0, len(pageRows))
	for _, sr := range pageRows {
		seriesIDs = append(seriesIDs, sr.ID)
	}
	points, err := s.loadPoints(seriesIDs, q.Resolution, q.From, q.To)
	if err != nil {
		return out, err
	}

	// 5) 回填实例数值 ID（instance 维度），并按契约命名指标。
	nodeIDByUUID := map[string]uint{}
	if kind == model.MetricScopeNode {
		nodeIDByUUID, err = s.nodeIDsByUUID(pageRows)
		if err != nil {
			return out, err
		}
	}
	instIDByUUID := map[string]uint{}
	if kind == model.MetricScopeInstance {
		instIDByUUID, err = s.instanceIDsByUUIDForSeries(pageRows)
		if err != nil {
			return out, err
		}
	}

	out.Series = make([]StatsMetricSeries, 0, len(pageRows))
	for _, sr := range pageRows {
		item := StatsMetricSeries{
			Metric: statsMetricName(kind, sr.MetricKey),
			Points: points[sr.ID],
		}
		if item.Points == nil {
			item.Points = []StatsMetricPoint{}
		}
		if kind == model.MetricScopeInstance {
			item.InstanceID = instIDByUUID[sr.InstanceID]
		} else {
			item.NodeID = nodeIDByUUID[sr.NodeUUID]
		}
		out.Series = append(out.Series, item)
	}
	return out, nil
}

// statsSeriesBatch 单次 IN 查询的序列数上限。SQLite 的变量数上限为 999，
// 取 500 留余量；超出即分批，保证语句不会因变量过多失败。
const statsSeriesBatch = 500

// loadPoints 按档位读取点位。**这是「5m 不打 raw 表」的唯一入口**：
// raw 档只查 metric_sample_raws，其余档只查对应的 rollup 表。
func (s *StatsService) loadPoints(seriesIDs []uint, res StatsResolution, from, to time.Time) (map[uint][]StatsMetricPoint, error) {
	out := make(map[uint][]StatsMetricPoint, len(seriesIDs))
	for _, id := range seriesIDs {
		out[id] = []StatsMetricPoint{}
	}
	for start := 0; start < len(seriesIDs); start += statsSeriesBatch {
		end := start + statsSeriesBatch
		if end > len(seriesIDs) {
			end = len(seriesIDs)
		}
		batch := seriesIDs[start:end]
		switch res {
		case StatsResolutionRaw:
			var rows []model.MetricSampleRaw
			if err := s.db.Where("series_id IN ? AND ts >= ? AND ts <= ? AND value IS NOT NULL", batch, from.UTC(), to.UTC()).
				Order("series_id, ts").Find(&rows).Error; err != nil {
				return nil, fmt.Errorf("查询原始样本失败: %w", err)
			}
			for _, r := range rows {
				if r.Value == nil {
					continue
				}
				out[r.SeriesID] = append(out[r.SeriesID], StatsMetricPoint{T: r.TS.UTC(), V: *r.Value})
			}
		case StatsResolution1h:
			var rows []model.MetricRollup1h
			if err := s.db.Where("series_id IN ? AND bucket_ts >= ? AND bucket_ts <= ?", batch, from.UTC(), to.UTC()).
				Order("series_id, bucket_ts").Find(&rows).Error; err != nil {
				return nil, fmt.Errorf("查询小时预聚合失败: %w", err)
			}
			for _, r := range rows {
				out[r.SeriesID] = append(out[r.SeriesID], StatsMetricPoint{T: r.BucketTS.UTC(), V: r.Avg})
			}
		default: // StatsResolution5m（含 15s/1m 的映射目标）
			var rows []model.MetricRollup5m
			if err := s.db.Where("series_id IN ? AND bucket_ts >= ? AND bucket_ts <= ?", batch, from.UTC(), to.UTC()).
				Order("series_id, bucket_ts").Find(&rows).Error; err != nil {
				return nil, fmt.Errorf("查询 5 分钟预聚合失败: %w", err)
			}
			for _, r := range rows {
				out[r.SeriesID] = append(out[r.SeriesID], StatsMetricPoint{T: r.BucketTS.UTC(), V: r.Avg})
			}
		}
	}
	return out, nil
}

// statsMetricName 把内部 metric_key 转为契约里的对外指标名。
//
// 剥掉 `inst_` / `node_` 前缀：契约示例用 `cpu_pct` / `tps` / `rss_mb`，而内部键带
// scope 前缀（`inst_cpu_pct` / `node_cpu_pct`）。两个接口各自只有一个 scope，
// 剥离后不会歧义；未知前缀原样返回，避免新增指标键时被静默改名。
func statsMetricName(kind model.MetricScope, key string) string {
	prefix := "inst_"
	if kind == model.MetricScopeNode {
		prefix = "node_"
	}
	return strings.TrimPrefix(key, prefix)
}

// instanceUUIDs 批量解析实例 UUID。
func (s *StatsService) instanceUUIDs(ids []uint) ([]string, error) {
	var rows []model.Instance
	if err := s.db.Select("id", "uuid").Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("解析实例 UUID 失败: %w", err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.UUID)
	}
	return out, nil
}

// nodeUUIDs 批量解析节点 UUID。
func (s *StatsService) nodeUUIDs(ids []uint) ([]string, error) {
	var rows []model.Node
	if err := s.db.Select("id", "uuid").Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("解析节点 UUID 失败: %w", err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.UUID)
	}
	return out, nil
}

// nodeIDsByUUID 回填节点数值 ID。
func (s *StatsService) nodeIDsByUUID(rows []model.MetricSeries) (map[string]uint, error) {
	uuids := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.NodeUUID != "" {
			uuids = append(uuids, r.NodeUUID)
		}
	}
	out := make(map[string]uint, len(uuids))
	if len(uuids) == 0 {
		return out, nil
	}
	var nodes []model.Node
	if err := s.db.Select("id", "uuid").Where("uuid IN ?", uuids).Find(&nodes).Error; err != nil {
		return nil, fmt.Errorf("回填节点 ID 失败: %w", err)
	}
	for _, n := range nodes {
		out[n.UUID] = n.ID
	}
	return out, nil
}

// instanceIDsByUUIDForSeries 回填实例数值 ID（序列行 → 实例 ID）。
func (s *StatsService) instanceIDsByUUIDForSeries(rows []model.MetricSeries) (map[string]uint, error) {
	uuids := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.InstanceID != "" {
			uuids = append(uuids, r.InstanceID)
		}
	}
	out := make(map[string]uint, len(uuids))
	if len(uuids) == 0 {
		return out, nil
	}
	var insts []model.Instance
	if err := s.db.Select("id", "uuid").Where("uuid IN ?", uuids).Find(&insts).Error; err != nil {
		return nil, fmt.Errorf("回填实例 ID 失败: %w", err)
	}
	for _, i := range insts {
		out[i.UUID] = i.ID
	}
	return out, nil
}

// QueryLogs 检索日志。返回内容不含玩家敏感信息（脱敏由采集侧负责）。
func (s *StatsService) QueryLogs(q StatsLogQuery, scope StatsScope) (StatsLogQueryResponse, error) {
	out := StatsLogQueryResponse{
		Entries:    []StatsLogEntry{},
		Pagination: StatsPagination{Page: q.Page, PageSize: q.PageSize},
	}
	if !scope.allowsAnyInstance() {
		return out, nil
	}
	// 显式 instanceId 必须落在可见集合内。
	if q.InstanceID != nil && scope.Scoped {
		allowed := false
		for _, id := range scope.InstanceIDs {
			if id == *q.InstanceID {
				allowed = true
				break
			}
		}
		if !allowed {
			return out, nil
		}
	}

	base := s.db.Model(&model.LogEntry{})
	if q.InstanceID != nil {
		base = base.Where("instance_id = ?", *q.InstanceID)
	} else if scope.Scoped {
		if len(scope.InstanceIDs) == 0 {
			base = base.Where("1 = 0")
		} else {
			base = base.Where("instance_id IN ?", scope.InstanceIDs)
		}
	}
	if q.NodeID != nil {
		base = base.Where("node_id = ?", *q.NodeID)
	}
	if lv := strings.TrimSpace(q.Level); lv != "" {
		level, ok := parseStatsLogLevel(lv)
		if !ok {
			return out, &StatsError{Code: StatsCodeInvalidArgument, Message: "level 必须为 ERROR、WARN、INFO 或 DEBUG"}
		}
		base = base.Where("level = ?", level)
	}
	if kw := strings.TrimSpace(q.Keyword); kw != "" {
		base = base.Where("message LIKE ?", "%"+kw+"%")
	}
	base = base.Where("time >= ? AND time <= ?", q.From.UTC(), q.To.UTC())

	var total int64
	if err := base.Count(&total).Error; err != nil {
		return out, fmt.Errorf("统计日志总数失败: %w", err)
	}
	out.Pagination.Total = int(total)
	if total == 0 {
		return out, nil
	}

	var rows []model.LogEntry
	if err := base.Order("time DESC").Order("id DESC").
		Limit(q.PageSize).Offset((q.Page - 1) * q.PageSize).
		Find(&rows).Error; err != nil {
		return out, fmt.Errorf("查询日志失败: %w", err)
	}
	out.Entries = make([]StatsLogEntry, 0, len(rows))
	for _, r := range rows {
		src := strings.TrimSpace(r.Stream)
		if src == "" {
			src = string(r.Source)
		}
		out.Entries = append(out.Entries, StatsLogEntry{
			Timestamp:  r.Time.UTC(),
			Level:      statsLogLevelName(r.Level),
			InstanceID: r.InstanceID,
			NodeID:     r.NodeID,
			Message:    r.Message,
			Source:     src,
		})
	}
	return out, nil
}

// statsLogLevelName 把内部小写级别转为契约的大写枚举。
func statsLogLevelName(l model.LogLevel) string {
	switch l {
	case model.LogLevelError:
		return "ERROR"
	case model.LogLevelWarn:
		return "WARN"
	case model.LogLevelDebug:
		return "DEBUG"
	default:
		return "INFO"
	}
}

// parseStatsLogLevel 解析契约级别（大小写不敏感）。
func parseStatsLogLevel(raw string) (model.LogLevel, bool) {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "ERROR":
		return model.LogLevelError, true
	case "WARN":
		return model.LogLevelWarn, true
	case "INFO":
		return model.LogLevelInfo, true
	case "DEBUG":
		return model.LogLevelDebug, true
	}
	return "", false
}

// QueryCrashTrend 返回崩溃趋势（按天 + 按根因）。
//
// 数据源是独立汇总表 instance_crash_stats，**不受单次崩溃快照的 K=5 滚动裁剪影响**。
func (s *StatsService) QueryCrashTrend(q StatsCrashQuery, scope StatsScope) (StatsCrashTrendResponse, error) {
	out := StatsCrashTrendResponse{ByDay: []StatsCrashDay{}, ByReason: []StatsCrashReason{}}
	if !scope.allowsAnyInstance() {
		return out, nil
	}

	dbq := s.db.Model(&model.InstanceCrashStat{}).Where("bucket_day >= ?", crashDayFloor(q.Days))
	if q.InstanceID != 0 {
		if scope.Scoped {
			allowed := false
			for _, id := range scope.InstanceIDs {
				if id == q.InstanceID {
					allowed = true
					break
				}
			}
			if !allowed {
				return out, nil
			}
		}
		dbq = dbq.Where("instance_id = ?", q.InstanceID)
	} else if scope.Scoped {
		if len(scope.InstanceIDs) == 0 {
			dbq = dbq.Where("1 = 0")
		} else {
			dbq = dbq.Where("instance_id IN ?", scope.InstanceIDs)
		}
	}

	var stats []model.InstanceCrashStat
	if err := dbq.Select("bucket_day", "root_cause", "count").Find(&stats).Error; err != nil {
		return out, fmt.Errorf("查询崩溃统计失败: %w", err)
	}
	byDay := map[string]int{}
	byReason := map[string]int{}
	for _, st := range stats {
		byDay[st.BucketDay] += st.Count
		byReason[st.RootCause] += st.Count
	}
	for day, count := range byDay {
		out.ByDay = append(out.ByDay, StatsCrashDay{Date: day, Count: count})
	}
	sort.Slice(out.ByDay, func(i, j int) bool { return out.ByDay[i].Date < out.ByDay[j].Date })
	for reason, count := range byReason {
		out.ByReason = append(out.ByReason, StatsCrashReason{Reason: reason, Count: count})
	}
	sort.Slice(out.ByReason, func(i, j int) bool {
		if out.ByReason[i].Count != out.ByReason[j].Count {
			return out.ByReason[i].Count > out.ByReason[j].Count
		}
		return out.ByReason[i].Reason < out.ByReason[j].Reason
	})
	return out, nil
}

// statsChangeStatusToSession 把契约状态名映射到滚动编排会话状态集合。
//
// 【数据源说明】契约（schemas.yaml#/ChangeSummary）描述的是「配置交付变更单」，
// 对应的 ConfigDeliveryOrder 状态机。该模型**已交付**（v0.24.0，见
// model/config_delivery.go 与 service/config_delivery.go 的完整状态机）。
//
// 但本函数的 adapter 仍以的 InstanceRollingOp（滚动/分批/灰度编排会话）
// 为数据源，即仪表盘「变更」列表当前展示的是**滚动编排会话**而非配置交付单。
// 保持现状的原因：切换数据源会改变该列表的语义与用户可见内容（哪些变更算「最近变更」），
// 属产品口径决策，不宜在未确认的情况下顺手改。
//
// 因此这里做状态名双向映射，保证出参严格落在契约 enum 内；若将来确定以交付单为准，
// 只需替换本函数与 statsChangeSummaryOf，路由与契约不变。
var statsChangeStatusToSession = map[string]model.RollingState{
	"draft":            model.RollingStatePending,
	"pending_approval": model.RollingStatePending,
	"approved":         model.RollingStatePending,
	"rolling":          model.RollingStateRunning,
	"observing":        model.RollingStateRunning,
	"paused":           model.RollingStatePaused,
	"completed":        model.RollingStateDone,
	"failed":           model.RollingStateDone,
	"rolled_back":      model.RollingStateCanceled,
}

// statsSessionToChangeStatus 是上方映射的反向（用于出参）。
var statsSessionToChangeStatus = map[model.RollingState]string{
	model.RollingStatePending:  "draft",
	model.RollingStateRunning:  "rolling",
	model.RollingStatePaused:   "paused",
	model.RollingStateDone:     "completed",
	model.RollingStateCanceled: "rolled_back",
}

// statsInFlightStatuses 未指定 status 时的缺省在途状态（终态不算在途）。
var statsInFlightStatuses = []model.RollingState{
	model.RollingStatePending, model.RollingStateRunning, model.RollingStatePaused,
}

// ParseStatsChangeStatuses 解析 status 过滤（逗号分隔的契约状态名）。
// 未知状态名返回 INVALID_ARGUMENT——静默忽略会让调用方以为过滤生效。
func ParseStatsChangeStatuses(raw string) ([]model.RollingState, *StatsError) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return statsInFlightStatuses, nil
	}
	seen := map[model.RollingState]struct{}{}
	out := make([]model.RollingState, 0, 4)
	for _, part := range strings.Split(trimmed, ",") {
		name := strings.ToLower(strings.TrimSpace(part))
		if name == "" {
			continue
		}
		state, ok := statsChangeStatusToSession[name]
		if !ok {
			return nil, &StatsError{
				Code:    StatsCodeInvalidArgument,
				Message: fmt.Sprintf("未知的 status：%s", name),
			}
		}
		if _, dup := seen[state]; dup {
			continue
		}
		seen[state] = struct{}{}
		out = append(out, state)
	}
	if len(out) == 0 {
		return statsInFlightStatuses, nil
	}
	return out, nil
}

// ListInFlightChanges 返回在途变更（不含配置内容）。
func (s *StatsService) ListInFlightChanges(q StatsChangeQuery) (StatsChangeListResponse, error) {
	out := StatsChangeListResponse{
		Changes:    []StatsChangeSummary{},
		Pagination: StatsPagination{Page: q.Page, PageSize: q.PageSize},
	}
	statuses := q.Statuses
	if len(statuses) == 0 {
		statuses = statsInFlightStatuses
	}

	base := s.db.Model(&model.InstanceRollingOp{}).Where("state IN ?", statuses)
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return out, fmt.Errorf("统计在途变更失败: %w", err)
	}
	out.Pagination.Total = int(total)
	if total == 0 {
		return out, nil
	}

	var ops []model.InstanceRollingOp
	if err := base.Order("id DESC").
		Limit(q.PageSize).Offset((q.Page - 1) * q.PageSize).
		Find(&ops).Error; err != nil {
		return out, fmt.Errorf("查询在途变更失败: %w", err)
	}
	out.Changes = make([]StatsChangeSummary, 0, len(ops))
	for _, op := range ops {
		out.Changes = append(out.Changes, statsChangeSummaryOf(op))
	}
	return out, nil
}

// statsChangeSummaryOf 把滚动会话投影为契约摘要。
//
// title 由「动作 × 目标数」派生：InstanceRollingOp 没有独立标题列，而对外契约要求
// title 可展示。这里只陈述通道动作（如 `restart × 12`），不引入任何业务语义。
func statsChangeSummaryOf(op model.InstanceRollingOp) StatsChangeSummary {
	status, ok := statsSessionToChangeStatus[op.State]
	if !ok {
		status = "draft"
	}
	created := op.CreatedAt.UTC()
	// startedAt 语义是「会话真正开跑的时刻」；滚动会话创建后同步 launch，
	// 故非 pending 时取 UpdatedAt 之前的 CreatedAt 即最接近的真实起点。
	var started *time.Time
	if op.State != model.RollingStatePending {
		t := created
		started = &t
	}
	return StatsChangeSummary{
		ID:           op.ID,
		Title:        fmt.Sprintf("%s × %d", op.Action, op.Requested),
		Status:       status,
		TotalTargets: op.Requested,
		Succeeded:    op.Succeeded,
		Failed:       op.Failed,
		// Deferred 是的「离线/暂不可达，不计入失败率」概念；
		// 滚动编排没有该分类，恒为 0（不编造数据）。
		Deferred:  0,
		CreatedAt: created,
		StartedAt: started,
	}
}

// ── 限流 ──────────────────────────────────────────────────────────────

// StatsRateLimitPerMinute 每 token 每分钟的请求上限（spec §4.4）。
const StatsRateLimitPerMinute = 120

// statsRateLimitMaxBuckets 限流桶表上限。超过即清理过期窗口，防止状态随键无限膨胀。
const statsRateLimitMaxBuckets = 10000

// StatsRateLimiter 按 key（token 标识）计的固定窗口限流器。
//
// 为什么用固定窗口而非令牌桶：spec 的语义就是「每 token **每分钟** 120 次」，
// 固定窗口与之一一对应，且 Retry-After 可直接由「距窗口结束」算出。
type StatsRateLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	buckets map[string]*statsRateBucket
	// now 可注入，便于测试推进时间。
	now func() time.Time
}

// statsRateBucket 单个 key 在当前窗口内的计数。
type statsRateBucket struct {
	windowStart time.Time
	count       int
}

// NewStatsRateLimiter 创建限流器。
func NewStatsRateLimiter(limit int, window time.Duration) *StatsRateLimiter {
	if limit <= 0 {
		limit = StatsRateLimitPerMinute
	}
	if window <= 0 {
		window = time.Minute
	}
	return &StatsRateLimiter{
		limit:   limit,
		window:  window,
		buckets: map[string]*statsRateBucket{},
		now:     time.Now,
	}
}

// SetClockForTest 注入时钟（仅测试用）。
func (l *StatsRateLimiter) SetClockForTest(fn func() time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if fn == nil {
		fn = time.Now
	}
	l.now = fn
}

// Allow 记一次调用并返回 (剩余配额, 若被拒的建议退避秒数, 是否放行)。
func (l *StatsRateLimiter) Allow(key string) (remaining int, retryAfterSec int, ok bool) {
	if l == nil {
		return l.limit, 0, true
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.buckets) >= statsRateLimitMaxBuckets {
		for k, b := range l.buckets {
			if now.Sub(b.windowStart) >= l.window {
				delete(l.buckets, k)
			}
		}
	}
	b, exists := l.buckets[key]
	if !exists || now.Sub(b.windowStart) >= l.window {
		l.buckets[key] = &statsRateBucket{windowStart: now, count: 1}
		return l.limit - 1, 0, true
	}
	if b.count >= l.limit {
		// 距本窗口结束的秒数（向上取整，至少 1 秒）。
		wait := l.window - now.Sub(b.windowStart)
		sec := int(wait / time.Second)
		if wait%time.Second != 0 {
			sec++
		}
		if sec < 1 {
			sec = 1
		}
		return 0, sec, false
	}
	b.count++
	return l.limit - b.count, 0, true
}
