import { useTranslation } from 'react-i18next'
import { Sparkline } from '@/components/charts/Sparkline'
import {
  buildSnapshots,
  catalogFor,
  formatterFor,
  type RawSeries,
} from '@/lib/monitor-metrics'

const SPARK_COLORS = ['var(--chart-1)', 'var(--chart-2)', 'var(--chart-3)', 'var(--chart-4)', 'var(--chart-5)']

/**
 * 关键指标概览（FR-221）：一屏看选中 target 的关键指标当前值 + 趋势缩略（sparkline）。
 * 纯消费已取的原始序列（raw），按 target 的指标目录装配格子。当前值缺测显示 —。
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
        return (
          <div key={s.metricKey} className="rounded-lg border bg-card/40 p-2">
            <p className="truncate text-[11px] text-muted-foreground">{t(s.nameKey)}</p>
            <p className="mt-0.5 text-sm font-semibold tabular-nums">
              {s.current != null ? fmt(s.current) : '—'}
            </p>
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
