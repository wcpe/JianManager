import { Fragment, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useQueries } from '@tanstack/react-query'
import {
  useNodes,
  useSetNodeMaintenance,
  useDrainNode,
  useDeleteNode,
  type NodeInfo,
} from '@/api/nodes'
import { useInstances } from '@/api/instances'
import api from '@/api/client'
import { useMetricSeries, type MetricSeriesResponse } from '@/api/metrics'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Badge } from '@/components/ui/badge'
import { Panel } from '@/components/ui/panel'
import { MiniBar } from '@/components/ui/mini-bar'
import { StatusBadge } from '@/components/ui/status-badge'
import { ResourceGauge } from '@/components/ui/gauge'
import { TimeSeriesChart, type ChartSeries } from '@/components/charts/TimeSeriesChart'
import { RangePicker, type MetricRange } from '@/components/charts/RangePicker'
import type { StatusLevel } from '@/lib/threshold'

import NodeJDKPanel from '@/components/NodeJDKPanel'
import NodePortsPanel from '@/components/NodePortsPanel'
import DangerConfirm from '@/components/DangerConfirm'

/** 将字节数格式化为人类可读的大小（B/KB/MB/GB）。 */
function formatBytes(bytes: number): string {
  if (bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  const value = bytes / Math.pow(1024, i)
  return `${value.toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

/** 节点状态码 → 状态等级（1 在线=正常 / 2 启动中=警告 / 0 离线=危险）。 */
function nodeStatusLevel(status: number): StatusLevel {
  if (status === 1) return 'success'
  if (status === 2) return 'warning'
  return 'danger'
}

/** 待二次确认的危险节点操作（FR-048）。 */
type PendingAction = { kind: 'drain' | 'delete'; node: NodeInfo }

/** 单元格内的占用条：MiniBar + 百分比（阈值变色）。 */
function UsageCell({ pct }: { pct: number }) {
  if (!pct) return <span className="text-muted-foreground">--</span>
  const v = pct * 100
  return (
    <div className="flex items-center gap-2">
      <MiniBar value={v} className="w-16" />
      <span className="tabular-nums text-xs">{v.toFixed(0)}%</span>
    </div>
  )
}

/** 各实例对比图可切的指标（FR-060 #2：节点上各实例 TPS/MSPT/堆/线程对比）。 */
const COMPARE_METRICS: { key: string; labelKey: string; fmt: (v: number) => string }[] = [
  { key: 'inst_tps', labelKey: 'metrics.tps', fmt: (v) => v.toFixed(1) },
  { key: 'inst_mspt', labelKey: 'metrics.mspt', fmt: (v) => `${v.toFixed(1)}ms` },
  { key: 'inst_heap_used', labelKey: 'metrics.heap', fmt: formatBytes },
  { key: 'inst_threads', labelKey: 'metrics.threads', fmt: (v) => v.toFixed(0) },
]

/** 节点上各实例同一指标对比：每实例一条线，可切 TPS/MSPT/堆/线程。 */
function NodeInstanceCompare({ node, range }: { node: NodeInfo; range: MetricRange }) {
  const { t } = useTranslation()
  const [metric, setMetric] = useState('inst_tps')
  const { data: instances } = useInstances()
  const nodeInstances = (instances ?? []).filter((i) => i.nodeId === node.id)
  const spec = COMPARE_METRICS.find((m) => m.key === metric) ?? COMPARE_METRICS[0]

  // 每实例一条查询并行拉取（实例数动态，用 useQueries）。
  const results = useQueries({
    queries: nodeInstances.map((inst) => ({
      queryKey: ['metricSeries', 'instance', inst.uuid, range, metric],
      queryFn: async () => {
        const q = new URLSearchParams({ scope: 'instance', targetId: inst.uuid, range, metrics: metric })
        const { data } = await api.get<MetricSeriesResponse>(`/metrics/series?${q.toString()}`)
        return data
      },
      enabled: !!inst.uuid,
      refetchInterval: 30_000,
    })),
  })

  const series: ChartSeries[] = nodeInstances.map((inst, i) => {
    const s = results[i].data?.series.find((x) => x.metricKey === metric && x.world === '')
    return { key: inst.uuid, name: inst.name, points: (s?.points ?? []).map((p) => ({ ts: p.ts, value: p.avg })) }
  })

  return (
    <Panel
      title={t('nodes.instanceCompare')}
      actions={
        <div className="inline-flex rounded-md border p-0.5">
          {COMPARE_METRICS.map((m) => (
            <button
              key={m.key}
              type="button"
              onClick={() => setMetric(m.key)}
              className={`rounded px-2 py-0.5 text-xs ${metric === m.key ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:text-foreground'}`}
            >
              {t(m.labelKey)}
            </button>
          ))}
        </div>
      }
    >
      <TimeSeriesChart series={series} height={180} valueFormatter={spec.fmt} emptyHint={t('nodes.empty')} />
    </Panel>
  )
}

