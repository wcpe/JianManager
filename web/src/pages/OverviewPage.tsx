import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNodes } from '@/api/nodes'
import { useInstances } from '@/api/instances'
import { useMetricOverview } from '@/api/metrics'
import { Panel } from '@/components/ui/panel'
import { ResourceGauge } from '@/components/ui/gauge'
import { StatusBadge } from '@/components/ui/status-badge'
import { TimeSeriesChart, type ChartSeries } from '@/components/charts/TimeSeriesChart'
import { RangePicker, type MetricRange } from '@/components/charts/RangePicker'
import { instanceStatusLevel } from '@/lib/threshold'

/** 字节 → 紧凑可读（G/M/K）。 */
function fmtBytes(b: number): string {
  if (!Number.isFinite(b) || b <= 0) return '0'
  if (b >= 1e9) return `${(b / 1024 / 1024 / 1024).toFixed(1)}G`
  if (b >= 1e6) return `${(b / 1024 / 1024).toFixed(0)}M`
  return `${(b / 1024).toFixed(0)}K`
}

/** 统计块：大数值 + 标签 + 可选副信息。 */
function Stat({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <div className="flex flex-col justify-center rounded-lg border bg-card px-4 py-3">
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className="mt-0.5 text-2xl font-semibold tabular-nums">{value}</span>
      {sub && <span className="mt-0.5 text-xs text-muted-foreground">{sub}</span>}
    </div>
  )
}

/** 总览页（FR-061 旗舰）：环形仪表盘 + 聚合历史曲线（FR-060） + 密集实例表，一屏概览。 */
export default function OverviewPage() {
  const { t } = useTranslation()
  const [range, setRange] = useState<MetricRange>('24h')
  const { data: nodes } = useNodes()
  const { data: instances } = useInstances()
  const { data: overview } = useMetricOverview(range)

  const totals = overview?.totals
  const memPct = totals && totals.memTotalBytes > 0 ? (totals.memUsedBytes / totals.memTotalBytes) * 100 : 0

  /** 据 metricKey 取一条聚合趋势并映射为图表序列。 */
  const trend = (metricKey: string, name: string): ChartSeries[] => {
    const tr = overview?.trends.find((x) => x.metricKey === metricKey)
    if (!tr) return []
    return [{ key: metricKey, name, points: tr.points.map((p) => ({ ts: p.ts, value: p.avg })) }]
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-bold">{t('dashboard.title')}</h1>
        <RangePicker value={range} onChange={setRange} />
      </div>

      {/* 顶部：环形仪表盘 + 统计块 */}
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5">
        <Panel bodyClassName="flex items-center justify-center py-3">
          <ResourceGauge label={t('dashboard.totalCpu')} value={totals?.cpuPct ?? 0} unit="%" />
        </Panel>
        <Panel bodyClassName="flex items-center justify-center py-3">
          <ResourceGauge label={t('dashboard.totalMem')} value={memPct} unit="%" />
        </Panel>
        <Stat
          label={t('dashboard.nodes')}
          value={`${totals?.onlineNodeCount ?? 0}/${totals?.nodeCount ?? nodes?.length ?? 0}`}
          sub={t('dashboard.online')}
        />
        <Stat label={t('dashboard.runningInstances')} value={String(totals?.runningInstances ?? 0)} sub={t('dashboard.instances')} />
        <Stat label={t('dashboard.onlinePlayers')} value={String(totals?.onlinePlayers ?? 0)} sub={t('nav.players')} />
      </div>

      {/* 中部：聚合历史曲线（FR-060） */}
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-3">
        <Panel title={t('dashboard.cpuTrend')}>
          <TimeSeriesChart series={trend('node_cpu_pct', t('dashboard.totalCpu'))} height={180} valueFormatter={(v) => `${v.toFixed(0)}%`} />
        </Panel>
        <Panel title={t('dashboard.memTrend')}>
          <TimeSeriesChart series={trend('node_mem_used', t('dashboard.totalMem'))} height={180} valueFormatter={fmtBytes} />
        </Panel>
        <Panel title={t('dashboard.playersTrend')}>
          <TimeSeriesChart series={trend('inst_players_online', t('dashboard.onlinePlayers'))} height={180} valueFormatter={(v) => v.toFixed(0)} />
        </Panel>
      </div>

      {/* 底部：密集实例表 */}
      <Panel title={t('dashboard.instanceList')} bodyClassName="p-0">
        <table className="w-full text-[13px]">
          <thead>
            <tr className="border-b text-xs text-muted-foreground">
              <th className="px-3 py-2 text-left font-medium">{t('instances.name')}</th>
              <th className="px-3 py-2 text-left font-medium">{t('instances.type')}</th>
              <th className="px-3 py-2 text-left font-medium">{t('instances.status')}</th>
            </tr>
          </thead>
          <tbody>
            {instances?.map((inst) => (
              <tr key={inst.id} className="border-b last:border-0 hover:bg-muted/40">
                <td className="px-3 py-1.5 font-medium">{inst.name}</td>
                <td className="px-3 py-1.5 text-muted-foreground">{inst.type}</td>
                <td className="px-3 py-1.5">
                  <StatusBadge level={instanceStatusLevel(inst.status)} label={inst.status} />
                </td>
              </tr>
            ))}
            {(!instances || instances.length === 0) && (
              <tr>
                <td colSpan={3} className="px-3 py-6 text-center text-muted-foreground">
                  {t('instances.empty')}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </Panel>
    </div>
  )
}
