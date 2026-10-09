import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { TimeSeriesChart, type ChartSeries } from '@jianmanager/ui/charts/TimeSeriesChart'
import { clampChartPoints, formatLatencyMs } from '@/lib/bot-load-metrics'
import type { BotLoadMetricPoint } from '@/lib/bot-load-types'
import { DisclaimerBanner } from '@/components/views/bot-load/session/DisclaimerBanner'

/** 时间窗（决定服务端 resolution，故由容器持有）。 */
export type SessionMetricsRange = '15m' | '1h' | 'all'

/** 各时间窗对应的服务端聚合粒度。 */
export const rangeResolution: Record<SessionMetricsRange, '15s' | '1m' | '5m'> = {
  '15m': '15s',
  '1h': '1m',
  all: '5m',
}

export interface SessionMetricsProps {
  /** 当前时间窗（上提：它决定服务端取数）。 */
  range: SessionMetricsRange
  onRangeChange: (range: SessionMetricsRange) => void
  /** 历史指标页（容器经 useBotLoadMetrics 取数）。 */
  data?: { items: BotLoadMetricPoint[] }
  isLoading?: boolean
  isError?: boolean
  /** 实时流指标（容器取数）；按 timestamp 与历史页合并，实时点覆盖同刻历史点。 */
  liveMetrics: BotLoadMetricPoint[]
}

/**
 * 会话指标（FR-371）：在线率 / 连接时延 / 调度延迟 / 命令发送 / 屏障释放 / 执行器健康
 * 六组图表，外加目标侧 legacy 指标区块（仅当数据里出现过才渲染）。
 */
export function SessionMetrics({
  range,
  onRangeChange,
  data,
  isLoading,
  isError,
  liveMetrics,
}: SessionMetricsProps) {
  const { t } = useTranslation()

  const points = useMemo(() => {
    const http = data?.items ?? []
    const merged = mergeMetrics(http, liveMetrics)
    return clampChartPoints(filterRange(merged, range), 1200)
  }, [data?.items, liveMetrics, range])

  const hasLegacy = points.some(
    (p) =>
      p.targetLegacy?.tps != null ||
      p.targetLegacy?.msptP95 != null ||
      p.targetLegacy?.onlinePlayers != null,
  )

  const onlineSeries: ChartSeries[] = [
    {
      key: 'online',
      name: t('botLoad.kpi.online'),
      color: 'var(--chart-1)',
      points: points.map((p) => ({
        ts: p.timestamp,
        value:
          p.counts.connected != null && p.counts.planned
            ? p.counts.connected / Math.max(1, p.counts.planned)
            : typeof p.counts.onlineRate === 'number'
              ? p.counts.onlineRate
              : null,
      })),
    },
  ]

  const connectSeries = latencySeries(points, 'connect')
  const lagSeries = latencySeries(points, 'scheduleLag')
  const barrierSeries = latencySeries(points, 'barrierReleaseLag')

  const cmdSeries: ChartSeries[] = [
    {
      key: 'sent',
      name: t('botLoad.kpi.cmdSent'),
      color: 'var(--chart-1)',
      points: points.map((p) => ({
        ts: p.timestamp,
        value: typeof p.command.sent === 'number' ? p.command.sent : null,
      })),
    },
    {
      key: 'failed',
      name: t('botLoad.kpi.cmdFailed'),
      color: 'var(--chart-4)',
      points: points.map((p) => ({
        ts: p.timestamp,
        value: typeof p.command.failed === 'number' ? p.command.failed : null,
      })),
    },
  ]

  const barrierConfigured = points.some(
    (p) =>
      p.latency.barrierReleaseLagP50Ms != null ||
      p.latency.barrierReleaseLagP95Ms != null ||
      (p.barrier?.arrived ?? 0) > 0,
  )

  const nodeIds = collectNodeIds(points).slice(0, 6)

  const legacySeries: ChartSeries[] = [
    {
      key: 'tps',
      name: 'TPS',
      color: 'var(--chart-1)',
      points: points.map((p) => ({
        ts: p.timestamp,
        value: p.targetLegacy?.tps ?? null,
      })),
    },
    {
      key: 'mspt',
      name: 'MSPT p95',
      color: 'var(--chart-2)',
      points: points.map((p) => ({
        ts: p.timestamp,
        value: p.targetLegacy?.msptP95 ?? null,
      })),
    },
  ]

  return (
    <div className="space-y-6" data-testid="session-metrics">
      <div className="flex flex-wrap items-center gap-2">
        {(['15m', '1h', 'all'] as const).map((k) => (
          <button
            key={k}
            type="button"
            className={`rounded-md border px-2.5 py-1 text-xs transition-colors ${range === k ? 'bg-primary text-primary-foreground' : 'bg-card hover:bg-accent/50'}`}
            onClick={() => onRangeChange(k)}
          >
            {t(`botLoad.range.${k}`)}
          </button>
        ))}
        {isLoading && <span className="text-xs text-muted-foreground">{t('common.loading')}</span>}
        {isError && (
          <span className="text-xs text-amber-700 dark:text-amber-300">{t('botLoad.metricsDegraded')}</span>
        )}
      </div>

      <DisclaimerBanner />

      <ChartBlock title={t('botLoad.chart.onlineRate')} summary={t('botLoad.chart.onlineRateSummary')}>
        <TimeSeriesChart series={onlineSeries} valueFormatter={(v) => `${(Number(v) * 100).toFixed(1)}%`} />
      </ChartBlock>

      <ChartBlock title={t('botLoad.chart.connectLatency')} summary={t('botLoad.chart.connectLatencySummary')}>
        <TimeSeriesChart series={connectSeries} valueFormatter={(v) => formatLatencyMs(Number(v))} />
      </ChartBlock>

      <ChartBlock title={t('botLoad.chart.scheduleLag')} summary={t('botLoad.chart.scheduleLagSummary')}>
        <TimeSeriesChart series={lagSeries} valueFormatter={(v) => formatLatencyMs(Number(v))} />
      </ChartBlock>

      <ChartBlock title={t('botLoad.chart.commandSend')} summary={t('botLoad.sentMeansChatOnly')}>
        <TimeSeriesChart series={cmdSeries} />
      </ChartBlock>

      {barrierConfigured && (
        <ChartBlock
          title={t('botLoad.chart.barrierReleaseLag')}
          summary={t('botLoad.chart.barrierReleaseLagSummary')}
        >
          <TimeSeriesChart series={barrierSeries} valueFormatter={(v) => formatLatencyMs(Number(v))} />
        </ChartBlock>
      )}

      <ChartBlock title={t('botLoad.chart.executorHealth')} summary={t('botLoad.chart.executorHealthSummary')}>
        {nodeIds.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t('botLoad.noData')}</p>
        ) : (
          <ul className="space-y-1 text-sm">
            {nodeIds.map((id) => {
              const last = [...points].reverse().find((p) => p.executor.some((e) => e.nodeId === id))
              const e = last?.executor.find((x) => x.nodeId === id)
              return (
                <li key={id} className="flex flex-wrap gap-3 border-b border-border/40 py-1">
                  <span className="font-medium">node-{id}</span>
                  <span>health={e?.health ?? '—'}</span>
                  <span>bots={e?.activeBots ?? '—'}</span>
                  <span>CPU={e?.cpuPercent != null ? `${e.cpuPercent}%` : '—'}</span>
                  <span>EL={e?.eventLoopP95Ms != null ? `${e.eventLoopP95Ms}ms` : '—'}</span>
                </li>
              )
            })}
          </ul>
        )}
      </ChartBlock>

      {hasLegacy && (
        <section className="rounded-lg border border-dashed bg-muted/20 p-4" data-testid="legacy-metrics">
          <h3 className="mb-1 text-sm font-semibold">{t('botLoad.legacyMetrics')}</h3>
          <p className="mb-3 text-xs text-muted-foreground">{t('botLoad.legacyMetricsHint')}</p>
          <TimeSeriesChart series={legacySeries} />
        </section>
      )}
    </div>
  )
}

