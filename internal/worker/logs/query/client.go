package query

import (
	"context"
	"errors"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// ErrRangeClientUnimplemented 表示底层实现（含旧 Worker / 未接入 VL）不支持该能力。
// Service 必须映射为 LOG_UNSUPPORTED，不得返回空完整结果。
var ErrRangeClientUnimplemented = errors.New("query: range client unimplemented")

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
	// Bytes 粗粒度成本累计（budget.MaxBytes 用）。
	Bytes uint64
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

// FieldsRangeClient 扩展查询字段枚举。实现必须读取同一 authoritative range，
// 不得从物理副本集合自行拼接结果。
type FieldsRangeClient interface {
	Fields(ctx context.Context, rng AuthoritativeRange, q RangeQuery) (FieldsResult, error)
}

// FacetsRangeClient 扩展受控维度聚合。
type FacetsRangeClient interface {
	Facets(ctx context.Context, rng AuthoritativeRange, q RangeQuery, dimensions []string, limit uint32) (RangeFacetsResult, error)
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
