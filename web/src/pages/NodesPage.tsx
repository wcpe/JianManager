import { Fragment, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useQueries } from '@tanstack/react-query'
import { Plus } from 'lucide-react'
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
import { StatCard } from '@/components/ui/stat-card'
import { SummaryChips, type SummaryChip } from '@/components/ui/summary-chips'
import { ViewToggle, type ViewMode } from '@/components/ui/view-toggle'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
} from '@/components/ui/dropdown-menu'
import { TimeSeriesChart, type ChartSeries } from '@/components/charts/TimeSeriesChart'
import { RangePicker, type MetricRange } from '@/components/charts/RangePicker'
import { resourceLevel, type StatusLevel } from '@/lib/threshold'
import { summarizeNodes } from '@/lib/node-summary'
import { NodeWorktableCard } from '@/components/console/NodeWorktableCard'

import NodeJDKPanel from '@/components/NodeJDKPanel'
import NodePortsPanel from '@/components/NodePortsPanel'
import NodeArtifactCachePanel from '@/components/NodeArtifactCachePanel'
import DangerConfirm from '@/components/DangerConfirm'
import AddNodeDialog from '@/components/AddNodeDialog'
import { Button } from '@/components/ui/button'
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetDescription,
} from '@/components/ui/sheet'

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
function UsageCell({ pct, online = true }: { pct: number; online?: boolean }) {
  // 离线节点的资源值是 DB 里的陈旧 last-known，渲染成实时占用条会误导为「在线空载/在跑」，统一显示无数据（BUG-019）。
  if (!online || !pct) return <span className="text-muted-foreground">--</span>
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

/** 详情中的「概览」分段：硬件 + 系统 + 网络等次要信息（从主表移入，FR-144）。 */
function NodeOverviewSection({ node }: { node: NodeInfo }) {
  const { t } = useTranslation()
  const online = node.status === 1
  const rows: { label: string; value: React.ReactNode }[] = [
    { label: t('nodes.ip'), value: node.host },
    { label: t('nodes.system'), value: `${node.os} ${node.arch}` },
    { label: t('nodes.cpuCores'), value: node.cpuCores > 0 ? node.cpuCores : '--' },
    {
      label: t('nodes.network'),
      value:
        online && (node.networkBytesSent || node.networkBytesRecv)
          ? `↑${formatBytes(node.networkBytesSent)} ↓${formatBytes(node.networkBytesRecv)}`
          : '--',
    },
    { label: t('nodes.grpcPort'), value: node.grpcPort > 0 ? node.grpcPort : '--' },
    { label: t('nodes.wsPort'), value: node.wsPort > 0 ? node.wsPort : '--' },
  ]
  return (
    <Panel title={t('nodes.overviewSection')}>
      <div className="grid grid-cols-2 gap-x-6 gap-y-2 lg:grid-cols-3">
        {rows.map((r) => (
          <div key={r.label}>
            <div className="text-[11px] text-muted-foreground">{r.label}</div>
            <div className="text-xs">{r.value}</div>
          </div>
        ))}
      </div>
    </Panel>
  )
}

/** 展开的节点详情（FR-061/FR-060/FR-144）：概览（IP/系统/网络）+ 环形仪表盘 + 历史曲线 + 各实例指标对比。 */
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
      <NodeOverviewSection node={node} />
      <div className="flex flex-wrap items-center gap-6">
        <ResourceGauge label={t('nodes.cpu')} value={(node.cpuUsage ?? 0) * 100} unit="%" size={84} />
        <ResourceGauge
          label={t('nodes.load')}
          value={node.cpuCores > 0 ? ((node.loadAvg1 ?? 0) / node.cpuCores) * 100 : 0}
          unit="%"
          size={84}
        />
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
        <Panel title={t('nodes.loadTrend')}>
          <TimeSeriesChart series={seriesOf('node_load', t('nodes.load'))} height={160} valueFormatter={(v) => v.toFixed(2)} />
        </Panel>
      </div>
      <NodeInstanceCompare node={node} range={range} />
    </div>
  )
}

/**
 * 节点操作菜单（FR-144）：JDK / 端口 / 维护 / 排空 / 下线收入「⋯」kebab，
 * 排空与下线标危险色；下线在线节点禁用 + tooltip。
 */
