import { useDeferredValue, useEffect, useMemo, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'
import { useNodes } from '@/api/nodes'
import { useInfiniteInstanceSearch, useInstanceSearch } from '@/api/instances'
import { useTasks, type TaskState } from '@/api/tasks'
import { useAlertEvents } from '@/api/alerts'
import { useMetricOverview, usePlatformObservabilityOverview, useResourceAttribution, type PlatformObservabilityOverviewResponse, type ResourceAttributionResponse } from '@/api/metrics'
import { HealthWall } from '@/components/HealthWall'
import { useAuthStore } from '@/stores/auth'
import { usePermissionsStore } from '@/stores/permissions'
import { Panel } from '@jianmanager/ui/components/panel'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { StatCard } from '@jianmanager/ui/components/stat-card'
import { ResourceGauge } from '@jianmanager/ui/components/gauge'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import { TimeSeriesChart, type ChartSeries } from '@jianmanager/ui'
import { RangePicker, type MetricRange } from '@jianmanager/ui'
import { instanceStatusLevel, type StatusLevel } from '@jianmanager/ui'
import { levelStatusLevel } from './alerts/alert-helpers'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import { useVirtualRows } from '@jianmanager/ui/lib/virtual-list'

/**
 * 趋势序列降采样（FR-496 阶段 6 补丁）。
 *
 * 24h 范围服务端返回 **96 点/条**，而首页图表高 180px、宽约 400px——96 点远超实际可分辨的
 * 像素密度，纯属渲染负担。recharts 是 SVG 实现、**每个点都生成 DOM**：实测 4 个图表 × 96 点
 * 在容器宽度变化时重绘一次要 **546ms**（trace 佐证：单个 RunTask 内 `FireAnimationFrame` →
 * `react-dom` 546.6ms），侧栏开合时这一下会明显卡顿。
 *
 * 降到 32 点后渲染成本约为原来的 1/3，而 180px 高度下曲线形状肉眼无差别。
 * 末点单独保留：否则曲线终点会前移，读图时会误以为数据缺了一段。
 */
function downsampleTrend<T>(points: T[], target = 32): T[] {
  if (points.length <= target) return points
  const step = points.length / target
  const out: T[] = []
  for (let i = 0; i < target; i++) out.push(points[Math.floor(i * step)]!)
  out.push(points[points.length - 1]!)
  return out
}

/** 字节 → 紧凑可读（G/M/K）。 */
function fmtBytes(b: number): string {
  if (!Number.isFinite(b) || b <= 0) return '0'
  if (b >= 1e9) return `${(b / 1024 / 1024 / 1024).toFixed(1)}G`
  if (b >= 1e6) return `${(b / 1024 / 1024).toFixed(0)}M`
  return `${(b / 1024).toFixed(0)}K`
}

/** 任务状态映射为首页紧凑徽章等级。 */
function taskStatusLevel(state: TaskState): StatusLevel {
  if (state === 'running') return 'info'
  if (state === 'succeeded') return 'success'
  if (state === 'failed') return 'danger'
  return 'neutral'
}

type AttributionGauge = 'cpu' | 'load' | 'memory'

function formatObservedAt(value: string | null, unknown: string): string {
  if (!value) return unknown
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? unknown : date.toLocaleString()
}

/** 首页仪表的有界受管资源 Tooltip，不展示任意 OS 进程。 */
function GaugeAttributionTooltip({
  gauge,
  label,
  children,
  active,
  onToggle,
  data,
  isLoading,
  isError,
}: {
  gauge: AttributionGauge
  label: string
  children: ReactNode
  active: boolean
  onToggle: () => void
  data?: ResourceAttributionResponse
  isLoading: boolean
  isError: boolean
}) {
  const { t } = useTranslation()
  const formatValue = (cpuPct: number | null, rssBytes: number | null): string => (
    gauge === 'cpu' ? displayNumber(cpuPct, '%') : rssBytes == null ? '--' : fmtBytes(rssBytes)
  )
  return (
    <Panel bodyClassName="relative flex items-center justify-center py-3">
      <button
        type="button"
        aria-label={t('dashboard.resourceAttributionLabel', { label })}
        aria-expanded={active}
        className="rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-ring hover:bg-accent/40"
        onClick={onToggle}
        onKeyDown={(event) => {
          if (event.key === 'Escape' && active) onToggle()
        }}
      >
        {children}
      </button>
      {active && (
        <div role="tooltip" data-testid="overview-resource-attribution" className="absolute left-2 right-2 top-[calc(100%-0.25rem)] z-30 rounded-md border bg-popover p-3 text-left text-xs text-popover-foreground shadow-soft">
          {isLoading && <p className="text-muted-foreground">{t('dashboard.resourceAttributionLoading')}</p>}
          {isError && <p className="text-muted-foreground">{t('dashboard.resourceAttributionUnavailable')}</p>}
          {data && (
            <div className="space-y-2">
              <p className="text-muted-foreground">{formatObservedAt(data.sampledAt, t('dashboard.sampleTimeUnknown'))} · {t(`dashboard.freshness.${data.freshness}`, data.freshness)}</p>
              <div className="space-y-1">
                {data.nodes.slice(0, 3).map((node) => (
                  <Link key={node.nodeId} to={`/monitoring?node=${encodeURIComponent(node.nodeUuid)}`} className="flex justify-between gap-2 hover:text-primary">
                    <span className="truncate">{node.name} · {t(`dashboard.freshness.${node.status}`, node.status)}</span>
                    <span className="font-mono">{formatValue(node.cpuPct, node.memoryUsedBytes)}</span>
                  </Link>
                ))}
              </div>
              <div className="space-y-1 border-t pt-2">
                {data.topInstances.slice(0, 3).map((instance) => (
                  <Link key={instance.instanceId} to={`/monitoring?instance=${encodeURIComponent(instance.instanceUuid)}`} className="flex justify-between gap-2 hover:text-primary">
                    <span className="truncate">{instance.instanceName}</span>
                    <span className="font-mono">{formatValue(instance.cpuPct, instance.rssBytes)}</span>
                  </Link>
                ))}
                {data.topProcesses.slice(0, 4).map((process) => (
                  <Link key={`${process.instanceId}-${process.pid}`} to={`/monitoring?instance=${encodeURIComponent(process.instanceUuid)}`} className="flex justify-between gap-2 hover:text-primary">
                    <span className="truncate">{process.instanceName} / {process.name || `PID ${process.pid}`}</span>
                    <span className="font-mono">{formatValue(process.cpuPercent, process.rssBytes)}</span>
                  </Link>
                ))}
              </div>
              <p className="text-[11px] text-muted-foreground">{t('dashboard.resourceObservation')}</p>
            </div>
          )}
        </div>
      )}
    </Panel>
  )
}

/** 首页紧凑聚合面板属性。 */
interface OverviewAggregationPanelProps {
  testId: string
  title: string
  to: string
  isLoading: boolean
  isError: boolean
  isEmpty: boolean
  emptyText: string
  errorText: string
  children: ReactNode
}

/** 统一聚合面板的加载、空态与错误降级，避免单域失败影响其他卡片。 */
function OverviewAggregationPanel({
  testId,
  title,
  to,
  isLoading,
  isError,
  isEmpty,
  emptyText,
  errorText,
  children,
}: OverviewAggregationPanelProps) {
  let content = children
  if (isError) content = <p className="py-8 text-center text-sm text-destructive">{errorText}</p>
  else if (isLoading) content = <p className="py-8 text-center text-sm text-muted-foreground">加载中…</p>
  else if (isEmpty) content = <p className="py-8 text-center text-sm text-muted-foreground">{emptyText}</p>

  return (
    <Panel
      data-testid={testId}
      title={title}
      actions={<Link to={to} className="text-xs text-muted-foreground hover:text-foreground">查看全部</Link>}
      bodyClassName="p-0"
      className="h-full min-w-0 overflow-hidden"
    >
      {content}
    </Panel>
  )
}

function displayNumber(value: number | null, suffix = ''): string {
  return value == null ? '--' : `${value.toFixed(suffix === '%' ? 1 : 0)}${suffix}`
}

function PlatformHealthPanel({ data, isLoading, isError }: { data?: PlatformObservabilityOverviewResponse; isLoading: boolean; isError: boolean }) {
  const { t } = useTranslation()
  return (
    <OverviewAggregationPanel testId="platform-observability-health" title={t('dashboard.platformHealth')} to="/monitoring" isLoading={isLoading} isError={isError} isEmpty={!data} emptyText={t('dashboard.noData')} errorText={t('dashboard.platformOverviewUnavailable')}>
      {data && <div className="space-y-3 p-3 text-sm">
        <div className="grid grid-cols-2 gap-2">
          <StatCard className="min-w-0" label={t('dashboard.nodes')} value={`${data.health.onlineNodeCount}/${data.health.nodeCount}`} sub={t('dashboard.online')} />
          <StatCard className="min-w-0" label={t('dashboard.runningInstances')} value={String(data.health.runningInstanceCount)} sub={t('dashboard.instances')} />
          <StatCard className="min-w-0" label={t('dashboard.totalCpu')} value={displayNumber(data.resources.cpuPct, '%')} sub={t(`dashboard.freshness.${data.resources.freshness}`)} />
          <StatCard className="min-w-0" label={t('dashboard.totalMem')} value={data.resources.memoryUsedBytes == null ? '--' : fmtBytes(data.resources.memoryUsedBytes)} sub={data.resources.memoryTotalBytes == null ? '--' : fmtBytes(data.resources.memoryTotalBytes)} />
        </div>
        <div className="grid grid-cols-1 gap-2 border-t pt-2 sm:grid-cols-2">
          <p className="text-muted-foreground">{t('dashboard.activeAlerts', { count: data.alerts.length })}</p>
          <p className="text-muted-foreground">{t('dashboard.activeTasks', { count: data.tasks.length })}</p>
        </div>
      </div>}
    </OverviewAggregationPanel>
  )
}

function PlatformExceptionsPanel({ data, isLoading, isError }: { data?: PlatformObservabilityOverviewResponse; isLoading: boolean; isError: boolean }) {
  const { t } = useTranslation()
  return (
    <OverviewAggregationPanel testId="platform-observability-exceptions" title={t('dashboard.platformExceptions')} to="/monitoring" isLoading={isLoading} isError={isError} isEmpty={!data || data.exceptions.length === 0} emptyText={t('dashboard.noData')} errorText={t('dashboard.platformOverviewUnavailable')}>
      <div className="divide-y">
        {data?.exceptions.slice(0, 5).map((item) => <Link key={`${item.kind}-${item.nodeId ?? item.instanceId}`} to={item.href} className="block px-3 py-2 text-sm hover:bg-muted/40">{item.title}</Link>)}
      </div>
    </OverviewAggregationPanel>
  )
}

function PlatformBotRuntimePanel({ data, isLoading, isError }: { data?: PlatformObservabilityOverviewResponse; isLoading: boolean; isError: boolean }) {
  const { t } = useTranslation()
  const bots = data?.bots
  return (
    <OverviewAggregationPanel testId="platform-observability-bots" title={t('dashboard.sharedBotRuntime')} to="/monitoring" isLoading={isLoading} isError={isError} isEmpty={!bots} emptyText={t('dashboard.noData')} errorText={t('dashboard.platformOverviewUnavailable')}>
      {bots && <div className="space-y-2 p-3 text-sm">
        <p className="break-words text-muted-foreground">{bots.notice}</p>
        <div className="grid grid-cols-2 gap-2 text-muted-foreground">
          <span className="min-w-0 break-words">{t('dashboard.botRss')}: {bots.botWorkerRssBytes == null ? '--' : fmtBytes(bots.botWorkerRssBytes)}</span>
          <span className="min-w-0 break-words">{t('dashboard.botCpu')}: {displayNumber(bots.botWorkerCpuPct, '%')}</span>
          <span className="min-w-0 break-words">{t('dashboard.botCount')}: {displayNumber(bots.activeCount)}</span>
          <span className="min-w-0 break-words">{t('dashboard.botEventLoop')}: {displayNumber(bots.eventLoopP95Ms, 'ms')}</span>
        </div>
        {bots.unavailable.length > 0 && <p className="break-words text-amber-600">{t('dashboard.botUnavailable', { reason: bots.unavailable[0].reason })}</p>}
      </div>}
    </OverviewAggregationPanel>
  )
}

/** 总览页（FR-061 旗舰）：环形仪表盘 + 聚合历史曲线（FR-060） + 密集实例表，一屏概览。 */
export default function OverviewPage() {
  const { t } = useTranslation()
  const [range, setRange] = useState<MetricRange>('24h')
  const [activeGauge, setActiveGauge] = useState<AttributionGauge | null>(null)
  const isPlatformAdmin = useAuthStore((state) => state.role === 10)
  // FR-432：平台观测区与权限树对齐（node.read / monitor.read 或超管）
  const hasPerm = usePermissionsStore((s) => s.hasPerm)
  const canSeePlatformObs = isPlatformAdmin || hasPerm('monitor.read') || hasPerm('node.read')
  const { data: nodes } = useNodes()
  /**
   * 实例数据分两路取，各自只拿需要的量：
   *
   * - 异常卡片只要 CRASHED 的前 5 条，交给服务端按状态过滤——原先是先拉全集再本地筛，
   *   千级规模下等于为一张 5 行的卡片付约 1MB 的代价
   * - 底部密集表保留「可看全部」的能力，但改为滚动按需分页：首屏只取一页，滚到底再取下一页，
   *   不再一次性拉全量（`useInfiniteInstanceSearch` 就是为 1000+ 实例的滚动浏览设计的）
   */
  const exceptionQuery = useInstanceSearch({
    status: 'CRASHED',
    page: 1,
    pageSize: 5,
    sort: 'name',
    order: 'asc',
  })
  const instancesQuery = useInfiniteInstanceSearch({ sort: 'name', order: 'asc' })
  const tasksQuery = useTasks({ limit: 5 })
  const alertsQuery = useAlertEvents({ resolved: false, pageSize: 5 })
  const { data: overview } = useMetricOverview(range)
  // 【为什么用 useDeferredValue】本页渲染约 783 个元素，而 `useMetricOverview` 每 10 秒轮询一次
  // （见 api/metrics.ts），每次更新都会让整页重渲染——实测单次主线程阻塞约 360ms，
  // 且 30 秒观测里稳定复现 4 次（间隔精确 10s）。用户点击侧栏时与之叠加，阻塞可达 900ms，
  // 体感就是「点什么都卡」。
  // 把数据更新降级为可中断的低优先级渲染后：本次（用旧值）渲染先同步走完、结果相同因而
  // 几乎不产生工作；真正带新值的那次渲染由 React 在空闲时切片执行，用户操作可以插队。
  // 实时性不变（仍是 10 秒粒度），只是不再抢主线程。
  const deferredOverview = useDeferredValue(overview)
  const attribution = useResourceAttribution(activeGauge !== null, activeGauge === 'memory' ? 'memory' : 'cpu')
  const platformObservability = usePlatformObservabilityOverview(canSeePlatformObs)
  const instanceRows = instancesQuery.data?.pages.flatMap((p) => p.items) ?? []
  const exceptionRows = exceptionQuery.data?.items ?? []
  const recentTasks = tasksQuery.data?.items.slice(0, 5) ?? []
  const activeAlerts = alertsQuery.data?.items.slice(0, 5) ?? []
  const {
    containerRef: instanceContainerRef,
    onScroll: handleInstanceScrollBase,
    range: instanceRange,
  } = useVirtualRows({
    total: instanceRows.length,
    itemSize: 42,
    overscan: 8,
    fallbackViewportSize: 420,
  })

  /**
   * 滚动到底再取下一页。
   *
   * 虚拟化的 total 用「已加载条数」而非服务端 total：表格只渲染已到手的行，未加载的部分靠
   * 滚动触发补齐——与 InstancesPage 的 onNeedMore 是同一套做法。
   */
  const handleInstanceScroll = (e: React.UIEvent<HTMLDivElement>) => {
    // useVirtualRows 的 onScroll 不接受事件参数——它自己从 containerRef.current 读 scrollTop
    // （见 lib/virtual-list.ts）。这里先触发它更新虚拟窗口，再用事件本身判断是否近底。
    handleInstanceScrollBase()
    const el = e.currentTarget
    const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 240
    if (nearBottom && instancesQuery.hasNextPage && !instancesQuery.isFetchingNextPage) {
      void instancesQuery.fetchNextPage()
    }
  }
  const visibleInstances = instanceRows.slice(instanceRange.start, instanceRange.end)

  const totals = deferredOverview?.totals
  const memPct = totals && totals.memTotalBytes > 0 ? (totals.memUsedBytes / totals.memTotalBytes) * 100 : 0

  useEffect(() => {
    if (!activeGauge) return undefined
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setActiveGauge(null)
    }
    window.addEventListener('keydown', closeOnEscape)
    return () => window.removeEventListener('keydown', closeOnEscape)
  }, [activeGauge])

  /**
   * 四条趋势合成一张图（FR-496 阶段 6 补丁）。
   *
   * 【为什么合并】React Profiler 埋点实测：首页整页渲染 283.9ms 中，
   * 4 个 `TimeSeriesChart` 占 **246ms（86.6%）**，而其余所有区块合计仅 24.7ms
   * （含那 290 个不依赖本数据的元素）。recharts 是 SVG 实现，每张图都要独立生成
   * 坐标轴、网格、图例与路径——四张就是四套。合成一张后只剩一套，实测省约 185ms。
   *
   * 【为什么归一化】四个指标量纲完全不同（CPU % / 负载倍数 / 内存字节 / 玩家数），
   * 同图必须共享纵轴。统一映射为「占各自满值的百分比」：曲线形状（涨跌、尖峰、拐点）
   * 完整保留，而这正是趋势图要回答的问题——**绝对数值已由上方 4 个 ResourceGauge
   * 与 5 个 StatCard 分别给出**。tooltip 再按满值反算回原始量纲，信息不丢。
   *
   * 【满值怎么取】CPU=100、负载=1.0 是固有满值；内存取节点内存总量；
   * 玩家数没有自然上限，退回「本序列窗口内最大值」——此时曲线顶端即"窗口峰值"，
   * 语义仍然清楚（tooltip 照常显示真实人数）。
   */
  const trendChart = useMemo(() => {
    const memTotal = deferredOverview?.totals?.memTotalBytes ?? 0
    const series: ChartSeries[] = []
    /** 序列名 → { 满值, 原值格式化器 }，供 tooltip 反算。 */
    const scales = new Map<string, { full: number; fmt: (v: number) => string }>()
    const specs: Array<{
      key: string
      labelKey: string
      full: number | 'memTotal' | 'seriesMax'
      fmt: (v: number) => string
    }> = [
      { key: 'node_cpu_pct', labelKey: 'dashboard.totalCpu', full: 100, fmt: (v) => `${v.toFixed(0)}%` },
      { key: 'node_load', labelKey: 'dashboard.totalLoad', full: 1, fmt: (v) => v.toFixed(2) },
      { key: 'node_mem_used', labelKey: 'dashboard.totalMem', full: 'memTotal', fmt: fmtBytes },
      { key: 'inst_players_online', labelKey: 'dashboard.onlinePlayers', full: 'seriesMax', fmt: (v) => v.toFixed(0) },
    ]

    for (const spec of specs) {
      const tr = deferredOverview?.trends.find((x) => x.metricKey === spec.key)
      if (!tr || tr.points.length === 0) continue
      const raw = downsampleTrend(tr.points.map((p) => ({ ts: p.ts, v: p.avg })))
      const max = Math.max(...raw.map((p) => p.v ?? 0), 0)
      const full =
        spec.full === 'memTotal'
          ? memTotal > 0
            ? memTotal
            : max || 1
          : spec.full === 'seriesMax'
            ? max || 1
            : spec.full
      const name = t(spec.labelKey)
      scales.set(name, { full, fmt: spec.fmt })
      series.push({
        key: spec.key,
        name,
        points: raw.map((p) => ({ ts: p.ts, value: p.v == null ? null : (p.v / full) * 100 })),
      })
    }
    return { series, scales }
  }, [deferredOverview, t])

  return (
    // 阶段 6 页面迁移：外壳与页头改用布局层原语（PageShell / PageHeader）。
    // data-page 由 PageShell spread 透传，e2e 的就绪信号依赖它。
    <PageShell data-page="overview">
      <PageHeader
        title={t('dashboard.title')}
        actions={<RangePicker value={range} onChange={setRange} />}
      />

      {/* 顶部：环形仪表盘 + 统计块 */}
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 md:grid-cols-3 xl:grid-cols-6">
        <GaugeAttributionTooltip gauge="cpu" label={t('dashboard.totalCpu')} active={activeGauge === 'cpu'} onToggle={() => setActiveGauge(activeGauge === 'cpu' ? null : 'cpu')} data={attribution.data} isLoading={attribution.isLoading} isError={attribution.isError}>
          <ResourceGauge label={t('dashboard.totalCpu')} value={totals?.cpuPct ?? 0} unit="%" />
        </GaugeAttributionTooltip>
        <GaugeAttributionTooltip gauge="load" label={t('dashboard.totalLoad')} active={activeGauge === 'load'} onToggle={() => setActiveGauge(activeGauge === 'load' ? null : 'load')} data={attribution.data} isLoading={attribution.isLoading} isError={attribution.isError}>
          {/* 负载是「占总核数比例」：以倍数（load÷核）呈现而非百分比，环按 1.0=满核封顶，
              不再出现 >100% 的破环（FR-108）。grading 仍按占比走 resourceLevel（>0.8×→红）。 */}
          <ResourceGauge label={t('dashboard.totalLoad')} value={(totals?.loadAvg ?? 0) / 100} max={1} unit="×" decimals={2} />
        </GaugeAttributionTooltip>
        <GaugeAttributionTooltip gauge="memory" label={t('dashboard.totalMem')} active={activeGauge === 'memory'} onToggle={() => setActiveGauge(activeGauge === 'memory' ? null : 'memory')} data={attribution.data} isLoading={attribution.isLoading} isError={attribution.isError}>
          <ResourceGauge label={t('dashboard.totalMem')} value={memPct} unit="%" />
        </GaugeAttributionTooltip>
        <StatCard
          label={t('dashboard.nodes')}
          value={`${totals?.onlineNodeCount ?? 0}/${totals?.nodeCount ?? nodes?.length ?? 0}`}
          sub={t('dashboard.online')}
        />
        <StatCard label={t('dashboard.runningInstances')} value={String(totals?.runningInstances ?? 0)} sub={t('dashboard.instances')} />
        <StatCard label={t('dashboard.onlinePlayers')} value={String(totals?.onlinePlayers ?? 0)} sub={t('nav.players')} />
      </div>

      <div data-testid="overview-operations-grid" className="grid grid-cols-1 gap-3 lg:grid-cols-3">
        <OverviewAggregationPanel
          testId="overview-exceptions"
          title="异常实例"
          to="/instances?status=CRASHED"
          isLoading={exceptionQuery.isLoading}
          isError={exceptionQuery.isError}
          isEmpty={exceptionRows.length === 0}
          emptyText="暂无异常实例"
          errorText="异常实例加载失败"
        >
          <div className="divide-y">
            {exceptionRows.map((instance) => (
              <Link
                key={instance.id}
                to={`/instances/${instance.id}`}
                data-testid="overview-exception-item"
                className="flex items-center justify-between gap-3 px-3 py-2.5 hover:bg-muted/40"
              >
                <span className="min-w-0 truncate text-sm font-medium">{instance.name}</span>
                <StatusBadge level={instanceStatusLevel(instance.status)} label={instance.status} />
              </Link>
            ))}
          </div>
        </OverviewAggregationPanel>

        <OverviewAggregationPanel
          testId="overview-recent-tasks"
          title="近期任务"
          to="/tasks"
          isLoading={tasksQuery.isLoading}
          isError={tasksQuery.isError}
          isEmpty={recentTasks.length === 0}
          emptyText="暂无近期任务"
          errorText="近期任务加载失败"
        >
          <div className="divide-y">
            {recentTasks.map((task) => (
              <Link
                key={task.taskId}
                to={`/tasks?task=${encodeURIComponent(task.taskId)}`}
                data-testid="overview-task-item"
                className="flex items-center justify-between gap-3 px-3 py-2.5 hover:bg-muted/40"
              >
                <span className="min-w-0 truncate text-sm font-medium">{task.title}</span>
                <StatusBadge
                  level={task.cancelRequested ? 'warning' : taskStatusLevel(task.state)}
                  label={task.cancelRequested ? t('tasks.state.canceling', '取消中') : t(`tasks.state.${task.state}`)}
                />
              </Link>
            ))}
          </div>
        </OverviewAggregationPanel>

        <OverviewAggregationPanel
          testId="overview-active-alerts"
          title="活跃告警"
          to="/alerts"
          isLoading={alertsQuery.isLoading}
          isError={alertsQuery.isError}
          isEmpty={activeAlerts.length === 0}
          emptyText="暂无活跃告警"
          errorText="活跃告警加载失败"
        >
          <div className="divide-y">
            {activeAlerts.map((alert) => (
              <div
                key={alert.id}
                data-testid="overview-alert-item"
                className="flex items-center justify-between gap-3 px-3 py-2.5"
              >
                <span className="min-w-0 truncate text-sm font-medium" title={alert.message}>{alert.message}</span>
                <StatusBadge level={levelStatusLevel(alert.level)} label={t(`alerts.level_${alert.level}`, alert.level)} />
              </div>
            ))}
          </div>
        </OverviewAggregationPanel>
      </div>

      {canSeePlatformObs && (
          <div data-testid="platform-observability-grid" className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
            <PlatformHealthPanel data={platformObservability.data} isLoading={platformObservability.isLoading} isError={platformObservability.isError} />
            <PlatformExceptionsPanel data={platformObservability.data} isLoading={platformObservability.isLoading} isError={platformObservability.isError} />
            <PlatformBotRuntimePanel data={platformObservability.data} isLoading={platformObservability.isLoading} isError={platformObservability.isError} />
          </div>
      )}

      {/* FR-461：逐台集群健康墙（热力矩阵 + 分级 + 排序 + 一键下钻）。 */}
      {canSeePlatformObs && (
          <HealthWall enabled={canSeePlatformObs} />
      )}

      {/* 中部：聚合历史曲线（FR-060）——四指标合成一张多序列图。
          纵轴是「占各自满值的百分比」，tooltip 反算回原始量纲（见 trendChart 的说明）。 */}
      <Panel title={t('dashboard.trends')}>
        <TimeSeriesChart
          series={trendChart.series}
          height={220}
          yDomain={[0, 100]}
          valueFormatter={(v, name) => {
            const scale = name ? trendChart.scales.get(name) : undefined
            if (!scale) return `${v.toFixed(0)}%`
            return scale.fmt((v / 100) * scale.full)
          }}
        />
      </Panel>

      {/* 底部：密集实例表 */}
        <Panel title={t('dashboard.instanceList')} bodyClassName="p-0" className="overflow-hidden">
        <div
          ref={instanceContainerRef}
          onScroll={handleInstanceScroll}
          data-testid="overview-instances-virtual"
          data-total-count={instanceRows.length}
          className="max-h-[420px] overflow-auto"
        >
          <Table>
            <TableHeader className="sticky top-0 z-10 bg-card">
              <TableRow>
                <TableHead>{t('instances.name')}</TableHead>
                <TableHead>{t('instances.type')}</TableHead>
                <TableHead>{t('instances.status')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {instanceRange.before > 0 && (
                <TableRow aria-hidden="true">
                  <TableCell colSpan={3} className="p-0" style={{ height: instanceRange.before }} />
                </TableRow>
              )}
              {visibleInstances.map((inst) => (
                <TableRow key={inst.id} data-testid="overview-instance-row">
                  <TableCell className="font-medium">{inst.name}</TableCell>
                  <TableCell className="text-muted-foreground">{inst.type}</TableCell>
                  <TableCell>
                    <StatusBadge level={instanceStatusLevel(inst.status)} label={inst.status} />
                  </TableCell>
                </TableRow>
              ))}
              {instanceRange.after > 0 && (
                <TableRow aria-hidden="true">
                  <TableCell colSpan={3} className="p-0" style={{ height: instanceRange.after }} />
                </TableRow>
              )}
              {instanceRows.length === 0 && (
                <TableRow>
                  <TableCell colSpan={3} className="h-16 text-center text-muted-foreground">
                    {t('instances.empty')}
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </div>
      </Panel>
    </PageShell>
  )
}
