package query

import (
	"fmt"
	"log/slog"
	"sort"
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
	// DefaultMaxBytes 是整个请求共用的默认扫描额度，不是每个 range 的额度。
	DefaultMaxBytes uint64 = 32 << 20
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
	ErrCodeArchivePurged       ErrorCode = "LOG_ARCHIVE_PURGED"
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

// Coverage partial / not-ready 可观测原因（契约 §6.3§3.1）。
const (
	ReasonColdMissing          = "COLD_MISSING"
	ReasonArchiveNotRestored   = "ARCHIVE_NOT_RESTORED"
	// ReasonArchivePurged 表示「该分区的归档副本已按保留期清除」。
	//
	// 为什么必须与 ARCHIVE_NOT_RESTORED 分开（而不是复用 LOG_ARCHIVE_MISSING）：
	// 两者对用户的**处置动作完全相反**——未恢复是「点一下 rehydrate 就能看」，
	// 已清除是「这段日志确实不在了，任何操作都取不回来」。
	// 复用 MISSING 会让界面把「已按保留期删除」显示成「历史日志尚未取回」，
	// 用户点恢复、恢复失败、再报错，最后还是要人来解释——而那时用户已经
	// 对「平台说数据在、其实不在」这件事失去了信任。
	ReasonArchivePurged = "ARCHIVE_PURGED"
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
	// ReasonFilteredOut 表示「粗筛取到的候选行全部被代次/水位等归因条件过滤掉」。
	//
	// 为什么要单独一个原因（2026-10-21 架构复审 D4）：合并粗筛先按 top-K 取最新 N 行、
	// 再在内存里按 `projection_generation` 白名单与 `ClosedVisibleSeq` 过滤。当这 N 行
	// 全部是旧代次副本（生产 hot 层确有此事）时，本页 0 行但枚举远未结束。
	// 若不上报该原因，coverage 会显示 complete，用户看到「0 条且无法翻页」而无任何解释。
	ReasonFilteredOut        = "CANDIDATES_FILTERED_OUT"
	ReasonUnauthorizedTarget = "UNAUTHORIZED_TARGET"
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

// safeTimePrefix 排除未完整时间组及更旧行，防止其它 owner 把全局游标带过缺失组。
func safeTimePrefix(events []logtypes.Event, fence string) []logtypes.Event {
	out := events[:0]
	for _, ev := range events {
		if compareUTCDESC(ev.EventTimeUTC, fence) < 0 {
			out = append(out, ev)
		}
	}
	return out
}

// LessSortKey 报告 a 是否应排在 b 之前（结果序列更前）。
func LessSortKey(a, b SortKey) bool { return CompareSortKey(a, b) < 0 }

func compareUTCDESC(a, b string) int {
	// RFC3339 的可选小数部分不能按字符串比较（"...00.1Z" 会排在
	// "...00Z" 之前或之后取决于标点字节），必须按时间值比较；空值排最后。
	if a == b {
		return 0
	}
	if a == "" {
		return 1
	}
	if b == "" {
		return -1
	}
	ta, errA := time.Parse(time.RFC3339Nano, a)
	tb, errB := time.Parse(time.RFC3339Nano, b)
	if errA == nil && errB == nil {
		switch {
		case ta.After(tb):
			return -1
		case ta.Before(tb):
			return 1
		default:
			return 0
		}
	}
	// 非规范时间仍保持确定性排序，避免排序比较器不传递。
	if a > b {
		return -1
	}
	return 1
}

// SortEvents 按冻结排序键就地排序（结果序列序：最新在前）。
//
// 复杂度是可用性约束（P2-7）：排序责任已完全落在这里（VL 侧只按 `_time desc` 出块，
// 全键次序由本函数与游标承接），而单次合并规模可达 10^5 行。旧实现的插入排序是 O(n²)：
// 实测 n=20000 的逆序输入耗时 23.4s（约 1.2µs/次全键比较），10^5 行量级足以把查询线程
// 拖死。改用 sort.SliceStable 后同规模约 10ms（接近 O(n log n)）。
//
// 必须用 SliceStable，不能用 Slice：全键相同的事件（同 source/record、仅 message 不同）
// 必须保持输入相对次序——分页游标取 items[limit-1]，不稳定序会让同一键的行在两次调用间
// 换位，进而重复或漏返。稳定序也是旧插入排序的既有语义，不得静默改变。
func SortEvents(items []logtypes.Event) {
	// 预先解出全键，避免每次比较都重建 SortKey（旧实现对每对比较都调用 SortKeyOf，
	// 是 O(n²) 之外的第二层放大）。
	keys := make([]SortKey, len(items))
	for i := range items {
		keys[i] = SortKeyOf(items[i])
	}
	// 排序索引置换、再按置换重排事件：键与事件必须同步移动——直接在 items 上排序会
	// 让 keys 与 items 错位（比较的键与被交换的元素不再对应），次序随即失真。
	perm := make([]int, len(items))
	for i := range perm {
		perm[i] = i
	}
	sort.SliceStable(perm, func(a, b int) bool {
		return LessSortKey(keys[perm[a]], keys[perm[b]])
	})
	sorted := make([]logtypes.Event, len(items))
	for i, p := range perm {
		sorted[i] = items[p]
	}
	copy(items, sorted)
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
	ClosedVisibleSeq   map[string]uint64 `json:"closed_visible_seq,omitempty"`
	Generations        map[string]uint64 `json:"generations,omitempty"`
	Owners             map[string]string `json:"owners,omitempty"`
	DirIDs             map[string]string `json:"dir_ids,omitempty"`
	ProjectionVersions map[string]string `json:"projection_versions,omitempty"`
	// Projections 与租约位置同次捕获；后页不能再拼接最新逐源列表。
	Projections      map[string]*catalog.PublishedProjection `json:"projections,omitempty"`
	LeaseIDs         map[string]string                       `json:"lease_ids,omitempty"`
	Transient        bool                                    `json:"-"`
	QueryableTargets map[string]bool                         `json:"queryable_targets,omitempty"`
	TargetCoverage     map[string]TargetCoverage             `json:"target_coverage,omitempty"`
	Quality            Quality                               `json:"quality"`
	CreatedAt        time.Time                               `json:"created_at"`
	ExpiresAt        time.Time                               `json:"expires_at"`
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
	// 已按保留期清除（FR-501 归档对象回收）：Queryable 恒为假，且原因不能是
	// ARCHIVE_NOT_RESTORED——那条建议（去 rehydrate）此时是死路。
	//
	// 这里必须**在** Recover 之前判并直接返回：Recover 只看 Owner/Dir，不认清除标记，
	// 若走正常分支，status 会带着一份「查询能力在归档」的形态进 classifyStatus，
	// 于是出现「已清除 + 请去恢复」这种自相矛盾的原因组合。
	if rec != nil && rec.Owner == catalog.OwnerArchive && rec.ArchiveEvidence.ArchiveObjectPurged() {
		return PartitionStatus{
			Queryable:         false,
			ArchiveConfigured: true,
			ArchiveRestored:   false,
			NotReadyReasons:   []string{ReasonArchivePurged},
		}
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
			// 观测日志（2026-09-30）：这里是「有记录但判定不可查」的兜底；
			// 打出原因与位置，就能终止“到底走的哪条分支”的猜测。
			slog.Info("DefaultPartitionStatus：记录不可查，归因 RANGE_UNAVAILABLE",
				"reasons", rv.PartialReasons, "owner", rec.Owner, "dir", rec.OwnerDirID,
				"state", string(rec.MigrationState), "recoveryRequired", rec.RecoveryRequired)
			st.NotReadyReasons = append(st.NotReadyReasons, ReasonRangeUnavailable)
		}
	}
	return st
}

// CatalogSource 将权威快照与租约作为同一原子读取操作。
// Planner 不自行修改 Catalog 记录，也不借当前 owner 读取历史位置。
type CatalogSource interface {
	Keys() []catalog.PartitionKey
	Get(key catalog.PartitionKey) (*catalog.Record, bool)
	OwnerForQuery(key catalog.PartitionKey) (catalog.QueryRef, bool)
	NewQueryViewID(now time.Time) (string, error)
	SnapshotAndLease(catalog.QuerySnapshotRequest) ([]catalog.QuerySnapshot, error)
	PinLeasedRead(viewID string, now time.Time) (func(), error)
	ReleaseQueryView(viewID string) error
	PruneQueryLeases(now time.Time) error
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
