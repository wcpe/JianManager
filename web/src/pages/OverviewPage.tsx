import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNodes } from '@/api/nodes'
import { useInstances } from '@/api/instances'
import { useMetricOverview } from '@/api/metrics'
import { Panel } from '@/components/ui/panel'
import { StatCard } from '@/components/ui/stat-card'
import { ResourceGauge } from '@/components/ui/gauge'
import { StatusBadge } from '@/components/ui/status-badge'
import { TimeSeriesChart, type ChartSeries } from '@/components/charts/TimeSeriesChart'
import { RangePicker, type MetricRange } from '@/components/charts/RangePicker'
import { instanceStatusLevel } from '@/lib/threshold'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

/** 字节 → 紧凑可读（G/M/K）。 */
function fmtBytes(b: number): string {
  if (!Number.isFinite(b) || b <= 0) return '0'
  if (b >= 1e9) return `${(b / 1024 / 1024 / 1024).toFixed(1)}G`
  if (b >= 1e6) return `${(b / 1024 / 1024).toFixed(0)}M`
  return `${(b / 1024).toFixed(0)}K`
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
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
        <Panel bodyClassName="flex items-center justify-center py-3">
          <ResourceGauge label={t('dashboard.totalCpu')} value={totals?.cpuPct ?? 0} unit="%" />
        </Panel>
        <Panel bodyClassName="flex items-center justify-center py-3">
          {/* 负载是「占总核数比例」：以倍数（load÷核）呈现而非百分比，环按 1.0=满核封顶，
              不再出现 >100% 的破环（FR-108）。grading 仍按占比走 resourceLevel（>0.8×→红）。 */}
          <ResourceGauge label={t('dashboard.totalLoad')} value={(totals?.loadAvg ?? 0) / 100} max={1} unit="×" decimals={2} />
        </Panel>
        <Panel bodyClassName="flex items-center justify-center py-3">
          <ResourceGauge label={t('dashboard.totalMem')} value={memPct} unit="%" />
        </Panel>
        <StatCard
          label={t('dashboard.nodes')}
          value={`${totals?.onlineNodeCount ?? 0}/${totals?.nodeCount ?? nodes?.length ?? 0}`}
          sub={t('dashboard.online')}
        />
        <StatCard label={t('dashboard.runningInstances')} value={String(totals?.runningInstances ?? 0)} sub={t('dashboard.instances')} />
        <StatCard label={t('dashboard.onlinePlayers')} value={String(totals?.onlinePlayers ?? 0)} sub={t('nav.players')} />
      </div>

      {/* 中部：聚合历史曲线（FR-060） */}
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
        <Panel title={t('dashboard.cpuTrend')}>
          <TimeSeriesChart series={trend('node_cpu_pct', t('dashboard.totalCpu'))} height={180} valueFormatter={(v) => `${v.toFixed(0)}%`} />
        </Panel>
        <Panel title={t('dashboard.loadTrend')}>
          <TimeSeriesChart series={trend('node_load', t('dashboard.totalLoad'))} height={180} valueFormatter={(v) => v.toFixed(2)} />
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
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('instances.name')}</TableHead>
              <TableHead>{t('instances.type')}</TableHead>
              <TableHead>{t('instances.status')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {instances?.map((inst) => (
              <TableRow key={inst.id}>
                <TableCell className="font-medium">{inst.name}</TableCell>
                <TableCell className="text-muted-foreground">{inst.type}</TableCell>
                <TableCell>
                  <StatusBadge level={instanceStatusLevel(inst.status)} label={inst.status} />
                </TableCell>
              </TableRow>
            ))}
            {(!instances || instances.length === 0) && (
              <TableRow>
                <TableCell colSpan={3} className="h-16 text-center text-muted-foreground">
                  {t('instances.empty')}
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </Panel>
    </div>
  )
}