/** 展开的节点详情（FR-061/FR-060）：环形仪表盘 + CPU/内存/磁盘/网络历史曲线 + 各实例指标对比。 */
function NodeDetail({ node }: { node: NodeInfo }) {
  const { t } = useTranslation()
  const [range, setRange] = useState<MetricRange>('24h')
  const { data } = useMetricSeries({ scope: 'node', targetId: node.uuid, range })

  const seriesOf = (metricKey: string, name: string): ChartSeries[] => {
    const s = data?.series.find((x) => x.metricKey === metricKey)
    if (!s) return []
    return [{ key: metricKey, name, points: s.points.map((p) => ({ ts: p.ts, value: p.avg })) }]
  }
  const netSeries: ChartSeries[] = [
    ...seriesOf('node_net_rx_rate', t('nodes.netRx')),
    ...seriesOf('node_net_tx_rate', t('nodes.netTx')),
  ]

  return (
    <div className="space-y-3 bg-muted/30 p-3">
      <div className="flex flex-wrap items-center gap-6">
        <ResourceGauge label={t('nodes.cpu')} value={(node.cpuUsage ?? 0) * 100} unit="%" size={84} />
        <ResourceGauge label={t('nodes.memory')} value={(node.memoryUsage ?? 0) * 100} unit="%" size={84} />
        <ResourceGauge label={t('nodes.disk')} value={(node.diskUsage ?? 0) * 100} unit="%" size={84} />
        <div className="ml-auto">
          <RangePicker value={range} onChange={setRange} />
        </div>
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
      </div>
      <NodeInstanceCompare node={node} range={range} />
    </div>
  )
}

