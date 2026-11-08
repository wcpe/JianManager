package query

import (
	"context"
	"errors"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// ErrRangeClientUnimplemented 表示底层实现（含旧 Worker / 未接入 VL）不支持该能力。
// Service 必须映射为 LOG_UNSUPPORTED，不得返回空完整结果。
var ErrRangeClientUnimplemented = errors.New("query: range client unimplemented")

// ErrRangeQueryBudget 表示物理查询文本不能在批准引擎硬界内安全表达。
var ErrRangeQueryBudget = errors.New("query: physical query text budget exceeded")

// RangeQuery 是对单个 AuthoritativeRange 的执行参数。
type RangeQuery struct {
	RequestID   string
	TimeRange   TimeRange
	Filter      string
	Budget      Budget
	GroupBy     []string
	TimeBucket  string
	MetricField string
	// ClosedVisibleSeq 视图固定前缀；执行侧不得越过。
	ClosedVisibleSeq uint64
	// CursorSortKey 非空时从此排序键之后继续（逻辑分页，不把 watermark 当提示）。
	CursorSortKey *SortKey
}

// RangeResult 是单 range 执行结果。
type RangeResult struct {
	Items []logtypes.Event
	// Exhausted 表示本 range 在 closed_visible_seq 前缀内已读完。
	Exhausted bool
	// Bytes 是本次调用实际读取的字节数，包含预读与补查；不得超过已预留额度。
	Bytes uint64
	// SafeBeforeUTC 非空时，该时间组尚未完整；只能返回严格更新的行，不能跨组推进游标。
	SafeBeforeUTC string
	// CoverageReasons 执行侧追加的 partial 原因（如冷层未恢复）。
	CoverageReasons []string
	// QualityOverride 非空时覆盖默认质量。
	DuplicateQuality DuplicateQuality
	StatsQuality     StatsQuality
}

// RangeClient 是可注入的 range 执行客户端。
// foundation 不实现真实 VL HTTP；生产实现由后续 FR 接入。
type RangeClient interface {
	// Search 在权威 range 上检索规范事件。
	Search(ctx context.Context, rng AuthoritativeRange, q RangeQuery) (RangeResult, error)
	// Stats 在权威 range 上聚合逻辑事件集合。
	Stats(ctx context.Context, rng AuthoritativeRange, q RangeQuery) (StatsResult, error)
	// Tail 在权威 range 上取尾部；FOLLOW_LIVE 语义由实现声明是否支持。
	Tail(ctx context.Context, rng AuthoritativeRange, q RangeQuery, mode TailMode) (RangeResult, error)
}

// MergedRangeClient 是可选能力：把**同分布层级**的多个 range 合并为
// 一次粗筛查询执行（LogsQL `in()` 列出来源集合），行级按各 range 的权威范围
// 谓词（来源/代次/前缀水位）在内存归因过滤。
//
// 语义约定（实现者必须逐字遵守，谓词与单 range 查询等价）：
//   - rngs 必须同 Owner，且每个 range 都有可枚举的来源集合（否则调用方不应合并）；
//     **允许跨 UTC 天**——窗口取各天分区并集，超过存储层单查上限时由实现
//     自行切分为滑动段（见 vlrange.SearchMerged）；
//   - 返回 Items 已按各 range 的 scope/代次/水位过滤；同 event_id 同 hash 去重
//     （异 hash 保留并触发质量降级语义，与 CP 侧 dedupe 一致）；
//   - Exhausted 表示粗筛未截断（全部匹配行已取回）。
//
// 背景（2026-10-08 实测）：面板 24h 平台级查询经 162 目标逐 range 扇出
// 7.4-20.9s；合并后同数据 0.3-0.4s（VL `in()` 一次查询），故浏览路径启用。
// 未实现本接口时 Service 自动退化为逐 range 执行——语义不变，仅性能差异。
type MergedRangeClient interface {
	SearchMerged(ctx context.Context, rngs []AuthoritativeRange, q RangeQuery) (RangeResult, error)
}

// FieldsRangeClient 扩展查询字段枚举。实现必须读取同一 authoritative range，
// 不得从物理副本集合自行拼接结果。
type FieldsRangeClient interface {
	Fields(ctx context.Context, rng AuthoritativeRange, q RangeQuery) (FieldsResult, error)
}

// FacetsRangeClient 扩展受控维度聚合。
type FacetsRangeClient interface {
	Facets(ctx context.Context, rng AuthoritativeRange, q RangeQuery, dimensions []string, limit uint32) (RangeFacetsResult, error)
}

// StreamsRangeClient 扩展 stream 枚举与统计。
//
// # 「stream」在这里指什么（先读这段再用）
//
// 指 VictoriaLogs 的 **stream 概念**：入库时按「流标签集合」建立的索引。
// 我们的行上流标签只有 `log_source_id` 与 `stream` 两个（实测
// `stream_field_names` 恒为这两项），所以本接口的粒度是「(源, 流) 二元组」。
//
// **不是**「窗内出现过的字段全集」——那是 FieldsRangeClient（`/field_names`）。
// 两者名字相近、语义完全不同，别抓错。
//
// 实现必须读取同一 authoritative range（用 range 的 selector 约束作用域），
// 不得从物理副本集合自行拼接。
type StreamsRangeClient interface {
	// Streams 返回该 range 内的 stream 集合与条数。
	//
	// Complete=false 表示条数不可信（上游截断）；
	// 调用方不得把 Hits=0 当作「该流没有数据」。
	Streams(ctx context.Context, rng AuthoritativeRange, q RangeQuery) (RangeStreamsResult, error)
	// StreamFieldNames 返回 stream 的标签维度名。
	StreamFieldNames(ctx context.Context, rng AuthoritativeRange, q RangeQuery) (StreamFieldNamesResult, error)
	// StreamFieldValues 返回某 stream 标签维度的取值分布。
	StreamFieldValues(ctx context.Context, rng AuthoritativeRange, q RangeQuery, field string) (RangeStreamFieldValuesResult, error)
}

// FollowLiveRangeClient 提供与固定 View 分离的实时尾流语义。
type FollowLiveRangeClient interface {
	FollowLive(ctx context.Context, rng AuthoritativeRange, q RangeQuery) (RangeResult, error)
}

// StatsResult 是单 range 统计结果。count/sum/min/max/avg 输入是逻辑事件集合。
type StatsResult struct {
	Points           []StatsPoint
	Bytes            uint64
	CoverageReasons  []string
	StatsQuality     StatsQuality
	DuplicateQuality DuplicateQuality
}

// FieldsResult 是单 range 的字段枚举结果。
type FieldsResult struct {
	Fields          []string
	Bytes           uint64
	CoverageReasons []string
	Truncated       bool
}

// FacetValue 是单个受控维度的聚合值。
type FacetValue struct {
	Dimension string `json:"dimension"`
	Value     string `json:"value"`
	Count     uint64 `json:"count"`
}

// RangeFacetsResult 是单 range 的 Facets 结果。
type RangeFacetsResult struct {
	Values          []FacetValue
	Bytes           uint64
	CoverageReasons []string
	Truncated       bool
}

// StreamValue 是一条 stream（粒度：源 + 流名）与其在窗内的条数。
type StreamValue struct {
	// LogSourceID 与 Stream 已从 VL 原始串拆出；拆不出时为空（不猜测）。
	LogSourceID string `json:"log_source_id"`
	Stream      string `json:"stream"`
	// Raw 是 VL 原始 stream 串，无损保留以应对标签值含特殊字符的形态。
	Raw string `json:"raw,omitempty"`
	// Hits 在 Complete=true 时是精确条数。
	Hits uint64 `json:"hits"`
}

// RangeStreamsResult 是单 range 的 stream 枚举结果。
//
// Complete=false 表示条数不可信（上游截断）：调用方不得把 Hits=0 当断言。
type RangeStreamsResult struct {
	Streams         []StreamValue
	Bytes           uint64
	CoverageReasons []string
	Complete        bool
}

// StreamFieldNamesResult 是单 range 的 stream 标签维度名。
type StreamFieldNamesResult struct {
	Fields          []string
	Bytes           uint64
	CoverageReasons []string
}

// RangeStreamFieldValuesResult 是单 range 的 stream 标签维度取值分布。
type RangeStreamFieldValuesResult struct {
	Values          []FacetValue
	Bytes           uint64
	CoverageReasons []string
	Complete        bool
}

// StatsPoint 对应 LogStatsPoint；AVG 以 sum/count 二次聚合。
type StatsPoint struct {
	Dimensions map[string]string `json:"dimensions,omitempty"`
	Count      uint64            `json:"count"`
	Sum        float64           `json:"sum"`
	Min        float64           `json:"min"`
	Max        float64           `json:"max"`
}

// Avg 以 sum/count 二次聚合；count==0 时返回 0。
func (p StatsPoint) Avg() float64 {
	if p.Count == 0 {
		return 0
	}
	return p.Sum / float64(p.Count)
}

// UnimplementedRangeClient 是默认 stub：所有能力返回 Unimplemented。
// 用于旧 Worker 风格路径与“尚未接入 VL”时的显式失败。
type UnimplementedRangeClient struct{}

func (UnimplementedRangeClient) Search(context.Context, AuthoritativeRange, RangeQuery) (RangeResult, error) {
	return RangeResult{}, ErrRangeClientUnimplemented
}

func (UnimplementedRangeClient) Stats(context.Context, AuthoritativeRange, RangeQuery) (StatsResult, error) {
	return StatsResult{}, ErrRangeClientUnimplemented
}

func (UnimplementedRangeClient) Tail(context.Context, AuthoritativeRange, RangeQuery, TailMode) (RangeResult, error) {
	return RangeResult{}, ErrRangeClientUnimplemented
}

// FuncRangeClient 便于单测注入。
type FuncRangeClient struct {
	SearchFn func(ctx context.Context, rng AuthoritativeRange, q RangeQuery) (RangeResult, error)
	StatsFn  func(ctx context.Context, rng AuthoritativeRange, q RangeQuery) (StatsResult, error)
	TailFn   func(ctx context.Context, rng AuthoritativeRange, q RangeQuery, mode TailMode) (RangeResult, error)
}

func (c FuncRangeClient) Search(ctx context.Context, rng AuthoritativeRange, q RangeQuery) (RangeResult, error) {
	if c.SearchFn == nil {
		return RangeResult{}, ErrRangeClientUnimplemented
	}
	return c.SearchFn(ctx, rng, q)
}

func (c FuncRangeClient) Stats(ctx context.Context, rng AuthoritativeRange, q RangeQuery) (StatsResult, error) {
	if c.StatsFn == nil {
		return StatsResult{}, ErrRangeClientUnimplemented
	}
	return c.StatsFn(ctx, rng, q)
}

func (c FuncRangeClient) Tail(ctx context.Context, rng AuthoritativeRange, q RangeQuery, mode TailMode) (RangeResult, error) {
	if c.TailFn == nil {
		return RangeResult{}, ErrRangeClientUnimplemented
	}
	return c.TailFn(ctx, rng, q, mode)
}
