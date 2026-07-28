import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useSearchParams } from 'react-router'
import { useNodes } from '@/api/nodes'
import { useInstances } from '@/api/instances'
import { useMetricOverview, useMetricSeries, useProcessTop, type ProcessTopItem } from '@/api/metrics'
import { Panel } from '@jianmanager/ui/components/panel'
import { RangePicker, ResolutionPicker, type MetricRange, type MetricResolution } from '@jianmanager/ui'
import { MonitorSkeleton, type MonitorSource } from '@jianmanager/ui'
import { MetricsOverviewStrip } from '@jianmanager/ui'
import { MetricComparePanel } from '@/components/charts/MetricComparePanel'
import { DrillTargetPicker, targetKey, type DrillTarget } from '@/components/charts/DrillTargetPicker'
import { useTargetSeries } from '@/components/charts/use-target-series'
import {
  NODE_CHART_DEFS,
  INSTANCE_CHART_DEFS,
  PLATFORM_CHART_DEFS,
  type MetricChartDef,
  type RawSeries,
} from '@jianmanager/ui'

/** 据 target 选用的图定义集（平台 4 / 节点 6 / 实例 6）。 */
function defsFor(kind: DrillTarget['kind']): MetricChartDef[] {
  if (kind === 'node') return NODE_CHART_DEFS
  if (kind === 'instance') return INSTANCE_CHART_DEFS
  return PLATFORM_CHART_DEFS
}

function targetFromSearch(searchParams: URLSearchParams): DrillTarget {
  const instance = searchParams.get('instance')
  if (instance) return { kind: 'instance', uuid: instance }
  const node = searchParams.get('node')
  if (node) return { kind: 'node', uuid: node }
  return { kind: 'platform' }
}

function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '--'
  const units = ['B', 'KiB', 'MiB', 'GiB']
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  return `${value.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`
}

function formatRate(read: number, write: number): string {
  const total = read + write
  return total > 0 ? `${formatBytes(total)}/s` : '--'
}

function formatTime(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '--' : date.toLocaleString()
}

function ProcessTopPanel({ rows }: { rows: ProcessTopItem[] }) {
  const { t } = useTranslation()
  return (
    <Panel title={t('monitor.processTop', '进程 TOP10')}>
      {rows.length === 0 ? (
        <p className="py-5 text-center text-sm text-muted-foreground">{t('monitor.processTopEmpty', '暂无进程采样')}</p>
      ) : (
        <div className="divide-y divide-border text-xs">
          {rows.map((row) => (
            <div
              key={`${row.pid}-${row.sampledAt}`}
              tabIndex={0}
              data-testid="process-top-row"
              aria-label={t('monitor.processTopRow', { pid: row.pid, name: row.name, defaultValue: `进程 ${row.pid} ${row.name}` })}
              className="group relative grid grid-cols-[72px_80px_1fr_80px_90px_90px] items-center gap-3 py-2 outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <span className="font-mono text-muted-foreground">PID {row.pid}</span>
              <span className="truncate text-muted-foreground" title={row.user || '--'}>{row.user || '--'}</span>
              <span className="min-w-0 truncate" title={row.commandSummary || row.name}>
                {row.commandSummary || row.name || '--'}
              </span>
              <span className="font-mono tabular-nums">{row.cpuPercent.toFixed(1)}%</span>
              <span className="font-mono tabular-nums">{formatBytes(row.rssBytes)}</span>
              <span className="font-mono tabular-nums">{formatRate(row.readBytesPerSec, row.writeBytesPerSec)}</span>
              <div
                data-testid="process-top-hover"
                className="pointer-events-none absolute right-0 top-[calc(100%-0.25rem)] z-30 w-80 rounded-md border bg-popover p-3 text-popover-foreground opacity-0 shadow-soft transition-opacity duration-150 group-hover:opacity-100 group-focus-within:opacity-100"
              >
                <div className="mb-2 min-w-0 truncate font-medium">{row.commandSummary || row.name || '--'}</div>
                <dl className="grid grid-cols-[92px_1fr] gap-x-3 gap-y-1 text-[11px]">
                  <dt className="text-muted-foreground">{t('monitor.processPid', 'PID')}</dt>
                  <dd className="font-mono tabular-nums">{row.pid}</dd>
                  <dt className="text-muted-foreground">{t('monitor.processUser', '用户')}</dt>
                  <dd>{row.user || '--'}</dd>
                  <dt className="text-muted-foreground">{t('monitor.processSampledAt', '采样时间')}</dt>
                  <dd>{formatTime(row.sampledAt)}</dd>
                  <dt className="text-muted-foreground">{t('monitor.processCpu', 'CPU')}</dt>
                  <dd className="font-mono tabular-nums">{row.cpuPercent.toFixed(1)}%</dd>
                  <dt className="text-muted-foreground">{t('monitor.processMemory', '内存')}</dt>
                  <dd className="font-mono tabular-nums">{formatBytes(row.rssBytes)}</dd>
                  <dt className="text-muted-foreground">{t('monitor.processIo', 'IO 读/写')}</dt>
                  <dd className="font-mono tabular-nums">
                    {formatBytes(row.readBytesPerSec)}/s · {formatBytes(row.writeBytesPerSec)}/s
                  </dd>
                </dl>
              </div>
            </div>
          ))}
        </div>
      )}
    </Panel>
  )
}

