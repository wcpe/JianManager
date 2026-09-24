package logcoord

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// OrderVersion 稳定复合排序版本（FR-472 §6.3 / §4.5）。
//
//	(event_time DESC, log_source_id ASC, source_generation ASC,
//	 record_start DESC, record_end DESC, event_id ASC)
const OrderVersion = "v1:event_time.DESC,log_source_id.ASC,source_generation.ASC,record_start.DESC,record_end.DESC,event_id.ASC"

const DefaultPageLimit = 200

// EnumerationState 列表/导出枚举结束语义（与 proto LogEnumerationState 对齐）。
type EnumerationState string

const (
	EnumOpen      EnumerationState = "OPEN"
	EnumExhausted EnumerationState = "EXHAUSTED"
	EnumStale     EnumerationState = "STALE"
	EnumCancelled EnumerationState = "CANCELLED"
)

// CoverageState 目标覆盖状态（proto LogCoverageState 字符串形态）。
type CoverageState string

const (
	CoverageUnspecified    CoverageState = "unspecified"
	CoverageSuccess        CoverageState = "success"
	CoverageOffline        CoverageState = "offline"
	CoverageNotReady       CoverageState = "not_ready"
	CoverageArchiveMissing CoverageState = "archive_missing"
	CoveragePartial        CoverageState = "partial"
	CoverageStale          CoverageState = "stale"
)

// DuplicateQuality / StatsQuality 独立于覆盖完整性（FR-472 §6.3）。
type DuplicateQuality string

const (
	DupUnspecified DuplicateQuality = "unspecified"
	DupExact       DuplicateQuality = "exact"
	DupUnresolved  DuplicateQuality = "unresolved"
	DupConflict    DuplicateQuality = "conflict"
)

type StatsQuality string

const (
	StatsQUnspecified StatsQuality = "unspecified"
	StatsQExact       StatsQuality = "exact"
	StatsQPartial     StatsQuality = "partial"
	StatsQUnavailable StatsQuality = "unavailable"
)

// Readiness 目标当前可查询就绪度（CP 注册表/探测结果，非覆盖结果）。
type Readiness string

const (
	ReadyOnline         Readiness = "online"
	ReadyOffline        Readiness = "offline"
	ReadyNotReady       Readiness = "not_ready"
	ReadyArchiveMissing Readiness = "archive_missing"
	ReadyUnsupported    Readiness = "unsupported" // 老 Worker Log RPC Unimplemented
)

// TargetInfo CP 计算出的授权目标 + 运行时就绪信息。
//
// 实例换节点时，历史数据持有者（HistoricalHolder）仍属于授权目标集合，
// 不得因当前不在线而被静默丢弃。
type TargetInfo struct {
	ID               string    `json:"id"`
	AuthorizationID  string    `json:"authorization_id,omitempty"`
	WorkerTargetID   string    `json:"worker_target_id,omitempty"`
	WorkerID         string    `json:"worker_id"`
	Readiness        Readiness `json:"readiness"`
	HistoricalHolder bool      `json:"historical_holder"`
	// ClosedVisibleSeq / CatalogGeneration 来自 Worker PublishedProjection（若已知）。
	ClosedVisibleSeq  string `json:"closed_visible_seq,omitempty"`
	CatalogGeneration string `json:"catalog_generation,omitempty"`
}

// QueryBudget 结果行预算与扇出/字节预算（FR-472：limit 只限结果数）。
type QueryBudget struct {
	Limit     int    `json:"limit"`
	MaxBytes  uint64 `json:"max_bytes"`
	TimeoutMS uint32 `json:"timeout_ms"`
	MaxFanout uint32 `json:"max_fanout"`
}

// Query 协调器查询请求。
type Query struct {
	RequestID string `json:"request_id"`
	FromUTC   string `json:"from_utc"` // RFC3339，闭开 [from,to)
	ToUTC     string `json:"to_utc"`
	// AuthorizedTargetIDs 为 CP 鉴权后的全部授权目标；协调器必须先解析全集。
	AuthorizedTargetIDs []string `json:"authorized_target_ids"`
	// PrincipalKey 当前调用方授权主体标识（如 user:<id>|role:<roleKey>|scope digest）。
	// Query View 创建时绑定；复用 viewId 时必须与创建主体一致且授权集合仍覆盖 view 目标。
	PrincipalKey string `json:"principal_key,omitempty"`
	// OnlineOnly 仅在调用方显式选择「仅在线」时为 true；不得默认。
	OnlineOnly bool        `json:"online_only"`
	Budget     QueryBudget `json:"budget"`
	// ViewID 非空时复用既有逻辑事件集合，不重新选择最新目标集合。
	ViewID string `json:"view_id,omitempty"`
	// Cursor 是 CP 联邦分页边界，绑定 View 与全局复合排序键。
	Cursor string `json:"cursor,omitempty"`
	// OrderVersion 复用 view 时须与创建时一致（空则跳过该维校验）。
	OrderVersion    string `json:"order_version,omitempty"`
	Filter          string `json:"filter,omitempty"`
	PermissionScope string `json:"permission_scope,omitempty"`
	CancelToken     string `json:"cancellation_token,omitempty"`
}

