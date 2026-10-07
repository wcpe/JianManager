import { useTranslation } from 'react-i18next'
import { Panel } from '@jianmanager/ui/components/panel'
import { RangePicker, type MetricRange } from '../../../charts/RangePicker'
import { TimeSeriesChart, type ChartSeries } from '../../../charts/TimeSeriesChart'
import { formatBytes } from './NodeListParts'

/** 各实例对比图可切的指标（FR-060 #2：节点上各实例 TPS/MSPT/堆/线程对比）。 */
export const COMPARE_METRICS: { key: string; labelKey: string; fmt: (v: number) => string }[] = [
  { key: 'inst_tps', labelKey: 'metrics.tps', fmt: (v) => v.toFixed(1) },
  { key: 'inst_mspt', labelKey: 'metrics.mspt', fmt: (v) => `${v.toFixed(1)}ms` },
  { key: 'inst_heap_used', labelKey: 'metrics.heap', fmt: formatBytes },
  { key: 'inst_threads', labelKey: 'metrics.threads', fmt: (v) => v.toFixed(0) },
]

/** 对比图可读性上限：一图最多 12 条线（超出仅取名称升序前 12，FR-340）。50 是后端硬上限。 */
export const COMPARE_TARGET_CAP = 12

export interface NodeInstanceCompareProps {
  /** 当前指标键（容器持有：它决定批量取数）。 */
  metric: string
  onMetricChange: (key: string) => void
  /** 该节点实例总数（用于截断提示）。 */
  total: number
  /** 当前指标下各实例的序列（容器取数后组装）。 */
  series: ChartSeries[]
}

export interface NodeMonitorChartsProps {
  /** 当前时间范围（容器持有：它决定取数）。 */
  range: MetricRange
  onRangeChange: (r: MetricRange) => void
  /** 节点序列数据（容器经 useMetricSeries 取数）。 */
  series: { metricKey: string; points: { ts: string; avg: number }[] }[]
}

/**
 * 节点上各实例同一指标对比：每实例一条线，可切 TPS/MSPT/堆/线程（FR-060 #2）。
 * FR-340：实例清单走服务端按节点分页（`/instances/search`）取前 12（名称升序），
 * 指标一次批量查询（`/metrics/series/batch`）拆分为各线，消 N+1 请求风暴。
 */
export function NodeInstanceCompare({
  metric,
  onMetricChange,
  total,
  series,
}: NodeInstanceCompareProps) {
  const { t } = useTranslation()
  const spec = COMPARE_METRICS.find((m) => m.key === metric) ?? COMPARE_METRICS[0]

  return (
    <Panel
      title={t('nodes.instanceCompare')}
      actions={
        <div className="inline-flex rounded-md border p-0.5">
          {COMPARE_METRICS.map((m) => (
            <button
              key={m.key}
              type="button"
              onClick={() => onMetricChange(m.key)}
              className={`rounded px-2 py-0.5 text-xs ${metric === m.key ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:text-foreground'}`}
            >
              {t(m.labelKey)}
            </button>
          ))}
        </div>
      }
    >
      {total > COMPARE_TARGET_CAP && (
        <p className="mb-2 text-xs text-muted-foreground">
          {t('nodes.compareCap', { shown: COMPARE_TARGET_CAP, total })}
        </p>
      )}
      <TimeSeriesChart series={series} height={180} valueFormatter={spec.fmt} emptyHint={t('nodes.empty')} />
    </Panel>
  )
}

/** 详情「监控」分段：节点历史曲线组（CPU/内存/磁盘/网络/负载，FR-061/FR-060）。 */
export function NodeMonitorCharts({ range, onRangeChange, series: nodeSeries }: NodeMonitorChartsProps) {
  const { t } = useTranslation()

  const seriesOf = (metricKey: string, name: string): ChartSeries[] => {
    const s = nodeSeries.find((x) => x.metricKey === metricKey)
    if (!s) return []
    return [{ key: metricKey, name, points: s.points.map((p) => ({ ts: p.ts, value: p.avg })) }]
  }
  const netSeries: ChartSeries[] = [
    ...seriesOf('node_net_rx_rate', t('nodes.netRx')),
    ...seriesOf('node_net_tx_rate', t('nodes.netTx')),
  ]

  return (
    <div className="space-y-3">
      <div className="flex justify-end">
        <RangePicker value={range} onChange={onRangeChange} />
      </div>
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
        <Panel title={t('dashboard.cpuTrend')}>
          <TimeSeriesChart series={seriesOf('node_cpu_pct', t('nodes.cpu'))} height={160} valueFormatter={(v) => `${v.toFixed(0)}%`} />
        </Panel>
        <Panel title={t('dashboard.memTrend')}>
          <TimeSeriesChart series={seriesOf('node_mem_used', t('nodes.memory'))} height={160} valueFormatter={formatBytes} />
        </Panel>
        <Panel title={t('nodes.diskTrend')}>
          <TimeSeriesChart series={seriesOf('node_disk_used', t('nodes.disk'))} height={160} valueFormatter={formatBytes} />
        </Panel>
        <Panel title={t('nodes.netTrend')}>
          <TimeSeriesChart series={netSeries} height={160} valueFormatter={(v) => `${formatBytes(v)}/s`} />
        </Panel>
        <Panel title={t('nodes.loadTrend')}>
          <TimeSeriesChart series={seriesOf('node_load', t('nodes.load'))} height={160} valueFormatter={(v) => v.toFixed(2)} />
        </Panel>
      </div>
    </div>
  )
}