function NodeActionsMenu({
  node,
  onJDK,
  onPorts,
  onCache,
  onToggleMaintenance,
  onDrain,
  onDelete,
}: {
  node: NodeInfo
  onJDK: () => void
  onPorts: () => void
  onCache: () => void
  onToggleMaintenance: () => void
  onDrain: () => void
  onDelete: () => void
}) {
  const { t } = useTranslation()
  const online = node.status === 1
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="xs" aria-label={t('nodes.actions')} className="px-1.5">
          ⋯
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuItem onSelect={onJDK}>{t('nodes.jdkTitle')}</DropdownMenuItem>
        <DropdownMenuItem onSelect={onCache}>{t('artifactCache.title')}</DropdownMenuItem>
        <DropdownMenuItem onSelect={onPorts}>{t('ports.button')}</DropdownMenuItem>
        <DropdownMenuItem onSelect={onToggleMaintenance}>
          {node.maintenance ? t('nodes.uncordon') : t('nodes.cordon')}
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem variant="destructive" onSelect={onDrain}>
          {t('nodes.drain')}
        </DropdownMenuItem>
        <DropdownMenuItem
          variant="destructive"
          title={online ? t('nodes.deleteOnlineHint') : undefined}
          className={online ? 'opacity-50 cursor-not-allowed' : undefined}
          onSelect={(e) => {
            if (online) {
              e.preventDefault()
              return
            }
            onDelete()
          }}
        >
          {t('nodes.delete')}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

export default function NodesPage() {
  const { t } = useTranslation()
  const { data: nodes, isLoading } = useNodes({ refetchInterval: 30_000 })
  const { data: instances } = useInstances()

  const [jdkNodeId, setJdkNodeId] = useState<number | null>(null)
  const [portsNodeId, setPortsNodeId] = useState<number | null>(null)
  const [cacheNodeId, setCacheNodeId] = useState<number | null>(null)
  const [pending, setPending] = useState<PendingAction | null>(null)
  const [expandedId, setExpandedId] = useState<number | null>(null)
  const [addOpen, setAddOpen] = useState(false)
  // 工作台卡 ⇄ 列表视图（FR-144，§4.5）；运行实体默认卡片。
  const [view, setView] = useState<ViewMode>('card')

  const setMaintenance = useSetNodeMaintenance()
  const drain = useDrainNode()
  const del = useDeleteNode()

  // 集群汇总（FR-144）：在线/离线/维护计数 + 在线节点资源水位均值。
  const summary = useMemo(() => summarizeNodes(nodes ?? []), [nodes])
  // 各节点实例数（统计一次，卡片/详情共用）。
  const instanceCountByNode = useMemo(() => {
    const map = new Map<number, number>()
    for (const i of instances ?? []) map.set(i.nodeId, (map.get(i.nodeId) ?? 0) + 1)
    return map
  }, [instances])

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

  const buildMenu = (node: NodeInfo) => (
    <NodeActionsMenu
      node={node}
      onJDK={() => setJdkNodeId(node.id)}
      onPorts={() => setPortsNodeId(node.id)}
      onCache={() => setCacheNodeId(node.id)}
      onToggleMaintenance={() => toggleMaintenance(node)}
      onDrain={() => setPending({ kind: 'drain', node })}
      onDelete={() => setPending({ kind: 'delete', node })}
    />
  )

  // 集群水位卡（仅有在线节点时显示水位，否则「--」）。
  const gauge = (pct: number | null) => (pct === null ? '--' : `${pct.toFixed(0)}%`)
  const summaryChips: SummaryChip[] = [
    {
      key: 'online',
      label: t('nodes.online'),
      count: summary.online,
      level: 'success',
      breathing: summary.online > 0,
    },
    { key: 'offline', label: t('nodes.offline'), count: summary.offline, level: 'danger' },
    { key: 'maintenance', label: t('nodes.maintenance'), count: summary.maintenance, level: 'warning' },
  ]

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-bold">{t('nodes.title')}</h1>
        <Button size="sm" onClick={() => setAddOpen(true)}>
          <Plus className="size-4" /> {t('nodes.enroll.addNode', '添加节点')}
        </Button>
      </div>

      {/* 集群汇总头：状态计数 chip + CPU/内存/磁盘聚合水位（FR-144） */}
      <div className="flex flex-wrap items-center gap-2">
        <SummaryChips chips={summaryChips} className="flex-1" />
        <ViewToggle
          value={view}
          onChange={setView}
          cardLabel={t('grouping.viewCard')}
          listLabel={t('grouping.viewList')}
        />
      </div>
      <div className="grid grid-cols-3 gap-3">
        <StatCard
          label={t('nodes.clusterCpu')}
          value={gauge(summary.cpuPct)}
          bar={summary.cpuPct !== null ? { value: summary.cpuPct, level: resourceLevel(summary.cpuPct) } : undefined}
        />
        <StatCard
          label={t('nodes.clusterMem')}
          value={gauge(summary.memPct)}
          bar={summary.memPct !== null ? { value: summary.memPct, level: resourceLevel(summary.memPct) } : undefined}
        />
        <StatCard
          label={t('nodes.clusterDisk')}
          value={gauge(summary.diskPct)}
          bar={summary.diskPct !== null ? { value: summary.diskPct, level: resourceLevel(summary.diskPct) } : undefined}
        />
      </div>

      {isLoading ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : !nodes || nodes.length === 0 ? (
        <Panel bodyClassName="py-10 text-center text-muted-foreground">{t('nodes.empty')}</Panel>
      ) : view === 'card' ? (
        <div className="space-y-3">
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
            {nodes.map((node) => (
              <NodeWorktableCard
                key={node.id}
                node={node}
                instanceCount={instanceCountByNode.get(node.id) ?? 0}
                expanded={expandedId === node.id}
                onToggle={() => setExpandedId(expandedId === node.id ? null : node.id)}
                menu={buildMenu(node)}
              />
            ))}
          </div>
          {/* 展开的节点详情（卡片视图：详情显示在网格下方，仅展开时挂载→可见才轮询） */}
          {expandedId !== null && nodes.find((n) => n.id === expandedId) && (
            <Panel bodyClassName="p-0">
              <NodeDetail node={nodes.find((n) => n.id === expandedId)!} />
            </Panel>
          )}
        </div>
      ) : (
        <Panel bodyClassName="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('nodes.name')}</TableHead>
                <TableHead>{t('nodes.status')}</TableHead>
                <TableHead>{t('nodes.cpu')}</TableHead>
                <TableHead>{t('nodes.memory')}</TableHead>
                <TableHead>{t('nodes.disk')}</TableHead>
                <TableHead className="text-right">{t('nodes.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {nodes.map((node) => {
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
                        <UsageCell pct={node.cpuUsage} online={isOnline} />
                      </TableCell>
                      <TableCell>
                        <UsageCell pct={node.memoryUsage} online={isOnline} />
                      </TableCell>
                      <TableCell>
                        <UsageCell pct={node.diskUsage} online={isOnline} />
                      </TableCell>
                      <TableCell className="text-right whitespace-nowrap">
                        {buildMenu(node)}
                      </TableCell>
                    </TableRow>
                    {expanded && (
                      <TableRow>
                        <TableCell colSpan={6} className="p-0">
                          <NodeDetail node={node} />
                        </TableCell>
                      </TableRow>
                    )}
                  </Fragment>
                )
              })}
            </TableBody>
          </Table>
        </Panel>
      )}

      {/* 统一右侧抽屉容器取代手搓 fixed inset-0 模态（FR-178）：宽、可滚动、主题化滚动条。 */}
      <Sheet open={jdkNodeId !== null} onOpenChange={(o: boolean) => { if (!o) setJdkNodeId(null) }}>
        <SheetContent className="w-full sm:max-w-2xl overflow-y-auto">
          <SheetHeader className="pr-8">
            <SheetTitle>{t('nodes.jdkTitle')}</SheetTitle>
            <SheetDescription>{t('artifactCache.jdkDrawerDesc')}</SheetDescription>
          </SheetHeader>
          {jdkNodeId !== null && <NodeJDKPanel nodeId={jdkNodeId} active={jdkNodeId !== null} />}
        </SheetContent>
      </Sheet>

      <Sheet open={cacheNodeId !== null} onOpenChange={(o: boolean) => { if (!o) setCacheNodeId(null) }}>
        <SheetContent className="w-full sm:max-w-2xl overflow-y-auto">
          <SheetHeader className="pr-8">
            <SheetTitle>{t('artifactCache.title')}</SheetTitle>
            <SheetDescription>{t('artifactCache.drawerDesc')}</SheetDescription>
          </SheetHeader>
          {cacheNodeId !== null && <NodeArtifactCachePanel nodeId={cacheNodeId} active={cacheNodeId !== null} />}
        </SheetContent>
      </Sheet>

      <Sheet open={portsNodeId !== null} onOpenChange={(o: boolean) => { if (!o) setPortsNodeId(null) }}>
        <SheetContent className="w-full sm:max-w-2xl overflow-y-auto">
          <SheetHeader className="pr-8">
            <SheetTitle>{t('ports.title')}</SheetTitle>
          </SheetHeader>
          {portsNodeId !== null && <NodePortsPanel nodeId={portsNodeId} />}
        </SheetContent>
      </Sheet>
      <AddNodeDialog open={addOpen} onClose={() => setAddOpen(false)} />
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