// SortKey 稳定复合排序键。
type SortKey struct {
	EventTimeUTC     time.Time
	LogSourceID      string
	SourceGeneration string
	RecordStart      uint64
	RecordEnd        uint64
	EventID          string
}

// Compare 返回 a 相对 b 的排序位置（负数 a 在前，0 相等，正数 b 在前）。
// 顺序：event_time DESC, log_source_id ASC, source_generation ASC,
// record_start DESC, record_end DESC, event_id ASC。
func (a SortKey) Compare(b SortKey) int {
	if !a.EventTimeUTC.Equal(b.EventTimeUTC) {
		if a.EventTimeUTC.After(b.EventTimeUTC) {
			return -1
		}
		return 1
	}
	if c := strings.Compare(a.LogSourceID, b.LogSourceID); c != 0 {
		return c
	}
	if c := strings.Compare(a.SourceGeneration, b.SourceGeneration); c != 0 {
		return c
	}
	if a.RecordStart != b.RecordStart {
		if a.RecordStart > b.RecordStart {
			return -1
		}
		return 1
	}
	if a.RecordEnd != b.RecordEnd {
		if a.RecordEnd > b.RecordEnd {
			return -1
		}
		return 1
	}
	return strings.Compare(a.EventID, b.EventID)
}

// Event 协调器合并用的逻辑事件行。
type Event struct {
	EventID          string `json:"event_id"`
	LogSourceID      string `json:"log_source_id"`
	SourceGeneration string `json:"source_generation"`
	RecordStart      uint64 `json:"record_start"`
	RecordEnd        uint64 `json:"record_end"`
	EventTimeUTC     string `json:"event_time_utc"` // RFC3339
	IngestTimeUTC    string `json:"ingest_time_utc,omitempty"`
	Level            string `json:"level,omitempty"`
	Stream           string `json:"stream,omitempty"`
	Message          string `json:"message,omitempty"`
	CanonicalHash    string `json:"canonical_content_hash,omitempty"`
	// TargetID 合并时标注来源目标（便于覆盖侧信道审计，不参与排序）。
	TargetID string `json:"target_id,omitempty"`
	// WorkerID 来源 Worker。
	WorkerID string `json:"worker_id,omitempty"`
}

// SortKey 提取稳定复合排序键；非法时间按零值处理（仍保持确定性）。
func (e Event) SortKey() SortKey {
	t, _ := time.Parse(time.RFC3339Nano, e.EventTimeUTC)
	if t.IsZero() {
		t, _ = time.Parse(time.RFC3339, e.EventTimeUTC)
	}
	return SortKey{
		EventTimeUTC:     t,
		LogSourceID:      e.LogSourceID,
		SourceGeneration: e.SourceGeneration,
		RecordStart:      e.RecordStart,
		RecordEnd:        e.RecordEnd,
		EventID:          e.EventID,
	}
}

// ApproxBytes 粗略事件字节估算（用于 MaxBytes 预算门禁）。
func (e Event) ApproxBytes() uint64 {
	n := len(e.EventID) + len(e.LogSourceID) + len(e.SourceGeneration) +
		len(e.EventTimeUTC) + len(e.IngestTimeUTC) + len(e.Level) +
		len(e.Stream) + len(e.Message) + len(e.CanonicalHash) +
		len(e.TargetID) + len(e.WorkerID) + 32
	if n < 0 {
		return 0
	}
	return uint64(n)
}

// TargetCoverage 单目标覆盖摘要。
type TargetCoverage struct {
	TargetID          string        `json:"target_id"`
	State             CoverageState `json:"state"`
	Reasons           []string      `json:"reasons,omitempty"`
	ClosedVisibleSeq  string        `json:"closed_visible_seq,omitempty"`
	CatalogGeneration string        `json:"catalog_generation,omitempty"`
	WorkerID          string        `json:"worker_id,omitempty"`
	HistoricalHolder  bool          `json:"historical_holder,omitempty"`
}

// Coverage 跨目标覆盖摘要；随 Search/Stats/Facets/Export 返回。
type Coverage struct {
	Complete         bool             `json:"complete"`
	PartialReasons   []string         `json:"partial_reasons,omitempty"`
	Targets          []TargetCoverage `json:"targets,omitempty"`
	EnumerationState EnumerationState `json:"enumeration_state"`
}

