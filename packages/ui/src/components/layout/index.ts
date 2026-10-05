/**
 * 布局层（FR-496 阶段 3）：内容页布局规范的唯一出处。
 *
 * 依据《资源优先工作区》原型（`.tmp/设计/`）实测的页面骨架与结构词汇表实现。
 * 页面只应从这里取布局原语，不再各自手写 `space-y-*` / `grid-cols-*` 之类的骨架类名——
 * 这是「每个内容页布局规范统一」的落地方式。
 *
 * 标准内容页形状：
 * ```tsx
 * <PageShell variant="fixed">
 *   <PageHeader title="实例" count={1248} description="…" actions={…} />
 *   <ScopeBar scope="全部节点" note="数据截至 12:00">…</ScopeBar>
 *   <SummaryStrip>…</SummaryStrip>
 *   <Panel title="…" actions={…} footer={…}>
 *     <Toolbar>…</Toolbar>
 *     …
 *   </Panel>
 * </PageShell>
 * ```
 */
export { PageShell } from './PageShell'
export { PageHeader, type PageBreadcrumb } from './PageHeader'
// 骨架（FR-496 阶段 6 补丁）：路由 fallback 与页内数据区的统一占位，见 PageSkeleton.tsx 头注。
export { PageSkeleton, DataPanelSkeleton, ListSkeleton, type PageSkeletonVariant } from './PageSkeleton'
export { ScopeBar } from './ScopeBar'
export { SummaryStrip, SummaryItem } from './SummaryStrip'
export { Toolbar, ToolbarSpacer } from './Toolbar'
export { PlatformTabs, PlatformTab } from './PlatformTabs'
export { Segments, Segment } from './Segments'
export { TwoCol, ThreeCol, MetricGrid, MetricCell, CardsGrid, SettingsLayout } from './grids'