export default function NodesPage() {
  const { t } = useTranslation()
  const { data: nodes, isLoading } = useNodes({ refetchInterval: 30_000 })

  const [jdkNodeId, setJdkNodeId] = useState<number | null>(null)
  const [portsNodeId, setPortsNodeId] = useState<number | null>(null)
  const [pending, setPending] = useState<PendingAction | null>(null)
  const [expandedId, setExpandedId] = useState<number | null>(null)

  const setMaintenance = useSetNodeMaintenance()
  const drain = useDrainNode()
  const del = useDeleteNode()

  const toggleMaintenance = (node: NodeInfo) => {
    const enabled = !node.maintenance
    setMaintenance.mutate(
      { id: node.id, enabled },
      {
        onSuccess: () =>
          toast.success(enabled ? t('nodes.maintenanceEnabled') : t('nodes.maintenanceDisabled')),
        onError: (e: Error & { response?: { data?: { message?: string } } }) =>
          toast.error(e?.response?.data?.message || t('common.error')),
      },
    )
  }

  const confirmPending = () => {
    if (!pending) return
    const { kind, node } = pending
    setPending(null)
    if (kind === 'drain') {
      drain.mutate(node.id, {
        onSuccess: (res) => toast.success(t('nodes.drainDone', { count: res.data.stoppedCount })),
        onError: (e: Error & { response?: { data?: { message?: string } } }) =>
          toast.error(e?.response?.data?.message || t('common.error')),
      })
    } else {
      del.mutate(node.id, {
        onSuccess: () => toast.success(t('nodes.deleted')),
        onError: (e: Error & { response?: { data?: { message?: string } } }) =>
          toast.error(e?.response?.data?.message || t('common.error')),
      })
    }
  }

  return (
    <div className="space-y-4">
      <h1 className="text-xl font-bold">{t('nodes.title')}</h1>

      {isLoading ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : (
        <Panel bodyClassName="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('nodes.name')}</TableHead>
                <TableHead>{t('nodes.ip')}</TableHead>
                <TableHead>{t('nodes.status')}</TableHead>
                <TableHead>{t('nodes.cpu')}</TableHead>
                <TableHead>{t('nodes.memory')}</TableHead>
                <TableHead>{t('nodes.disk')}</TableHead>
                <TableHead>{t('nodes.network')}</TableHead>
                <TableHead>{t('nodes.system')}</TableHead>
                <TableHead className="text-right">{t('nodes.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {nodes?.map((node) => {
                const isOnline = node.status === 1
                const expanded = expandedId === node.id
                return (
                  <Fragment key={node.id}>
                    <TableRow>
                      <TableCell>
                        <button
                          className="font-medium hover:text-primary"
                          onClick={() => setExpandedId(expanded ? null : node.id)}
                        >
                          {expanded ? '▾ ' : '▸ '}
                          {node.name}
                        </button>
                      </TableCell>
                      <TableCell className="text-muted-foreground">{node.host}</TableCell>
                      <TableCell>
                        <div className="flex items-center gap-1.5">
                          <StatusBadge
                            level={nodeStatusLevel(node.status)}
                            label={
                              node.status === 1
                                ? t('nodes.online')
                                : node.status === 2
                                  ? t('nodes.starting')
                                  : t('nodes.offline')
                            }
                          />
                          {node.maintenance && (
                            <Badge variant="outline" className="text-status-warning border-status-warning/50">
                              {t('nodes.maintenance')}
                            </Badge>
                          )}
                        </div>
                      </TableCell>
                      <TableCell>
                        <UsageCell pct={node.cpuUsage} />
                      </TableCell>
                      <TableCell>
                        <UsageCell pct={node.memoryUsage} />
                      </TableCell>
                      <TableCell>
                        <UsageCell pct={node.diskUsage} />
                      </TableCell>
                      <TableCell className="text-muted-foreground text-xs">
                        {node.networkBytesSent || node.networkBytesRecv
                          ? `↑${formatBytes(node.networkBytesSent)} ↓${formatBytes(node.networkBytesRecv)}`
                          : '--'}
                      </TableCell>
                      <TableCell className="text-muted-foreground">
                        {node.os} {node.arch}
                      </TableCell>
                      <TableCell className="space-x-3 text-right whitespace-nowrap">
                        <button className="text-xs text-primary hover:underline" onClick={() => setJdkNodeId(node.id)}>
                          JDK
                        </button>
                        <button className="text-xs text-primary hover:underline" onClick={() => setPortsNodeId(node.id)}>
                          {t('ports.button')}
                        </button>
                        <button
                          className="text-xs text-status-warning hover:underline"
                          onClick={() => toggleMaintenance(node)}
                        >
                          {node.maintenance ? t('nodes.uncordon') : t('nodes.cordon')}
                        </button>
                        <button
                          className="text-xs text-status-warning hover:underline"
                          onClick={() => setPending({ kind: 'drain', node })}
                        >
                          {t('nodes.drain')}
                        </button>
                        <button
                          className="text-xs text-status-danger hover:underline disabled:opacity-40 disabled:no-underline"
                          disabled={isOnline}
                          title={isOnline ? t('nodes.deleteOnlineHint') : undefined}
                          onClick={() => setPending({ kind: 'delete', node })}
                        >
                          {t('nodes.delete')}
                        </button>
                      </TableCell>
                    </TableRow>
                    {expanded && (
                      <TableRow>
                        <TableCell colSpan={9} className="p-0">
                          <NodeDetail node={node} />
                        </TableCell>
                      </TableRow>
                    )}
                  </Fragment>
                )
              })}
              {(!nodes || nodes.length === 0) && (
                <TableRow>
                  <TableCell colSpan={9} className="text-center text-muted-foreground">
                    {t('nodes.empty')}
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </Panel>
      )}

      {jdkNodeId !== null && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
          <div className="bg-background border rounded-lg p-6 w-full max-w-2xl shadow-lg max-h-[80vh] overflow-y-auto">
            <div className="flex items-center justify-between mb-4">
              <h2 className="text-lg font-bold">JDK Management</h2>
              <button onClick={() => setJdkNodeId(null)} className="text-sm text-muted-foreground hover:text-foreground">
                Close
              </button>
            </div>
            <NodeJDKPanel nodeId={jdkNodeId} />
          </div>
        </div>
      )}
      {portsNodeId !== null && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
          <div className="bg-background border rounded-lg p-6 w-full max-w-2xl shadow-lg max-h-[80vh] overflow-y-auto">
            <div className="flex items-center justify-between mb-4">
              <h2 className="text-lg font-bold">{t('ports.title')}</h2>
              <button onClick={() => setPortsNodeId(null)} className="text-sm text-muted-foreground hover:text-foreground">
                {t('common.close')}
              </button>
            </div>
            <NodePortsPanel nodeId={portsNodeId} />
          </div>
        </div>
      )}
      <DangerConfirm
        open={pending !== null}
        title={pending?.kind === 'drain' ? t('nodes.drainConfirmTitle') : t('nodes.deleteConfirmTitle')}
        description={
          pending?.kind === 'drain'
            ? t('nodes.drainConfirmDesc', { name: pending?.node.name })
            : t('nodes.deleteConfirmDesc', { name: pending?.node.name })
        }
        confirmLabel={pending?.kind === 'drain' ? t('nodes.drain') : t('nodes.delete')}
        confirmText={pending?.kind === 'delete' ? pending?.node.name : undefined}
        scope="platform"
        onConfirm={confirmPending}
        onCancel={() => setPending(null)}
      />
    </div>
  )
}