// Quality 查询质量维度（与 complete 独立）。
type Quality struct {
	DuplicateQuality DuplicateQuality `json:"duplicate_quality"`
	StatsQuality     StatsQuality     `json:"stats_quality"`
}

// View 跨 Worker 固定查询视图：各目标 closed_visible_seq 向量。
// 不承诺全局同一物理时刻；Export 与 Search 必须复用同一 view。
type View struct {
	ViewID        string   `json:"view_id"`
	OrderVersion  string   `json:"order_version"`
	FromUTC       string   `json:"from_utc"`
	ToUTC         string   `json:"to_utc"`
	TargetIDs     []string `json:"target_ids"` // 解析后的查询目标集合
	AuthorizedIDs []string `json:"authorized_ids"`
	// PrincipalKey 创建时的授权主体；复用 viewId 时必须与当前调用方一致（F-001）。
	PrincipalKey    string      `json:"principal_key,omitempty"`
	OnlineOnly      bool        `json:"online_only"`
	Budget          QueryBudget `json:"budget"`
	Filter          string      `json:"filter,omitempty"`
	PermissionScope string      `json:"permission_scope,omitempty"`
	// WorkerViewIDs binds the CP view to each Worker-local planner view.
	WorkerViewIDs            map[string]string `json:"-"`
	WorkerViewErrors         map[string]string `json:"-"`
	TargetAuthorizationIDs   map[string]string `json:"-"`
	ClosedVisibleSeq         map[string]string `json:"closed_visible_seq,omitempty"`
	WorkerCatalogGenerations map[string]string `json:"-"`
	CreatedAt                time.Time         `json:"created_at"`
}

// SearchResponse 跨 Worker Search 汇总结果。
type SearchResponse struct {
	View       View     `json:"view"`
	Coverage   Coverage `json:"coverage"`
	Quality    Quality  `json:"quality"`
	Items      []Event  `json:"items"`
	Exhausted  bool     `json:"exhausted"`
	NextCursor string   `json:"next_cursor,omitempty"`
	// BudgetCut 行/字节预算截断了结果；与 Coverage.Complete 独立。
	BudgetCut bool `json:"budget_cut"`
}

type FieldsResponse struct {
	View     View     `json:"view"`
	Coverage Coverage `json:"coverage"`
	Fields   []string `json:"fields"`
}

// StatsQuery 统计请求。
type StatsQuery struct {
	Query
	GroupBy    []string `json:"group_by,omitempty"`
	TimeBucket string   `json:"time_bucket,omitempty"` // 如 "1m" / "5m" / "1h"
	// MetricField 参与 sum/min/max/avg 的数值字段；空则只做 count。
	MetricField string `json:"metric_field,omitempty"`
}

// StatsAggregate 可合并聚合：avg = sum/count（加权，禁止对 avg 再平均）。
type StatsAggregate struct {
	Count     uint64  `json:"count"`
	Sum       float64 `json:"sum"`
	Min       float64 `json:"min"`
	Max       float64 `json:"max"`
	HasMinMax bool    `json:"has_min_max"`
}

// Merge 将 b 合并进 a（sum+count；min/max 取边界）。
func (a *StatsAggregate) Merge(b StatsAggregate) {
	a.Count += b.Count
	a.Sum += b.Sum
	if b.Count == 0 && !b.HasMinMax {
		return
	}
	if !a.HasMinMax && b.HasMinMax {
		a.Min, a.Max, a.HasMinMax = b.Min, b.Max, true
		return
	}
	if !a.HasMinMax {
		return
	}
	if b.HasMinMax {
		if b.Min < a.Min {
			a.Min = b.Min
		}
		if b.Max > a.Max {
			a.Max = b.Max
		}
	}
}

// Avg 加权平均：sum/count；count==0 时返回 0。
func (a StatsAggregate) Avg() float64 {
	if a.Count == 0 {
		return 0
	}
	return a.Sum / float64(a.Count)
}

// StatsPoint 一个维度/时间桶上的聚合结果。
type StatsPoint struct {
	// Dimensions 含 group_by 字段；TimeBucketUTC 为绝对 UTC 桶起点（RFC3339）。
	Dimensions    map[string]string `json:"dimensions,omitempty"`
	TimeBucketUTC string            `json:"time_bucket_utc,omitempty"`
	Agg           StatsAggregate    `json:"agg"`
}

// StatsResponse 跨 Worker 二次聚合结果。
type StatsResponse struct {
	View       View         `json:"view"`
	Coverage   Coverage     `json:"coverage"`
	Quality    Quality      `json:"quality"`
	Points     []StatsPoint `json:"points"`
	Metric     string       `json:"metric,omitempty"`
	TimeBucket string       `json:"time_bucket,omitempty"`
}

