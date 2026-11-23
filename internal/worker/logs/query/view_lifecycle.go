package query

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

const DefaultViewTTL = 5 * time.Minute

// Options 控制固定视图的绝对寿命；请求复用不会续期。
type Options struct {
	ViewTTL          time.Duration
	MaxViewLifetime  time.Duration
	MaxRetainedViews int
	Now              func() time.Time
	ResourceBudget   ResourceBudget
}

// NewPlannerWithOptions 提供可注入时钟与有界租约，默认最长五分钟。
func NewPlannerWithOptions(cat CatalogSource, status StatusFunc, opts Options) *Planner {
	if status == nil {
		status = DefaultPartitionStatus
	}
	if opts.MaxViewLifetime <= 0 || opts.MaxViewLifetime > DefaultViewTTL {
		opts.MaxViewLifetime = DefaultViewTTL
	}
	if opts.ViewTTL <= 0 || opts.ViewTTL > opts.MaxViewLifetime {
		opts.ViewTTL = opts.MaxViewLifetime
	}
	if opts.MaxRetainedViews <= 0 || opts.MaxRetainedViews > maxRetainedViews {
		opts.MaxRetainedViews = maxRetainedViews
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Planner{cat: cat, status: status, views: make(map[string]*QueryView),
		timers: make(map[string]*time.Timer), activeViews: make(map[string]time.Time), now: opts.Now, ttl: opts.ViewTTL,
		maxViews: opts.MaxRetainedViews}
}

// logLeaseCleanupError 保留原始失败响应，同时显式暴露后台租约回收的持久化错误。
func logLeaseCleanupError(err error) {
	if err != nil {
		slog.Warn("查询租约清理失败", "err", err)
	}
}

// ReleaseView 先禁止新读；Catalog 保留实际在途 pin，直到读取协程退出。
func (p *Planner) ReleaseView(viewID string) error {
	if p == nil || viewID == "" {
		return nil
	}
	p.mu.Lock()
	delete(p.views, viewID)
	delete(p.activeViews, viewID)
	if timer := p.timers[viewID]; timer != nil {
		timer.Stop()
		delete(p.timers, viewID)
	}
	p.mu.Unlock()
	if p.cat != nil {
		return p.cat.ReleaseQueryView(viewID)
	}
	return nil
}

// PinRead 的释放必须晚于真实 RangeClient 调用结束，而不是晚于调用方停止等待。
func (p *Planner) PinRead(viewID string) (func(), error) {
	if p == nil || p.cat == nil {
		return nil, fmt.Errorf("query: catalog unavailable")
	}
	p.mu.Lock()
	expires, ok := p.activeViews[viewID]
	closed := p.closed
	p.mu.Unlock()
	if closed || !ok || !p.now().Before(expires) {
		return nil, catalog.ErrQueryViewStale
	}
	return p.cat.PinLeasedRead(viewID, p.now())
}

// Close 用于 Worker 停机，既有在途读仍由其 pin 自行归还。
func (p *Planner) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	p.closed = true
	ids := make([]string, 0, len(p.activeViews))
	for id := range p.activeViews {
		ids = append(ids, id)
	}
	p.mu.Unlock()
	var first error
	for _, id := range ids {
		if err := p.ReleaseView(id); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// registerView 在交付前登记到期回收器；过期不依赖下一次查询触发。
func (p *Planner) registerView(view *QueryView) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return fmt.Errorf("query: planner closed")
	}
	if !view.Transient {
		p.pruneViewsLocked()
		p.views[view.ViewID] = cloneQueryView(view)
	}
	p.activeViews[view.ViewID] = view.ExpiresAt
	p.timers[view.ViewID] = time.AfterFunc(view.ExpiresAt.Sub(p.now()), func() {
		logLeaseCleanupError(p.ReleaseView(view.ViewID))
	})
	return nil
}

// releaseOnce 避免取消、失败与正常退出交错时重复释放同一 request pin。
func releaseOnce(release func()) func() {
	var once sync.Once
	return func() {
		if release != nil {
			once.Do(release)
		}
	}
}

func cloneProjection(proj *catalog.PublishedProjection) *catalog.PublishedProjection {
	if proj == nil {
		return nil
	}
	return (&catalog.Record{PublishedProjection: proj}).Clone().PublishedProjection
}

func cloneQueryView(view *QueryView) *QueryView {
	if view == nil {
		return nil
	}
	out := *view
	out.TargetIDs = append([]string(nil), view.TargetIDs...)
	out.ClosedVisibleSeq = cloneMap(view.ClosedVisibleSeq)
	out.Generations = cloneMap(view.Generations)
	out.Owners = cloneMap(view.Owners)
	out.DirIDs = cloneMap(view.DirIDs)
	out.ProjectionVersions = cloneMap(view.ProjectionVersions)
	out.LeaseIDs = cloneMap(view.LeaseIDs)
	out.QueryableTargets = cloneMap(view.QueryableTargets)
	out.TargetCoverage = cloneMap(view.TargetCoverage)
	for id, target := range out.TargetCoverage {
		target.Reasons = append([]string(nil), target.Reasons...)
		out.TargetCoverage[id] = target
	}
	out.Projections = make(map[string]*catalog.PublishedProjection, len(view.Projections))
	for id, projection := range view.Projections {
		out.Projections[id] = cloneProjection(projection)
	}
	return &out
}

func cloneMap[K comparable, V any](in map[K]V) map[K]V {
	out := make(map[K]V, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// freezeProjection 只在来源及其水位证据充分时升级旧格式，绝不借另一源或分区最大值。
func freezeProjection(key catalog.PartitionKey, projection *catalog.PublishedProjection) (*catalog.PublishedProjection, bool) {
	out := cloneProjection(projection)
	if out == nil || len(out.SourceProjections) > 0 {
		return out, true
	}
	generations := append([]string(nil), out.ProjectionGenerations...)
	if len(generations) == 0 && out.ProjectionGeneration != "" {
		generations = []string{out.ProjectionGeneration}
	}
	if len(generations) == 0 {
		// 无物理代次的历史诊断记录不在此伪造源范围。
		return out, true
	}
	if len(out.CoveredSourceGenerations) == 0 {
		// 只有分区 max 而无来源身份时，无法证明哪个源/代次拥有该前缀。
		return out, false
	}
	for _, source := range out.CoveredSourceGenerations {
		if source.LogSourceID == "" || source.SourceGeneration == "" {
			return out, false
		}
		prefix, known := legacyPrefix(out.ClosedVisibleSeq, source.LogSourceID+"/"+source.SourceGeneration, source.LogSourceID)
		if !known && len(out.CoveredSourceGenerations) == 1 {
			prefix, known = legacyPrefix(out.ClosedVisibleSeq, key.String(), "default")
		}
		if !known {
			return out, false
		}
		out.SourceProjections = append(out.SourceProjections, catalog.SourceProjection{
			SourceGenerationRef: source, ProjectionGenerations: append([]string(nil), generations...), ClosedVisibleSeq: prefix})
	}
	return out, true
}

func legacyPrefix(prefixes map[string]uint64, keys ...string) (uint64, bool) {
	for _, key := range keys {
		if prefix, ok := prefixes[key]; ok {
			return prefix, true
		}
	}
	return 0, false
}
