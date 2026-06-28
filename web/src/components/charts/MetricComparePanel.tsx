import { useTranslation } from 'react-i18next'
import { MonitorChart } from '@/components/charts/MonitorChart'
import {
  buildCompareSeries,
  catalogFor,
  type RawSeries,
} from '@/lib/monitor-metrics'

/**
 * 多指标对比/叠加（FR-221）：用户从该 target 的指标目录里勾选多条指标，叠加到同一图对比趋势。
 * 跨指标量纲不同（TPS vs 字节 vs %），故对比图 Y 轴不约束（auto），以形状/趋势对比为主，
 * 绝对值看 hover 浮窗。选中集合受控由父级持有（随 target 切换重置）。
 */
export function MetricComparePanel({
  kind,
  raw,
  selected,
  onToggle,
  height = 220,
}: {
  kind: 'platform' | 'node' | 'instance'
  raw: RawSeries[]
  selected: string[]
  onToggle: (metricKey: string) => void
  height?: number
}) {
  const { t } = useTranslation()
  const catalog = catalogFor(kind)
  const plot = buildCompareSeries(selected, catalog, raw, (k) => t(k))

  return (
    <div className="space-y-3">
      <p className="text-[11px] text-muted-foreground">{t('monitor.compare.hint')}</p>
      <div className="flex flex-wrap gap-1.5" role="group" aria-label={t('monitor.compare.pick')}>
        {catalog.map((item) => {
          const on = selected.includes(item.metricKey)
          return (
            <button
              key={item.metricKey}
              type="button"
              aria-pressed={on}
              onClick={() => onToggle(item.metricKey)}
              className={
                'rounded-full border px-2.5 py-0.5 text-xs font-medium transition-colors ' +
                (on
                  ? 'border-primary bg-primary text-primary-foreground'
                  : 'text-muted-foreground hover:text-foreground')
              }
            >
              {t(item.nameKey)}
            </button>
          )
        })}
      </div>
      {selected.length === 0 ? (
        <div className="flex items-center justify-center text-xs text-muted-foreground" style={{ height }}>
          {t('monitor.compare.empty')}
        </div>
      ) : (
        <MonitorChart series={plot} height={height} emptyHint={t('common.noData')} />
      )}
    </div>
  )
}
