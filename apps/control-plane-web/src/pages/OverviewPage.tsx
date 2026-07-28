import { useEffect, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'
import { useNodes } from '@/api/nodes'
import { useInstances } from '@/api/instances'
import { useTasks, type TaskState } from '@/api/tasks'
import { useAlertEvents } from '@/api/alerts'
import { useMetricOverview, usePlatformObservabilityOverview, useResourceAttribution, type PlatformObservabilityOverviewResponse, type ResourceAttributionResponse } from '@/api/metrics'
import { useAuthStore } from '@/stores/auth'
import { Panel } from '@jianmanager/ui/components/panel'
import { StatCard } from '@jianmanager/ui/components/stat-card'
import { ResourceGauge } from '@jianmanager/ui/components/gauge'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import { TimeSeriesChart, type ChartSeries } from '@jianmanager/ui'
import { RangePicker, type MetricRange } from '@jianmanager/ui'
import { instanceStatusLevel, type StatusLevel } from '@jianmanager/ui'
import { levelStatusLevel } from './alerts/alert-helpers'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import { useVirtualRows } from '@/lib/virtual-list'

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
        className="rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
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
      className="h-full overflow-hidden"
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
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
          <StatCard label={t('dashboard.nodes')} value={`${data.health.onlineNodeCount}/${data.health.nodeCount}`} sub={t('dashboard.online')} />
          <StatCard label={t('dashboard.runningInstances')} value={String(data.health.runningInstanceCount)} sub={t('dashboard.instances')} />
          <StatCard label={t('dashboard.totalCpu')} value={displayNumber(data.resources.cpuPct, '%')} sub={t(`dashboard.freshness.${data.resources.freshness}`)} />
          <StatCard label={t('dashboard.totalMem')} value={data.resources.memoryUsedBytes == null ? '--' : fmtBytes(data.resources.memoryUsedBytes)} sub={data.resources.memoryTotalBytes == null ? '--' : fmtBytes(data.resources.memoryTotalBytes)} />
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
        <p className="text-muted-foreground">{bots.notice}</p>
        <div className="grid grid-cols-2 gap-2 text-muted-foreground sm:grid-cols-4">
          <span>{t('dashboard.botRss')}: {bots.botWorkerRssBytes == null ? '--' : fmtBytes(bots.botWorkerRssBytes)}</span>
          <span>{t('dashboard.botCpu')}: {displayNumber(bots.botWorkerCpuPct, '%')}</span>
          <span>{t('dashboard.botCount')}: {displayNumber(bots.activeCount)}</span>
          <span>{t('dashboard.botEventLoop')}: {displayNumber(bots.eventLoopP95Ms, 'ms')}</span>
        </div>
        {bots.unavailable.length > 0 && <p className="text-amber-600">{t('dashboard.botUnavailable', { reason: bots.unavailable[0].reason })}</p>}
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
  const { data: nodes } = useNodes()
  const instancesQuery = useInstances()
  const tasksQuery = useTasks({ limit: 5 })
  const alertsQuery = useAlertEvents({ resolved: false, pageSize: 5 })
  const { data: overview } = useMetricOverview(range)
  const attribution = useResourceAttribution(activeGauge !== null, activeGauge === 'memory' ? 'memory' : 'cpu')
  const platformObservability = usePlatformObservabilityOverview(isPlatformAdmin)
  const instanceRows = instancesQuery.data ?? []
  const exceptionRows = instanceRows.filter((instance) => instance.status === 'CRASHED').slice(0, 5)
  const recentTasks = tasksQuery.data?.items.slice(0, 5) ?? []
  const activeAlerts = alertsQuery.data?.items.slice(0, 5) ?? []
  const {
    containerRef: instanceContainerRef,
    onScroll: handleInstanceScroll,
    range: instanceRange,
  } = useVirtualRows({
    total: instanceRows.length,
    itemSize: 42,
    overscan: 8,
    fallbackViewportSize: 420,
  })
  const visibleInstances = instanceRows.slice(instanceRange.start, instanceRange.end)

  const totals = overview?.totals
  const memPct = totals && totals.memTotalBytes > 0 ? (totals.memUsedBytes / totals.memTotalBytes) * 100 : 0

  useEffect(() => {
    if (!activeGauge) return undefined
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setActiveGauge(null)
    }
    window.addEventListener('keydown', closeOnEscape)
    return () => window.removeEventListener('keydown', closeOnEscape)
  }, [activeGauge])

  /** 据 metricKey 取一条聚合趋势并映射为图表序列。 */
  const trend = (metricKey: string, name: string): ChartSeries[] => {
    const tr = overview?.trends.find((x) => x.metricKey === metricKey)
    if (!tr) return []
    return [{ key: metricKey, name, points: tr.points.map((p) => ({ ts: p.ts, value: p.avg })) }]
  }

  return (
    <div data-page="overview" className="jm-page-stack space-y-4">
      <div className="jm-page-header">
        <h1 className="jm-page-title">{t('dashboard.title')}</h1>
        <RangePicker value={range} onChange={setRange} />
      </div>

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
          isLoading={instancesQuery.isLoading}
          isError={instancesQuery.isError}
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

      {isPlatformAdmin && (
        <div data-testid="platform-observability-grid" className="grid grid-cols-1 gap-3 lg:grid-cols-3">
          <PlatformHealthPanel data={platformObservability.data} isLoading={platformObservability.isLoading} isError={platformObservability.isError} />
          <PlatformExceptionsPanel data={platformObservability.data} isLoading={platformObservability.isLoading} isError={platformObservability.isError} />
          <PlatformBotRuntimePanel data={platformObservability.data} isLoading={platformObservability.isLoading} isError={platformObservability.isError} />
        </div>
      )}

      {/* 中部：聚合历史曲线（FR-060） */}
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
        <Panel title={t('dashboard.cpuTrend')}>
          <TimeSeriesChart series={trend('node_cpu_pct', t('dashboard.totalCpu'))} height={180} valueFormatter={(v) => `${v.toFixed(0)}%`} />
        </Panel>
        <Panel title={t('dashboard.loadTrend')}>
          <TimeSeriesChart series={trend('node_load', t('dashboard.totalLoad'))} height={180} valueFormatter={(v) => v.toFixed(2)} />
        </Panel>
        <Panel title={t('dashboard.memTrend')}>
          <TimeSeriesChart series={trend('node_mem_used', t('dashboard.totalMem'))} height={180} valueFormatter={fmtBytes} />
        </Panel>
        <Panel title={t('dashboard.playersTrend')}>
          <TimeSeriesChart series={trend('inst_players_online', t('dashboard.onlinePlayers'))} height={180} valueFormatter={(v) => v.toFixed(0)} />
        </Panel>
      </div>

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
    </div>
  )
}