// FacetsQuery Facet 请求。
type FacetsQuery struct {
	Query
	Dimensions     []string `json:"dimensions"`
	DimensionLimit uint32   `json:"dimension_limit"` // 每维返回值上限
}

// FacetValue 维度值计数。
type FacetValue struct {
	Value string `json:"value"`
	Count uint64 `json:"count"`
}

// FacetDimension 合并后的单维度结果。
type FacetDimension struct {
	Dimension string `json:"dimension"`
	// Controlled 受控维度精确合并；高基数维度必须带截断语义。
	Controlled bool         `json:"controlled"`
	Values     []FacetValue `json:"values"`
	// Truncated 任一来源发生截断，或合并后按 DimensionLimit 裁剪。
	Truncated bool `json:"truncated"`
	// TruncatedCount 已知被省略的值数量（上界可来自 Worker 截断计数之和 + 本地裁剪）。
	TruncatedCount uint64 `json:"truncated_count"`
	Limit          uint32 `json:"limit"`
}

// FacetsResponse 跨 Worker Facet 汇总。
type FacetsResponse struct {
	View       View             `json:"view"`
	Coverage   Coverage         `json:"coverage"`
	Quality    Quality          `json:"quality"`
	Dimensions []FacetDimension `json:"dimensions"`
	// Truncated 任一维度存在截断语义。
	Truncated bool `json:"truncated"`
}

// ExportResult 导出物化结果。
//
// 完整校验通过前不得生成成功产物：partial / 截断 / 超预算一律 ExportIncomplete，
// Artifact 保持 nil。
type ExportResult struct {
	View     View     `json:"view"`
	Coverage Coverage `json:"coverage"`
	Quality  Quality  `json:"quality"`
	Items    []Event  `json:"items"`
	// ExportIncomplete 为 true 时 Artifact 必须为 nil。
	ExportIncomplete  bool     `json:"export_incomplete"`
	IncompleteReasons []string `json:"incomplete_reasons,omitempty"`
	BudgetExceeded    bool     `json:"budget_exceeded"`
	Cut               bool     `json:"cut"` // 列表/Worker 截断
	// Artifact 仅在完整通过时非 nil（NDJSON 等物化载荷）。
	Artifact []byte `json:"artifact,omitempty"`
	RowBytes uint64 `json:"row_bytes,omitempty"`
}

// ResolveReadinessToCoverage 就绪度映射为覆盖状态（查询前的默认）。
func ResolveReadinessToCoverage(r Readiness) CoverageState {
	switch r {
	case ReadyOnline:
		return CoverageSuccess
	case ReadyOffline:
		return CoverageOffline
	case ReadyNotReady:
		return CoverageNotReady
	case ReadyArchiveMissing:
		return CoverageArchiveMissing
	case ReadyUnsupported:
		return CoverageNotReady
	default:
		return CoverageUnspecified
	}
}

// FormatClosedVisibleSeq 将向量格式化为稳定字符串。
func FormatClosedVisibleSeq(vec map[string]string) string {
	if len(vec) == 0 {
		return ""
	}
	keys := make([]string, 0, len(vec))
	for k := range vec {
		keys = append(keys, k)
	}
	// 简单插入排序，保证确定性。
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(vec[k])
	}
	return b.String()
}

// NewViewID 生成视图 ID（基础实现；生产可替换）。
func NewViewID() string {
	return "cv_" + strconv.FormatInt(time.Now().UTC().UnixNano(), 36) +
		"_" + strconv.FormatInt(int64(time.Now().UnixNano()%1e6), 36)
}

// MustParseRFC3339 解析 RFC3339，失败返回零时间。
func MustParseRFC3339(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err == nil {
		return t
	}
	t, err = time.Parse(time.RFC3339, s)
	if err == nil {
		return t
	}
	return time.Time{}
}

// ValidateQuery 基础请求校验。
func ValidateQuery(q Query) error {
	if len(q.AuthorizedTargetIDs) == 0 && q.ViewID == "" {
		return fmt.Errorf("logcoord: authorized target set is empty")
	}
	if q.FromUTC != "" || q.ToUTC != "" {
		from := MustParseRFC3339(q.FromUTC)
		to := MustParseRFC3339(q.ToUTC)
		if q.FromUTC != "" && from.IsZero() {
			return fmt.Errorf("logcoord: invalid from_utc %q", q.FromUTC)
		}
		if q.ToUTC != "" && to.IsZero() {
			return fmt.Errorf("logcoord: invalid to_utc %q", q.ToUTC)
		}
		if !from.IsZero() && !to.IsZero() && !from.Before(to) {
			return fmt.Errorf("logcoord: time range from must be before to")
		}
	}
	return nil
}
