import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMetricSeries, type MetricSeries } from '@/api/metrics'
import { Panel } from '@/components/ui/panel'
import { TimeSeriesChart, type ChartSeries } from '@/components/charts/TimeSeriesChart'
import { RangePicker, type MetricRange } from '@/components/charts/RangePicker'

/** 字节 → G/M/K。 */
function fmtBytes(b: number): string {
  if (!Number.isFinite(b) || b <= 0) return '0'
  if (b >= 1e9) return `${(b / 1024 / 1024 / 1024).toFixed(1)}G`
  if (b >= 1e6) return `${(b / 1024 / 1024).toFixed(0)}M`
  return `${(b / 1024).toFixed(0)}K`
}

/**
 * 实例监控段（FR-060/FR-061）：消费 /metrics/series（scope=instance）渲染历史曲线——
 * TPS/MSPT/堆/在线/线程/CPU + 分世界区块。探针不可用时段渲染为断点。
 */
export default function MetricsSegment({ instanceUuid }: { instanceUuid: string }) {
  const { t } = useTranslation()
  const [range, setRange] = useState<MetricRange>('24h')
  const { data, isLoading } = useMetricSeries({ scope: 'instance', targetId: instanceUuid, range })

  const series = data?.series ?? []
  const one = (metricKey: string, name: string): ChartSeries[] => {
    const s = series.find((x) => x.metricKey === metricKey && x.world === '')
    if (!s) return []
    return [{ key: metricKey, name, points: s.points.map((p) => ({ ts: p.ts, value: p.avg })) }]
  }
  // 分世界：同一 metricKey 下每个 world 一条线
  const byWorld = (metricKey: string): ChartSeries[] =>
    series
      .filter((x: MetricSeries) => x.metricKey === metricKey && x.world !== '')
      .map((s) => ({ key: s.world, name: s.world, points: s.points.map((p) => ({ ts: p.ts, value: p.avg })) }))

  if (isLoading) {
    return <div className="p-4 text-sm text-muted-foreground">{t('common.loading')}</div>
  }

  return (
    <div className="space-y-3 p-4">
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-semibold">{t('metrics.title')}</h3>
        <RangePicker value={range} onChange={setRange} />
      </div>
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-2 xl:grid-cols-3">
        <Panel title={t('metrics.tps')}>
          <TimeSeriesChart series={one('inst_tps', t('metrics.tps'))} height={160} valueFormatter={(v) => v.toFixed(1)} />
        </Panel>
        <Panel title={t('metrics.mspt')}>
          <TimeSeriesChart series={one('inst_mspt', t('metrics.mspt'))} height={160} valueFormatter={(v) => `${v.toFixed(1)}ms`} />
        </Panel>
        <Panel title={t('metrics.heap')}>
          <TimeSeriesChart series={one('inst_heap_used', t('metrics.heap'))} height={160} valueFormatter={fmtBytes} />
        </Panel>
        <Panel title={t('metrics.players')}>
          <TimeSeriesChart series={one('inst_players_online', t('metrics.players'))} height={160} valueFormatter={(v) => v.toFixed(0)} />
        </Panel>
        <Panel title={t('metrics.threads')}>
          <TimeSeriesChart series={one('inst_threads', t('metrics.threads'))} height={160} valueFormatter={(v) => v.toFixed(0)} />
        </Panel>
        <Panel title={t('metrics.cpu')}>
          <TimeSeriesChart series={one('inst_cpu_pct', t('metrics.cpu'))} height={160} valueFormatter={(v) => `${v.toFixed(0)}%`} />
        </Panel>
        <Panel title={t('metrics.worldChunks')} className="lg:col-span-2 xl:col-span-3">
          <TimeSeriesChart series={byWorld('world_loaded_chunks')} height={180} valueFormatter={(v) => v.toFixed(0)} />
        </Panel>
      </div>
    </div>
  )
}
