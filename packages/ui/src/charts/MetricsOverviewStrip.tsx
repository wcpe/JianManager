import { useTranslation } from 'react-i18next'
import { Sparkline } from './Sparkline'
import {
  buildSnapshots,
  catalogFor,
  formatterFor,
  type RawSeries,
} from '../lib/monitor-metrics'

const SPARK_COLORS = ['var(--chart-1)', 'var(--chart-2)', 'var(--chart-3)', 'var(--chart-4)', 'var(--chart-5)']

/**
 * 取最后一个有效样本的时间，并用有效点间隔的中位数估计采样周期。
 * 三个周期（至少90秒）无新值才标陈旧，避免5m/1h聚合桶被固定90秒误判。
 * 单点无法推断周期时仅显示采样时间，不声称是当前值。
 */
function sampleStatus(points: RawSeries['points']) {
  const times = [...new Set(points
    .filter((p) => p.value != null && Number.isFinite(p.value))
    .map((p) => Date.parse(p.ts))
    .filter(Number.isFinite))].sort((a, b) => a - b)
  const sampledAt = times.at(-1)
  const intervals = times.slice(1).map((time, i) => time - times[i]!).sort((a, b) => a - b)
  const mid = Math.floor(intervals.length / 2)
  const interval = intervals.length === 0 ? null : intervals.length % 2
    ? intervals[mid]! : (intervals[mid - 1]! + intervals[mid]!) / 2
  return {
    sampledAt: sampledAt === undefined ? null : new Date(sampledAt),
    stale: sampledAt !== undefined && interval !== null && Date.now() - sampledAt > Math.max(90_000, interval * 3),
  }
}

/**
 * 关键指标概览：显示最后一次有效采样值、时间和趋势缩略（sparkline）。
 * 纯消费已取的原始序列（raw），缺测显示 —。
 */
export function MetricsOverviewStrip({
  kind,
  raw,
  isLoading,
}: {
  kind: 'platform' | 'node' | 'instance'
  raw: RawSeries[]
  isLoading: boolean
}) {
  const { t } = useTranslation()
  const snapshots = buildSnapshots(catalogFor(kind), raw)
  const hasAny = snapshots.some((s) => s.current != null || s.points.length > 0)

  if (!isLoading && !hasAny) {
    return <p className="px-2 py-3 text-xs text-muted-foreground">{t('monitor.overview.empty')}</p>
  }

  return (
    <div className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-6">
      {snapshots.map((s, i) => {
        const fmt = formatterFor(s.format)
        const { sampledAt, stale } = sampleStatus(s.points)
        return (
          <div key={s.metricKey} className="rounded-lg border bg-card/40 p-2">
            <p className="truncate text-[11px] text-muted-foreground">{t(s.nameKey)}</p>
            <p className="mt-0.5 text-sm font-semibold tabular-nums">
              {s.current != null ? fmt(s.current) : '—'}
            </p>
            {sampledAt && (
              <p className="mt-0.5 text-[10px] text-muted-foreground">
                <span>{t('monitor.processSampledAt')}</span>{' '}
                <time dateTime={sampledAt.toISOString()}>{sampledAt.toLocaleString()}</time>
                {stale && <span className="ml-1 text-warning">{t('overview.freshness.stale')}</span>}
              </p>
            )}
            <div className="mt-1 h-7">
              <Sparkline
                points={s.points}
                color={SPARK_COLORS[i % SPARK_COLORS.length]}
                ariaLabel={`${t(s.nameKey)} trend`}
              />
            </div>
          </div>
        )
      })}
    </div>
  )
}
