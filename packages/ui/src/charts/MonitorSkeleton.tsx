import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Panel } from '../components/panel'
import { RangePicker, type MetricRange, type MetricResolution } from './RangePicker'
import { MonitorChart } from './MonitorChart'
import {
  buildChartSeries,
  formatterFor,
  type MetricChartDef,
  type RawSeries,
} from '../lib/monitor-metrics'

/** 监控数据源：平台聚合（/metrics/overview）/ 单节点或单实例（/metrics/series）。 */
export type MonitorSource =
  | { kind: 'platform' }
  | { kind: 'node'; uuid: string }
  | { kind: 'instance'; uuid: string }

export type MonitorSeriesHook = (
  source: MonitorSource,
  range: MetricRange,
  resolution: MetricResolution,
) => { series: RawSeries[]; isLoading: boolean }

/** 一张图卡：独立时间筛选 + brush + hover（FR-169）。 */
function MonitorChartCard({
  def,
  source,
  defaultRange,
  resolution,
  worldFilter,
  height,
  useSeries,
}: {
  def: MetricChartDef
  source: MonitorSource
  defaultRange: MetricRange
  resolution: MetricResolution
  worldFilter?: string
  height: number
  useSeries: MonitorSeriesHook
}) {
  const { t } = useTranslation()
  const [range, setRange] = useState<MetricRange>(defaultRange)
  const { series: raw, isLoading } = useSeries(source, range, resolution)
  const plot = buildChartSeries(def, raw, (k) => t(k), worldFilter)

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
  resolution = 'auto',
  worldFilter,
  chartHeight = 190,
  useSeries,
}: {
  defs: MetricChartDef[]
  source: MonitorSource
  defaultRange?: MetricRange
  /** 页级聚合粒度（FR-221）：透传给每图查询；auto=按区间自动选档（既有默认）。 */
  resolution?: MetricResolution
  /** 下钻聚焦的世界名（FR-221）：分世界图只画该世界，非空才生效。 */
  worldFilter?: string
  chartHeight?: number
  useSeries: MonitorSeriesHook
}) {
  return (
    <div className="grid grid-cols-1 gap-3 lg:grid-cols-2 xl:grid-cols-3">
      {defs.map((def) => (
        <MonitorChartCard
          key={def.id}
          def={def}
          source={source}
          defaultRange={defaultRange}
          resolution={resolution}
          worldFilter={worldFilter}
          height={chartHeight}
          useSeries={useSeries}
        />
      ))}
    </div>
  )
}