function latencySeries(
  points: BotLoadMetricPoint[],
  kind: 'connect' | 'scheduleLag' | 'barrierReleaseLag',
): ChartSeries[] {
  const keys =
    kind === 'connect'
      ? (['connectP50Ms', 'connectP95Ms', 'connectP99Ms'] as const)
      : kind === 'scheduleLag'
        ? (['scheduleLagP50Ms', 'scheduleLagP95Ms', 'scheduleLagP99Ms'] as const)
        : (['barrierReleaseLagP50Ms', 'barrierReleaseLagP95Ms', 'barrierReleaseLagP99Ms'] as const)
  const labels = ['p50', 'p95', 'p99']
  return keys.map((k, i) => ({
    key: k,
    name: labels[i]!,
    color: `var(--chart-${i + 1})`,
    points: points.map((p) => ({
      ts: p.timestamp,
      value: p.latency[k],
    })),
  }))
}

function ChartBlock({
  title,
  summary,
  children,
}: {
  title: string
  summary: string
  children: React.ReactNode
}) {
  return (
    <section className="rounded-lg border bg-card p-4">
      <h3 className="text-sm font-semibold">{title}</h3>
      <p className="mb-3 text-xs text-muted-foreground">{summary}</p>
      {children}
    </section>
  )
}

/** 按 timestamp 合并历史页与实时流；实时点覆盖同刻历史点。 */
export function mergeMetrics(a: BotLoadMetricPoint[], b: BotLoadMetricPoint[]): BotLoadMetricPoint[] {
  const map = new Map<string, BotLoadMetricPoint>()
  for (const p of a) map.set(p.timestamp, p)
  for (const p of b) map.set(p.timestamp, p)
  return [...map.values()].sort((x, y) => x.timestamp.localeCompare(y.timestamp))
}

/** 相对最后一个点的滑动窗口过滤（`all` 不过滤）。 */
export function filterRange(points: BotLoadMetricPoint[], range: SessionMetricsRange): BotLoadMetricPoint[] {
  if (range === 'all' || points.length === 0) return points
  const last = Date.parse(points[points.length - 1]!.timestamp)
  const windowMs = range === '15m' ? 15 * 60_000 : 60 * 60_000
  const from = last - windowMs
  return points.filter((p) => Date.parse(p.timestamp) >= from)
}

/** 收集出现过的执行节点 id（升序）。 */
export function collectNodeIds(points: BotLoadMetricPoint[]): number[] {
  const s = new Set<number>()
  for (const p of points) for (const e of p.executor) s.add(e.nodeId)
  return [...s].sort((a, b) => a - b)
}
