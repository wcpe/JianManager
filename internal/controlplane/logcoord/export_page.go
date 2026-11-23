package logcoord

import (
	"context"
	"fmt"
	"time"
)

// ExportPage 新鲜重读已有固定视图的当前逻辑页，通过同视图统计完整性闸后才交付附件。
// 不接受客户端行；入页游标和页大小沿用 Search，不能借导出重新选择最新集合。
func (c *Coordinator) ExportPage(ctx context.Context, query Query) (*ExportResult, error) {
	if query.ViewID == "" {
		return nil, fmt.Errorf("%w: page export requires viewId", ErrViewReuseMismatch)
	}
	view, ok := c.GetView(query.ViewID)
	if !ok {
		return nil, fmt.Errorf("%w: %s", errNoView, query.ViewID)
	}
	if err := authorizeViewReuse(view, query); err != nil {
		return nil, err
	}
	pageLimit := view.Budget.Limit
	if pageLimit <= 0 {
		pageLimit = DefaultPageLimit
	}
	if query.Budget.Limit != 0 && query.Budget.Limit != pageLimit {
		return nil, fmt.Errorf("%w: page export limit differs from view", ErrViewReuseMismatch)
	}
	if query.Budget.TimeoutMS > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(query.Budget.TimeoutMS)*time.Millisecond)
		defer cancel()
	}
	limits := c.exportBudget.normalize()
	totalBytes := limits.MaxBytes
	if query.Budget.MaxBytes > 0 && query.Budget.MaxBytes < totalBytes {
		totalBytes = query.Budget.MaxBytes
	}
	outputView := cloneView(view)
	outputView.Budget.Limit = pageLimit
	if limits.MaxRows < outputView.Budget.Limit {
		outputView.Budget.Limit = limits.MaxRows
	}
	outputView.Budget.MaxBytes = totalBytes

	// 同一 HTTP 请求内的页面与统计共用额度，零份额不得落成 Worker 的默认预算。
	reservations := reserveWorkerBudgets([]string{"page", "stats"}, QueryBudget{MaxBytes: totalBytes})
	if len(reservations) != 2 {
		coverage := Coverage{Complete: false, EnumerationState: EnumOpen, PartialReasons: []string{"BUDGET_EXCEEDED"}}
		return BuildExportWithCut(outputView, coverage, Quality{DuplicateQuality: DupUnresolved, StatsQuality: StatsQPartial}, nil, ExportCut{Budget: true}), nil
	}
	searchQuery := query
	searchQuery.Fast = false
	searchQuery.disableCache = true
	searchQuery.Budget.Limit = pageLimit
	searchQuery.Budget.MaxBytes = reservations["page"].MaxBytes
	page, err := c.Search(ctx, searchQuery)
	if err != nil {
		return nil, err
	}
	coverage := page.Coverage
	quality := page.Quality
	if !coverage.Complete || coverage.Pending || len(coverage.PartialReasons) > 0 || page.BudgetCut || QualityBlocksExport(quality) {
		return BuildExportWithCut(outputView, coverage, quality, page.Items, ExportCut{Budget: page.BudgetCut, Worker: page.BudgetCut}), nil
	}
	statsQuery := searchQuery
	statsQuery.Cursor = ""
	statsQuery.Budget.MaxBytes = reservations["stats"].MaxBytes
	stats, err := c.Stats(ctx, StatsQuery{Query: statsQuery})
	if err != nil {
		return nil, err
	}
	// Stats 没有分页：完整性与质量证明其输入是否齐全，不能把共享 View 的 OPEN 当作失败。
	// 失效和取消仍必须阻断，页面无需把整个 Search 集合枚举到尾。
	//
	// 2026-10-30：这行注释一直是对的，但旧实现把 `OPEN` 一律当不安全，
	// 与下面的 Stale/Cancelled 检查互相矛盾。现传 `openIsUnsafe=false` 让代码追上注释。
	mergeWorkerCoverage(&coverage, &stats.Coverage, false)
	quality.DuplicateQuality = worstDuplicateQuality(quality.DuplicateQuality, stats.Quality.DuplicateQuality)
	quality.StatsQuality = worstStatsQuality(quality.StatsQuality, stats.Quality.StatsQuality)
	if stats.Coverage.EnumerationState == EnumStale || stats.Coverage.EnumerationState == EnumCancelled {
		coverage.Complete = false
		coverage.PartialReasons = appendUniqueReason(coverage.PartialReasons, "PAGE_VALIDATION_INCOMPLETE")
	}
	return BuildExportWithCut(outputView, coverage, quality, page.Items, ExportCut{Budget: stats.BudgetCut, Worker: stats.BudgetCut}), nil
}