function useMonitorSeries(
  source: MonitorSource,
  range: MetricRange,
  resolution: MetricResolution,
): { series: RawSeries[]; isLoading: boolean } {
  const isPlatform = source.kind === 'platform'
  const targetId = isPlatform ? '' : source.uuid
  const scope = source.kind === 'instance' ? 'instance' : 'node'

  const overview = useMetricOverview(range, resolution)
  const seriesQ = useMetricSeries({ scope, targetId, range, resolution, enabled: !isPlatform && !!targetId })

  if (isPlatform) {
    const series: RawSeries[] = (overview.data?.trends ?? []).map((trend) => ({
      metricKey: trend.metricKey,
      points: trend.points.map((point) => ({ ts: point.ts, value: point.avg })),
    }))
    return { series, isLoading: overview.isLoading }
  }

  const series: RawSeries[] = (seriesQ.data?.series ?? []).map((item) => ({
    metricKey: item.metricKey,
    world: item.world,
    points: item.points.map((point) => ({ ts: point.ts, value: point.avg })),
  }))
  return { series, isLoading: seriesQ.isLoading }
}

/**
 * 统一监控页（FR-169 + FR-221 时序剖析增强）。在既有 FR-060/061 时序底座上加四个剖析维度：
 * 1. 关键指标概览——一屏看当前值 + sparkline 趋势缩略；
 * 2. 自定义聚合粒度 + 时间范围——页级 RangePicker + ResolutionPicker（auto/30s/5m/1h，ADR-013 三档）；
 * 3. 多指标对比/叠加——勾选多条指标在同一图叠加对比；
 * 4. 下钻——平台→节点→实例→世界 逐级钻取（面包屑 + 各层下拉）。
 * 仅纯前端消费既有 /metrics/series、/metrics/overview，不改后端、不另立 ADR（沿用 ADR-013）。
 */
export default function MonitoringPage() {
  const { t } = useTranslation()
  const [searchParams] = useSearchParams()
  const { data: nodes } = useNodes()
  const { data: instances } = useInstances()
  const [target, setTarget] = useState<DrillTarget>(() => targetFromSearch(searchParams))
  // 页级范围 + 粒度（驱动概览/对比；主图网格各图另有独立范围，但共享该页级粒度）。
  const [range, setRange] = useState<MetricRange>('24h')
  const [resolution, setResolution] = useState<MetricResolution>('auto')
  // 多指标对比的选中集合（随 target 切换重置，见 key）。
  const [compareSel, setCompareSel] = useState<string[]>([])

  const tKey = targetKey(target)
  const currentInstance = target.kind === 'instance' ? (instances ?? []).find((i) => i.uuid === target.uuid) : undefined
  const currentNodeUUID = target.kind === 'node' ? target.uuid : undefined
  const { data: processTop = [] } = useProcessTop({
    instanceId: currentInstance?.id,
    nodeId: currentNodeUUID,
    enabled: target.kind !== 'instance' || !!currentInstance,
  })

  // 概览/对比/主图共享的数据源描述（MonitorSource 与 SeriesTarget 同构）。
  const source: MonitorSource =
    target.kind === 'platform' ? { kind: 'platform' } : { kind: target.kind, uuid: target.uuid }
  // 概览/对比共享的原始序列（页级范围 + 粒度）。
  const { series: raw, isLoading } = useTargetSeries(source, range, resolution)

  // 当前实例的世界名列表（来自其分世界序列），供下钻到世界的下拉。
  const worlds = useMemo(() => {
    if (target.kind !== 'instance') return []
    const set = new Set<string>()
    for (const s of raw) if (s.world) set.add(s.world)
    return [...set].sort()
  }, [raw, target.kind])

  const worldFilter = target.kind === 'instance' ? target.world : undefined

  const toggleCompare = (metricKey: string) =>
    setCompareSel((prev) =>
      prev.includes(metricKey) ? prev.filter((k) => k !== metricKey) : [...prev, metricKey],
    )

  // target 切换时重置对比选择（不同 target 指标目录不同）。
  const onChangeTarget = (next: DrillTarget) => {
    if (next.kind !== target.kind) setCompareSel([])
    setTarget(next)
  }

  return (
    <div data-page="monitoring" className="jm-page-stack space-y-4">
      <div className="jm-page-header flex-wrap">
        <h1 className="jm-page-title">{t('monitor.title')}</h1>
        <div className="flex flex-wrap items-center gap-2">
          <ResolutionPicker value={resolution} onChange={setResolution} />
          <RangePicker value={range} onChange={setRange} />
        </div>
      </div>

      {/* 下钻：平台 → 节点 → 实例 → 世界 */}
      <Panel bodyClassName="px-3 py-2">
        <DrillTargetPicker
          target={target}
          onChange={onChangeTarget}
          nodes={nodes ?? []}
          instances={instances ?? []}
          worlds={worlds}
        />
      </Panel>

      {/* 关键指标概览：当前值 + sparkline 趋势缩略 */}
      <Panel title={t('monitor.overview.title')}>
        <MetricsOverviewStrip kind={target.kind} raw={raw} isLoading={isLoading} />
      </Panel>

      {/* 多指标对比/叠加 */}
      <Panel title={t('monitor.compare.title')}>
        <MetricComparePanel kind={target.kind} raw={raw} selected={compareSel} onToggle={toggleCompare} />
      </Panel>

      <ProcessTopPanel rows={processTop} />

      <Panel bodyClassName="px-3 py-2 text-[11px] text-muted-foreground">{t('monitor.hint')}</Panel>

      {/* 主图网格（每图独立时间筛选 + brush + hover），共享页级粒度与下钻世界聚焦 */}
      <MonitorSkeleton
        key={tKey}
        defs={defsFor(target.kind)}
        source={source}
        defaultRange={range}
        resolution={resolution}
        worldFilter={worldFilter}
        useSeries={useMonitorSeries}
      />
    </div>
  )
}
