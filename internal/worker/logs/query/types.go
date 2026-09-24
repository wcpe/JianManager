package query

import (
	"fmt"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// SortVersion 是稳定复合排序的冻结版本标识。
// 与 logcoord.OrderVersion 同一语义键，接线前统一 wire 值，避免 view reuse 因 order_version 不一致失败。
// 契约 §6.3：(event_time DESC, log_source_id ASC, source_generation ASC,
// record_start DESC, record_end DESC, event_id ASC)。
const SortVersion = "v1:event_time.DESC,log_source_id.ASC,source_generation.ASC,record_start.DESC,record_end.DESC,event_id.ASC"

// DefaultProtocolVersion 与 proto LogQueryRequestBase.protocol_version 对齐。
const DefaultProtocolVersion = "log-query/1"

// 默认查询预算（仅当请求未携带 limit 时使用；契约：limit 只限制返回条数）。
const (
	DefaultLimit    uint32 = 200
	DefaultMaxLimit uint32 = 1000
)

// ErrorCode 对应 proto LogErrorCode 的语义名（生成代码未落地前的本地权威）。
type ErrorCode string

const (
	ErrCodeUnspecified         ErrorCode = "LOG_ERROR_UNSPECIFIED"
	ErrCodeViewStale           ErrorCode = "LOG_VIEW_STALE"
	ErrCodePartial             ErrorCode = "LOG_PARTIAL"
	ErrCodeUnauthorized        ErrorCode = "LOG_UNAUTHORIZED"
	ErrCodeBudgetExceeded      ErrorCode = "LOG_BUDGET_EXCEEDED"
	ErrCodeNotReady            ErrorCode = "LOG_NOT_READY"
	ErrCodeArchiveMissing      ErrorCode = "LOG_ARCHIVE_MISSING"
	ErrCodeDuplicateUnresolved ErrorCode = "LOG_ERR_DUPLICATE_UNRESOLVED"
	ErrCodeRecoveryRequired    ErrorCode = "LOG_RECOVERY_REQUIRED"
	ErrCodeCancelled           ErrorCode = "LOG_CANCELLED"
	ErrCodeUnsupported         ErrorCode = "LOG_UNSUPPORTED"
)

// CoverageState 对应 proto LogCoverageState。
type CoverageState string

const (
	CoverageUnspecified CoverageState = "LOG_COVERAGE_STATE_UNSPECIFIED"
	CoverageSuccess     CoverageState = "LOG_COVERAGE_SUCCESS"
	CoverageOffline     CoverageState = "LOG_COVERAGE_OFFLINE"
	CoverageNotReady    CoverageState = "LOG_COVERAGE_NOT_READY"
	CoverageArchiveMiss CoverageState = "LOG_COVERAGE_ARCHIVE_MISSING"
	CoveragePartial     CoverageState = "LOG_COVERAGE_PARTIAL"
	CoverageStale       CoverageState = "LOG_COVERAGE_STALE"
)

// EnumerationState 对应 proto LogEnumerationState。
type EnumerationState string

const (
	EnumOpen      EnumerationState = "LOG_ENUMERATION_OPEN"
	EnumExhausted EnumerationState = "LOG_ENUMERATION_EXHAUSTED"
	EnumStale     EnumerationState = "LOG_ENUMERATION_STALE"
	EnumCancelled EnumerationState = "LOG_ENUMERATION_CANCELLED"
)

// DuplicateQuality / StatsQuality 对应契约独立质量维度。
type DuplicateQuality string
type StatsQuality string

const (
	DupUnspecified DuplicateQuality = "LOG_QUALITY_DUPLICATE_UNSPECIFIED"
	DupExact       DuplicateQuality = "LOG_QUALITY_DUPLICATE_EXACT"
	DupUnresolved  DuplicateQuality = "LOG_QUALITY_DUPLICATE_UNRESOLVED"
	DupConflict    DuplicateQuality = "LOG_QUALITY_DUPLICATE_CONFLICT"

	StatsUnspecified StatsQuality = "LOG_QUALITY_STATS_UNSPECIFIED"
	StatsExact       StatsQuality = "LOG_QUALITY_STATS_EXACT"
	StatsPartial     StatsQuality = "LOG_QUALITY_STATS_PARTIAL"
	StatsUnavailable StatsQuality = "LOG_QUALITY_STATS_UNAVAILABLE"
)

// TailMode 对应契约：FOLLOW_LIVE（动态流）与 VIEW_BOUNDED（固定视图尾部）不可混称。
type TailMode string

const (
	TailFollowLive  TailMode = "FOLLOW_LIVE"
	TailViewBounded TailMode = "VIEW_BOUNDED"
)

// Tier 是存储层标识；与 catalog.Owner 对齐，另保留 deep/archive 别名。
type Tier string

const (
	TierHot     Tier = "hot"
	TierCold    Tier = "cold"
	TierDeep    Tier = "deep"
	TierArchive Tier = "archive"
)

// Coverage partial / not-ready 可观测原因（契约 §6.3 / FR-478 §3.1）。
const (
	ReasonColdMissing          = "COLD_MISSING"
	ReasonArchiveNotRestored   = "ARCHIVE_NOT_RESTORED"
	ReasonRehydrateFailed      = "REHYDRATE_FAILED"
	ReasonStagingExcluded      = "STAGING_EXCLUDED"
	ReasonRecoveryRequired     = "RECOVERY_REQUIRED"
	ReasonConflictGeneration   = "CONFLICT_GENERATION"
	ReasonJournalIncomplete    = "JOURNAL_INCOMPLETE"
	ReasonNoCatalogRecord      = "NO_CATALOG_RECORD"
	ReasonNoCatalogAuthority   = "NO_CATALOG_AUTHORITY"
	ReasonDuplicateUnresolved  = "DUPLICATE_UNRESOLVED"
	ReasonProjectionIncomplete = "PROJECTION_INCOMPLETE"
	ReasonBudgetExceeded       = "BUDGET_EXCEEDED"
	ReasonCancelled            = "CANCELLED"
	ReasonUnsupported          = "UNSUPPORTED"
	ReasonRangeUnavailable     = "RANGE_UNAVAILABLE"
	ReasonUnauthorizedTarget   = "UNAUTHORIZED_TARGET"
)

// QueryError 是结构化查询错误；partial 不得伪装成空成功。
type QueryError struct {
	Code        ErrorCode `json:"code"`
	Message     string    `json:"message"`
	Retryable   bool      `json:"retryable,omitempty"`
	RetryAfter  uint32    `json:"retry_after_ms,omitempty"`
	CoverageRef *Coverage `json:"-"`
}

func (e *QueryError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func newErr(code ErrorCode, msg string) *QueryError {
	return &QueryError{Code: code, Message: msg}
}

// TimeRange 绝对 UTC 时间范围，闭开区间 [FromUTC, ToUTC)。
type TimeRange struct {
	FromUTC string `json:"from_utc,omitempty"`
	ToUTC   string `json:"to_utc,omitempty"`
}

// Budget 对应 LogQueryBudget。limit 只限制返回条数，不等于查询成本上限。
type Budget struct {
	Limit     uint32 `json:"limit,omitempty"`
	MaxBytes  uint64 `json:"max_bytes,omitempty"`
	TimeoutMS uint32 `json:"timeout_ms,omitempty"`
	MaxFanout uint32 `json:"max_fanout,omitempty"`
}

// EffectiveLimit 返回生效的返回条数上限。
func (b Budget) EffectiveLimit() uint32 {
	if b.Limit == 0 {
		return DefaultLimit
	}
	if b.Limit > DefaultMaxLimit {
		return DefaultMaxLimit
	}
	return b.Limit
}

// ViewRef 对应 LogQueryViewRef。Cursor 只携带 (view_id, logical_sort_key, page_limit)。
type ViewRef struct {
	ViewID       string `json:"view_id,omitempty"`
	Cursor       string `json:"cursor,omitempty"`
	OrderVersion string `json:"order_version,omitempty"`
}

// SortKey 稳定复合排序键（契约 §6.3，不使用本地自增 ID）。
type SortKey struct {
	EventTimeUTC     string `json:"event_time_utc"`
	LogSourceID      string `json:"log_source_id"`
	SourceGeneration string `json:"source_generation"`
	RecordStart      uint64 `json:"record_start"`
	RecordEnd        uint64 `json:"record_end"`
	EventID          string `json:"event_id"`
}

// SortKeyOf 从规范事件提取排序键。
func SortKeyOf(ev logtypes.Event) SortKey {
	return SortKey{
		EventTimeUTC:     ev.EventTimeUTC,
		LogSourceID:      ev.Source.LogSourceID,
		SourceGeneration: ev.Source.SourceGeneration,
		RecordStart:      ev.Record.Start,
		RecordEnd:        ev.Record.End,
		EventID:          ev.EventID,
	}
}

// CompareSortKey 按契约排序：event_time DESC, log_source_id ASC, generation ASC,
// record_start DESC, record_end DESC, event_id ASC。
// 返回 -1/0/1：a < b 表示 a 在 DESC 结果序列中排在 b 之后。
func CompareSortKey(a, b SortKey) int {
	if c := compareUTCDESC(a.EventTimeUTC, b.EventTimeUTC); c != 0 {
		return c
	}
	if a.LogSourceID != b.LogSourceID {
		if a.LogSourceID < b.LogSourceID {
			return -1
		}
		return 1
	}
	if a.SourceGeneration != b.SourceGeneration {
		if a.SourceGeneration < b.SourceGeneration {
			return -1
		}
		return 1
	}
	if a.RecordStart != b.RecordStart {
		// DESC
		if a.RecordStart > b.RecordStart {
			return -1
		}
		return 1
	}
	if a.RecordEnd != b.RecordEnd {
		// DESC
		if a.RecordEnd > b.RecordEnd {
			return -1
		}
		return 1
	}
	if a.EventID != b.EventID {
		if a.EventID < b.EventID {
			return -1
		}
		return 1
	}
	return 0
}

// LessSortKey 报告 a 是否应排在 b 之前（结果序列更前）。
func LessSortKey(a, b SortKey) bool { return CompareSortKey(a, b) < 0 }

func compareUTCDESC(a, b string) int {
	// 时间字符串按 RFC3339 字典序即可比较 UTC；空值排最后。
	if a == b {
		return 0
	}
	if a == "" {
		return 1 // empty sorts after real times in DESC
	}
	if b == "" {
		return -1
	}
	if a > b {
		return -1 // newer first
	}
	return 1
}

// SortEvents 按冻结排序键就地排序（结果序列序：最新在前）。
func SortEvents(items []logtypes.Event) {
	// 插入排序足够覆盖 foundation stub 规模；生产由 VL 侧排序。
	for i := 1; i < len(items); i++ {
		j := i
		for j > 0 && LessSortKey(SortKeyOf(items[j]), SortKeyOf(items[j-1])) {
			items[j], items[j-1] = items[j-1], items[j]
			j--
		}
	}
}

// Cursor 是逻辑分页引用：只携带 view_id + sort key + page_limit。
type Cursor struct {
	ViewID       string  `json:"view_id"`
	OrderVersion string  `json:"order_version"`
	SortKey      SortKey `json:"sort_key"`
	PageLimit    uint32  `json:"page_limit"`
}

// EncodeCursor 序列化 Cursor。
func EncodeCursor(c Cursor) string {
	if c.OrderVersion == "" {
		c.OrderVersion = SortVersion
	}
	b, err := jsonMarshal(c)
	if err != nil {
		return ""
	}
	return string(b)
}

// DecodeCursor 反序列化 Cursor；失败返回 error。
func DecodeCursor(s string) (Cursor, error) {
	var c Cursor
	if s == "" {
		return c, fmt.Errorf("query: empty cursor")
	}
	if err := jsonUnmarshal([]byte(s), &c); err != nil {
		return Cursor{}, fmt.Errorf("query: decode cursor: %w", err)
	}
	return c, nil
}

// TargetCoverage 对应 LogCoverageTarget。
type TargetCoverage struct {
	TargetID           string        `json:"target_id"`
	State              CoverageState `json:"state"`
	Reasons            []string      `json:"reasons,omitempty"`
	ClosedVisibleSeq   uint64        `json:"closed_visible_seq"`
	CatalogGeneration  uint64        `json:"catalog_generation"`
	Owner              string        `json:"owner,omitempty"`
	DirID              string        `json:"dir_id,omitempty"`
	ProjectionManifest string        `json:"projection_manifest,omitempty"`
}

// Coverage 对应契约 §6.3 响应覆盖摘要。
type Coverage struct {
	Complete         bool             `json:"complete"`
	PartialReasons   []string         `json:"partial_reasons,omitempty"`
	Targets          []TargetCoverage `json:"targets,omitempty"`
	EnumerationState EnumerationState `json:"enumeration_state"`
}

// AddTarget 追加目标覆盖并维护 complete / partial_reasons。
func (c *Coverage) AddTarget(t TargetCoverage) {
	c.Targets = append(c.Targets, t)
	if t.State != CoverageSuccess {
		c.Complete = false
		for _, r := range t.Reasons {
			c.addReason(r)
		}
	}
}

func (c *Coverage) addReason(r string) {
	if r == "" {
		return
	}
	for _, x := range c.PartialReasons {
		if x == r {
			return
		}
	}
	c.PartialReasons = append(c.PartialReasons, r)
}

// MarkIncomplete 将 coverage 标为不完整并附加原因。
func (c *Coverage) MarkIncomplete(reason string) {
	c.Complete = false
	c.addReason(reason)
}

// Quality 独立于 completeness 的质量维度。
type Quality struct {
	DuplicateQuality DuplicateQuality `json:"duplicate_quality"`
	StatsQuality     StatsQuality     `json:"stats_quality"`
}

// QueryView 是固定查询视图（契约 §6.1）。
type QueryView struct {
	ViewID       string    `json:"view_id"`
	OrderVersion string    `json:"order_version"`
	TimeRange    TimeRange `json:"time_range"`
	Filter       string    `json:"filter,omitempty"`
	TargetIDs    []string  `json:"target_ids,omitempty"`
	// 每个目标创建时固定的 closed_visible_seq / generation / owner。
	ClosedVisibleSeq   map[string]uint64         `json:"closed_visible_seq,omitempty"`
	Generations        map[string]uint64         `json:"generations,omitempty"`
	Owners             map[string]string         `json:"owners,omitempty"`
	DirIDs             map[string]string         `json:"dir_ids,omitempty"`
	ProjectionVersions map[string]string         `json:"projection_versions,omitempty"`
	QueryableTargets   map[string]bool           `json:"queryable_targets,omitempty"`
	TargetCoverage     map[string]TargetCoverage `json:"target_coverage,omitempty"`
	CreatedAt          time.Time                 `json:"created_at"`
	ExpiresAt          time.Time                 `json:"expires_at"`
}

// AuthoritativeRange 是 planner 选出的不重叠权威查询副本。
// 同一 partition 永远只有一个 range（Catalog owner），禁止 hot+cold 同时入选。
type AuthoritativeRange struct {
	TargetID         string                       `json:"target_id"`
	Key              catalog.PartitionKey         `json:"key"`
	Owner            catalog.Owner                `json:"owner"`
	Tier             Tier                         `json:"tier"`
	DirID            string                       `json:"dir_id"`
	Generation       uint64                       `json:"generation"`
	ClosedVisibleSeq uint64                       `json:"closed_visible_seq"`
	Projection       *catalog.PublishedProjection `json:"projection,omitempty"`
}

// RangeKey 用于断言分区级不重叠。
func (r AuthoritativeRange) RangeKey() string {
	return r.Key.String()
}

// PartitionStatus 报告 Catalog 权威侧的物理就绪状态（可注入）。
type PartitionStatus struct {
	Queryable         bool
	HotReady          bool
	ColdConfigured    bool
	ColdReady         bool
	ArchiveConfigured bool
	ArchiveRestored   bool
	NotReadyReasons   []string
}

// StatusFunc 推导分区就绪状态；nil 时使用 DefaultPartitionStatus。
type StatusFunc func(key catalog.PartitionKey, rec *catalog.Record, ref catalog.QueryRef) PartitionStatus

// DefaultPartitionStatus 按 Catalog 权威推导默认就绪状态。
// 使用 catalog.Recover 作为启动恢复投影：FAILED_* 未切换时查询侧仍在源权威；
// 不发明未登记阈值。
func DefaultPartitionStatus(key catalog.PartitionKey, rec *catalog.Record, ref catalog.QueryRef) PartitionStatus {
	_ = key
	if !ref.OK {
		return PartitionStatus{Queryable: false, NotReadyReasons: []string{ReasonNoCatalogAuthority}}
	}
	rv := catalog.Recover(rec)
	archiveRestored := rv.Queryable && ref.Owner == catalog.OwnerArchive && !rec.RecoveryRequired &&
		ref.Projection != nil && ref.Projection.CoverageComplete &&
		(len(ref.Projection.ProjectionGenerations) > 0 || ref.Projection.ProjectionGeneration != "") && ref.Projection.QueryLocationDirID == ref.DirID &&
		ref.Projection.QueryGeneration == ref.Generation
	st := PartitionStatus{
		Queryable:         rv.Queryable,
		HotReady:          rv.Queryable && (rv.Owner == catalog.OwnerHot || ref.Owner == catalog.OwnerHot),
		ColdConfigured:    ref.Owner == catalog.OwnerCold || rec.TargetOwner == catalog.OwnerCold,
		ColdReady:         rv.Queryable && (rv.Owner == catalog.OwnerCold || ref.Owner == catalog.OwnerCold),
		ArchiveConfigured: ref.Owner == catalog.OwnerArchive || ref.Owner == catalog.OwnerCold,
		ArchiveRestored:   archiveRestored,
	}
	if ref.Owner == catalog.OwnerArchive && !archiveRestored {
		st.Queryable = false
		st.NotReadyReasons = append(st.NotReadyReasons, ReasonArchiveNotRestored)
	}
	if !rv.Queryable {
		if hasStr(rv.PartialReasons, ReasonRecoveryRequired) || hasStr(rv.PartialReasons, "FAILED_RETRYABLE") || hasStr(rv.PartialReasons, "FAILED_MANUAL") {
			st.NotReadyReasons = append(st.NotReadyReasons, ReasonRecoveryRequired)
		}
		if len(st.NotReadyReasons) == 0 {
			st.NotReadyReasons = append(st.NotReadyReasons, ReasonRangeUnavailable)
		}
	}
	return st
}

// CatalogSource 是 planner 依赖的 Catalog 只读视图。
// *catalog.Catalog 满足本接口。
type CatalogSource interface {
	Keys() []catalog.PartitionKey
	Get(key catalog.PartitionKey) (*catalog.Record, bool)
	OwnerForQuery(key catalog.PartitionKey) (catalog.QueryRef, bool)
}

// TierOf 将 catalog.Owner 映射为查询 Tier。
func TierOf(o catalog.Owner) Tier {
	switch o {
	case catalog.OwnerHot:
		return TierHot
	case catalog.OwnerCold:
		return TierCold
	case catalog.OwnerArchive:
		return TierArchive
	default:
		return Tier(string(o))
	}
}

// ClosedVisibleSeqOf 从 PublishedProjection 读取 closed_visible_seq。
// 读取方必须整对象使用 projection，不得与其他 manifest 拆开组合。
func ClosedVisibleSeqOf(proj *catalog.PublishedProjection, targetKey string) uint64 {
	if proj == nil || proj.ClosedVisibleSeq == nil {
		return 0
	}
	if v, ok := proj.ClosedVisibleSeq[targetKey]; ok {
		return v
	}
	if v, ok := proj.ClosedVisibleSeq["default"]; ok {
		return v
	}
	return 0
}
