package vlrange

import (
	"context"
	"fmt"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
)

// Tiered 把每个查询区段路由到它所属的受管 VL 实例。
//
// 2026-10-29：冷热双层已废除，只剩一个受管实例 ⇒ 这里不再有「选层」这回事，
// 所有区段都落到 Main。保留这个类型与 Ready 回调的理由：
//   - 它是「区段归属 → 物理接收端」的唯一路由点，删掉就要把 Ready 探活逻辑塞进每个调用点；
//   - 调用方（query 包）只面对一个接口，将来若再引入第二层无需改调用面。
//
// **不再做的事**：按 Owner 分支选不同客户端；缺失层报 range 失败；跳层回退。
type Tiered struct {
	Main *Client
	// Ready 每次读取检查实例健康，不把 HTTP 健康冒充 Catalog 发布就绪。
	Ready func(context.Context, catalog.Owner) error
}

func (t *Tiered) forRange(ctx context.Context, rng query.AuthoritativeRange) (*Client, error) {
	if t == nil || t.Main == nil {
		return nil, fmt.Errorf("vlrange: managed VictoriaLogs client is unavailable")
	}
	if t.Ready != nil {
		if err := t.Ready(ctx, rng.Owner); err != nil {
			return nil, fmt.Errorf("vlrange: authoritative owner %s unavailable: %w", rng.Owner, err)
		}
	}
	return t.Main, nil
}

func (t *Tiered) Search(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery) (query.RangeResult, error) {
	c, err := t.forRange(ctx, rng)
	if err != nil {
		return query.RangeResult{}, err
	}
	return c.Search(ctx, rng, q)
}

// SearchMerged 合并检索：全部区段都在同一个受管实例里，整组直接交给它。
func (t *Tiered) SearchMerged(ctx context.Context, rngs []query.AuthoritativeRange, q query.RangeQuery) (query.RangeResult, error) {
	if len(rngs) == 0 {
		return query.RangeResult{}, fmt.Errorf("vlrange: empty merged range set")
	}
	c, err := t.forRange(ctx, rngs[0])
	if err != nil {
		return query.RangeResult{}, err
	}
	return c.SearchMerged(ctx, rngs, q)
}

func (t *Tiered) Stats(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery) (query.StatsResult, error) {
	c, err := t.forRange(ctx, rng)
	if err != nil {
		return query.StatsResult{}, err
	}
	return c.Stats(ctx, rng, q)
}

func (t *Tiered) Tail(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery, mode query.TailMode) (query.RangeResult, error) {
	c, err := t.forRange(ctx, rng)
	if err != nil {
		return query.RangeResult{}, err
	}
	return c.Tail(ctx, rng, q, mode)
}

func (t *Tiered) Fields(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery) (query.FieldsResult, error) {
	c, err := t.forRange(ctx, rng)
	if err != nil {
		return query.FieldsResult{}, err
	}
	return c.Fields(ctx, rng, q)
}

func (t *Tiered) Facets(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery, dimensions []string, limit uint32) (query.RangeFacetsResult, error) {
	c, err := t.forRange(ctx, rng)
	if err != nil {
		return query.RangeFacetsResult{}, err
	}
	return c.Facets(ctx, rng, q, dimensions, limit)
}

func (t *Tiered) FollowLive(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery) (query.RangeResult, error) {
	c, err := t.forRange(ctx, rng)
	if err != nil {
		return query.RangeResult{}, err
	}
	return c.FollowLive(ctx, rng, q)
}

// Streams 转发到受管实例（流的枚举与条数）。
//
// # 为什么这三个转发是必需的（2026-11-05 真机踩到）
//
// 生产装配走的是 `&vlrange.Tiered{Main: client}`（见 apps/worker/log_rpc_wiring），
// **不是**直接丢 `*vlrange.Client`。而 Service 是用类型断言
// `s.client.(StreamsRangeClient)` 探测能力的：
//
//	Tiered 没有 Streams ⇒ 断言失败 ⇒ LOG_UNSUPPORTED「streams unimplemented on this worker」
//
// 表现是 CP 侧 `coverage.targets[].reasons=["streams_unsupported"]`，而
// 单测全绿（单测直接用 `*Client`，跑的路径与生产不同）。
//
// 教训：**新增可选能力接口时，所有包装层都必须同步转发**，并靠下面的 var 断言兜住。
func (t *Tiered) Streams(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery) (query.RangeStreamsResult, error) {
	c, err := t.forRange(ctx, rng)
	if err != nil {
		return query.RangeStreamsResult{}, err
	}
	return c.Streams(ctx, rng, q)
}

func (t *Tiered) StreamFieldNames(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery) (query.StreamFieldNamesResult, error) {
	c, err := t.forRange(ctx, rng)
	if err != nil {
		return query.StreamFieldNamesResult{}, err
	}
	return c.StreamFieldNames(ctx, rng, q)
}

func (t *Tiered) StreamFieldValues(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery, field string) (query.RangeStreamFieldValuesResult, error) {
	c, err := t.forRange(ctx, rng)
	if err != nil {
		return query.RangeStreamFieldValuesResult{}, err
	}
	return c.StreamFieldValues(ctx, rng, q, field)
}

var _ query.RangeClient = (*Tiered)(nil)
var _ query.MergedRangeClient = (*Tiered)(nil)
var _ query.FieldsRangeClient = (*Tiered)(nil)
var _ query.FacetsRangeClient = (*Tiered)(nil)

// StreamsRangeClient 断言必须写出来：漏掉转发时靠它当场编译失败，
// 而不是等到生产上只看到一句 streams unimplemented。
var _ query.StreamsRangeClient = (*Tiered)(nil)
var _ query.FollowLiveRangeClient = (*Tiered)(nil)
