import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMetricOverview, useMetricSeries } from '@/api/metrics'
import { Panel } from '@/components/ui/panel'
import { RangePicker, type MetricRange } from '@/components/charts/RangePicker'
import { MonitorChart } from '@/components/charts/MonitorChart'
import {
  buildChartSeries,
  formatterFor,
  type MetricChartDef,
  type RawSeries,
} from '@/lib/monitor-metrics'

/** 监控数据源：平台聚合（/metrics/overview）/ 单节点或单实例（/metrics/series）。 */
export type MonitorSource =
  | { kind: 'platform' }
  | { kind: 'node'; uuid: string }
  | { kind: 'instance'; uuid: string }

/**
 * 按当前图的 range + 数据源取原始序列。两条查询都无条件调用、用 enabled 互斥
 * （满足 rules-of-hooks；TanStack Query 对 disabled 查询不发请求）。各图独立 range，故按图调用。
 */
function useSourceSeries(source: MonitorSource, range: MetricRange): { series: RawSeries[]; isLoading: boolean } {
  const isPlatform = source.kind === 'platform'
  const targetId = isPlatform ? '' : source.uuid
  const scope = source.kind === 'instance' ? 'instance' : 'node'

  const overview = useMetricOverview(range)
  const seriesQ = useMetricSeries({ scope, targetId, range, enabled: !isPlatform && !!targetId })

  if (isPlatform) {
    const series: RawSeries[] = (overview.data?.trends ?? []).map((tr) => ({
      metricKey: tr.metricKey,
      points: tr.points.map((p) => ({ ts: p.ts, value: p.avg })),
    }))
    return { series, isLoading: overview.isLoading }
  }
  const series: RawSeries[] = (seriesQ.data?.series ?? []).map((s) => ({
    metricKey: s.metricKey,
    world: s.world,
    points: s.points.map((p) => ({ ts: p.ts, value: p.avg })),
  }))
  return { series, isLoading: seriesQ.isLoading }
}

/** 一张图卡：独立时间筛选 + brush + hover（FR-169）。 */
function MonitorChartCard({
  def,
  source,
  defaultRange,
  height,
}: {
  def: MetricChartDef
  source: MonitorSource
  defaultRange: MetricRange
  height: number
}) {
  const { t } = useTranslation()
  const [range, setRange] = useState<MetricRange>(defaultRange)
  const { series: raw, isLoading } = useSourceSeries(source, range)
  const plot = buildChartSeries(def, raw, (k) => t(k))

  return (
    <Panel title={t(def.titleKey)} hoverable actions={<RangePicker value={range} onChange={setRange} />}>
      {isLoading && plot.length === 0 ? (
        <div className="flex items-center justify-center text-xs text-muted-foreground" style={{ height: height + 20 }}>
          {t('common.loading')}
        </div>
      ) : (
        <MonitorChart
          series={plot}
          height={height}
          valueFormatter={formatterFor(def.format)}
          yDomain={def.yDomain ?? ['auto', 'auto']}
          emptyHint={t('common.noData')}
        />
      )}
    </Panel>
  )
}

/**
 * 监控骨架（FR-169，design §4.2）：平台/节点/实例共用。按 defs 渲染指标图网格，
 * 每图独立时间筛选 + 底部 brush 拖拽轴 + hover 浮窗。实时由各图查询的轮询（refetchInterval）承担，
 * 历史/实时并存——同一查询既回历史样本又随轮询前移末端。
 *
 * source：数据源描述（平台聚合 / 单节点 / 单实例），各图据此取数。
 */
export function MonitorSkeleton({
  defs,
  source,
  defaultRange = '24h',
  chartHeight = 190,
}: {
  defs: MetricChartDef[]
  source: MonitorSource
  defaultRange?: MetricRange
  chartHeight?: number
}) {
  return (
    <div className="grid grid-cols-1 gap-3 lg:grid-cols-2 xl:grid-cols-3">
      {defs.map((def) => (
        <MonitorChartCard key={def.id} def={def} source={source} defaultRange={defaultRange} height={chartHeight} />
      ))}
    </div>
  )
}
