import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useQueries } from '@tanstack/react-query'
import {
  Box,
  ChevronsLeft,
  ChevronsRight,
  Plus,
  Search,
  Server,
} from 'lucide-react'
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
import { Badge } from '@/components/ui/badge'
import { Panel } from '@/components/ui/panel'
import { Input } from '@/components/ui/input'
import { MiniBar } from '@/components/ui/mini-bar'
import { StatusBadge } from '@/components/ui/status-badge'
import { ResourceGauge } from '@/components/ui/gauge'
import { StatCard } from '@/components/ui/stat-card'
import { SummaryChips, type SummaryChip } from '@/components/ui/summary-chips'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
} from '@/components/ui/dropdown-menu'
import { TimeSeriesChart, type ChartSeries } from '@/components/charts/TimeSeriesChart'
import { RangePicker, type MetricRange } from '@/components/charts/RangePicker'
import { resourceLevel } from '@/lib/threshold'
import { summarizeNodes } from '@/lib/node-summary'
import {
  nodeStatusLevel,
  filterNodes,
  resolveSelectedNode,
  loadNodeListCollapsed,
  persistNodeListCollapsed,
} from '@/lib/node-list'
import { toneChipClass } from '@/lib/tone'
import { cn } from '@/lib/utils'

import NodeJDKPanel from '@/components/NodeJDKPanel'
import NodePortsPanel from '@/components/NodePortsPanel'
import NodeArtifactCachePanel from '@/components/NodeArtifactCachePanel'
import NodeProxyPanel from '@/components/NodeProxyPanel'
import NodeRepairPanel from '@/components/NodeRepairPanel'
import DangerConfirm from '@/components/DangerConfirm'
import AddNodeDialog from '@/components/AddNodeDialog'
import { Button } from '@/components/ui/button'

/** 将字节数格式化为人类可读的大小（B/KB/MB/GB）。 */
function formatBytes(bytes: number): string {
  if (bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  const value = bytes / Math.pow(1024, i)
  return `${value.toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

/** 待二次确认的危险节点操作（FR-048）。 */
type PendingAction = { kind: 'drain' | 'delete'; node: NodeInfo }

/** 右栏分段（FR-177 §3.3 + FR-185）：概览/实例/JDK/缓存/端口/代理/监控/坏节点修复。 */
type DetailTab = 'overview' | 'instances' | 'jdk' | 'cache' | 'ports' | 'proxy' | 'monitor' | 'repair'
const DETAIL_TABS: DetailTab[] = ['overview', 'instances', 'jdk', 'cache', 'ports', 'proxy', 'monitor', 'repair']

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

/** 详情「概览」分段：硬件 + 系统 + 网络等次要信息（FR-144）。 */
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

/** 详情「监控」分段：节点历史曲线组（CPU/内存/磁盘/网络/负载，FR-061/FR-060）。 */
function NodeMonitorCharts({ node }: { node: NodeInfo }) {
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
    <div className="space-y-3">
      <div className="flex justify-end">
        <RangePicker value={range} onChange={setRange} />
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

/**
 * 节点身份块操作菜单（FR-144/FR-177）：进入/解除维护、排空、下线收入「⋯」kebab。
 * 排空与下线标危险色；下线在线节点禁用 + tooltip。
 */
function NodeActionsMenu({
  node,
  onToggleMaintenance,
  onDrain,
  onDelete,
}: {
  node: NodeInfo
  onToggleMaintenance: () => void
  onDrain: () => void
  onDelete: () => void
}) {
  const { t } = useTranslation()
  const online = node.status === 1
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="sm" aria-label={t('nodes.actions')} className="px-2">
          ⋯
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
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

/** 左栏列表中的单条资源 mini 水位（离线置灰为空轨）。 */
function RowUsage({ label, pct, online }: { label: string; pct: number; online: boolean }) {
  return (
    <div className="flex items-center gap-1">
      <span className="text-[9px] font-medium text-muted-foreground">{label}</span>
      <MiniBar value={online ? pct : 0} className="w-10" />
    </div>
  )
}

/** 左栏节点列表行（FR-177）：状态点呼吸灯 + 名 + host + mini 水位（CPU/内存）+ 实例数；选中高亮、离线置灰。 */
function NodeListRow({
  node,
  instanceCount,
  selected,
  onSelect,
}: {
  node: NodeInfo
  instanceCount: number
  selected: boolean
  onSelect: () => void
}) {
  const { t } = useTranslation()
  const online = node.status === 1
  const level = nodeStatusLevel(node.status)
  return (
    <button
      type="button"
      onClick={onSelect}
      aria-current={selected}
      className={cn(
        'w-full rounded-lg border px-2.5 py-2 text-left transition-colors',
        selected ? 'border-primary/40 bg-accent' : 'border-transparent hover:bg-accent/50',
        !online && 'opacity-60',
      )}
    >
      <div className="flex items-center gap-2">
        <span
          className={cn('size-2 shrink-0 rounded-full', online && 'animate-breathing')}
          style={{ backgroundColor: `var(--status-${level === 'neutral' ? 'info' : level})`, color: `var(--status-${level === 'neutral' ? 'info' : level})` }}
          aria-hidden
        />
        <span className="min-w-0 flex-1 truncate text-sm font-medium" title={node.name}>
          {node.name}
        </span>
        <span className="inline-flex shrink-0 items-center gap-0.5 text-[11px] text-muted-foreground">
          <Box className="size-3" />
          <span className="tabular-nums">{instanceCount}</span>
        </span>
      </div>
      <div className="mt-0.5 truncate pl-4 text-[11px] text-muted-foreground" title={node.host}>
        {node.host}
      </div>
      <div className="mt-1.5 flex items-center gap-2 pl-4">
        <RowUsage label="C" pct={(node.cpuUsage ?? 0) * 100} online={online} />
        <RowUsage label="M" pct={(node.memoryUsage ?? 0) * 100} online={online} />
        {node.maintenance && (
          <Badge variant="outline" className="ml-auto h-4 px-1 text-[9px] text-status-warning border-status-warning/50">
            {t('nodes.maintenance')}
          </Badge>
        )}
      </div>
    </button>
  )
}

/** 收缩态窄轨中的单节点（仅状态点 + 名首字，hover tooltip 显名，点选中）。 */
function NodeRailIcon({
  node,
  selected,
  onSelect,
}: {
  node: NodeInfo
  selected: boolean
  onSelect: () => void
}) {
  const online = node.status === 1
  const level = nodeStatusLevel(node.status)
  return (
    <button
      type="button"
      onClick={onSelect}
      aria-current={selected}
      title={`${node.name} · ${node.host}`}
      className={cn(
        'relative grid size-9 place-items-center rounded-lg border text-xs font-semibold uppercase transition-colors',
        selected ? 'border-primary/40 bg-accent text-primary' : 'border-transparent text-foreground/70 hover:bg-accent/60',
        !online && 'opacity-60',
      )}
    >
      {node.name.slice(0, 1) || '?'}
      <span
        className={cn('absolute -right-0.5 -top-0.5 size-2 rounded-full ring-2 ring-card', online && 'animate-breathing')}
        style={{ backgroundColor: `var(--status-${level === 'neutral' ? 'info' : level})` }}
        aria-hidden
      />
    </button>
  )
}

export default function NodesPage() {
  const { t } = useTranslation()
  const { data: nodes, isLoading } = useNodes({ refetchInterval: 30_000 })
  const { data: instances } = useInstances()

  const [selectedId, setSelectedId] = useState<number | null>(null)
  const [query, setQuery] = useState('')
  const [pending, setPending] = useState<PendingAction | null>(null)
  const [addOpen, setAddOpen] = useState(false)
  const [tab, setTab] = useState<DetailTab>('overview')
  // 左栏收缩为窄图标轨（FR-177）：收缩态持久化（localStorage）。
  const [collapsed, setCollapsed] = useState(loadNodeListCollapsed)

  const setMaintenance = useSetNodeMaintenance()
  const drain = useDrainNode()
  const del = useDeleteNode()

  // 集群汇总（FR-144）：在线/离线/维护计数 + 在线节点资源水位均值。
  const summary = useMemo(() => summarizeNodes(nodes ?? []), [nodes])
  // 各节点实例数（统计一次，列表/详情共用）。
  const instanceCountByNode = useMemo(() => {
    const map = new Map<number, number>()
    for (const i of instances ?? []) map.set(i.nodeId, (map.get(i.nodeId) ?? 0) + 1)
    return map
  }, [instances])

  const filtered = useMemo(() => filterNodes(nodes ?? [], query), [nodes, query])
  // 选中节点解析为实时列表对象（节点下线→回空态，右栏随轮询刷新而非陈旧快照）。
  const selected = useMemo(() => resolveSelectedNode(nodes ?? [], selectedId), [nodes, selectedId])

  const toggleCollapsed = () => {
    setCollapsed((c) => {
      const next = !c
      persistNodeListCollapsed(next)
      return next
    })
  }

  // 选中节点被下线后清掉选中态（避免「幽灵选中」）。
  useEffect(() => {
    if (selectedId !== null && selected === null && (nodes?.length ?? 0) > 0) {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- 选中节点消失时清理，属合法同步
      setSelectedId(null)
    }
  }, [selectedId, selected, nodes])

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

  const summaryChips: SummaryChip[] = [
    { key: 'online', label: t('nodes.online'), count: summary.online, level: 'success', breathing: summary.online > 0 },
    { key: 'offline', label: t('nodes.offline'), count: summary.offline, level: 'danger' },
    { key: 'maintenance', label: t('nodes.maintenance'), count: summary.maintenance, level: 'warning' },
  ]
  const gauge = (pct: number | null) => (pct === null ? '--' : `${pct.toFixed(0)}%`)

  return (
    <div className="flex h-[calc(100vh-7rem)] min-h-0 gap-3">
      {/* 左栏：可收缩节点列表（窄图标轨 ⇄ 展开），收缩态持久 */}
      <aside
        className={cn(
          'flex min-h-0 shrink-0 flex-col rounded-xl border bg-card transition-[width] duration-200 ease-ios',
          collapsed ? 'w-14' : 'w-72',
        )}
      >
        {collapsed ? (
          <div className="flex min-h-0 flex-1 flex-col items-center gap-1.5 p-2">
            <button
              type="button"
              onClick={toggleCollapsed}
              aria-label={t('nodes.expandList')}
              title={t('nodes.expandList')}
              className="grid size-9 w-full place-items-center rounded-lg text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
            >
              <ChevronsRight className="size-4" />
            </button>
            <div className="flex min-h-0 flex-1 flex-col items-center gap-1.5 overflow-y-auto scrollbar-none">
              {filtered.map((node) => (
                <NodeRailIcon
                  key={node.id}
                  node={node}
                  selected={node.id === selectedId}
                  onSelect={() => setSelectedId(node.id)}
                />
              ))}
            </div>
          </div>
        ) : (
          <>
            <div className="shrink-0 space-y-2 border-b p-3">
              <div className="flex items-center justify-between gap-2">
                <h1 className="text-sm font-bold">{t('nodes.title')}</h1>
                <button
                  type="button"
                  onClick={toggleCollapsed}
                  aria-label={t('nodes.collapseList')}
                  title={t('nodes.collapseList')}
                  className="grid size-7 shrink-0 place-items-center rounded text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
                >
                  <ChevronsLeft className="size-4" />
                </button>
              </div>
              {/* 集群汇总头：状态计数 chip + CPU/内存/磁盘聚合水位（复用 summarizeNodes，FR-144） */}
              <SummaryChips chips={summaryChips} />
              <div className="grid grid-cols-3 gap-1.5">
                <StatCard label={t('nodes.cpu')} value={gauge(summary.cpuPct)} bar={summary.cpuPct !== null ? { value: summary.cpuPct, level: resourceLevel(summary.cpuPct) } : undefined} />
                <StatCard label={t('nodes.memory')} value={gauge(summary.memPct)} bar={summary.memPct !== null ? { value: summary.memPct, level: resourceLevel(summary.memPct) } : undefined} />
                <StatCard label={t('nodes.disk')} value={gauge(summary.diskPct)} bar={summary.diskPct !== null ? { value: summary.diskPct, level: resourceLevel(summary.diskPct) } : undefined} />
              </div>
              <div className="relative">
                <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
                <Input
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  placeholder={t('nodes.searchPlaceholder')}
                  className="h-8 pl-8 text-sm"
                  aria-label={t('nodes.searchPlaceholder')}
                />
              </div>
              <Button size="sm" className="w-full" onClick={() => setAddOpen(true)}>
                <Plus className="size-4" /> {t('nodes.enroll.addNode')}
              </Button>
            </div>
            <div className="min-h-0 flex-1 space-y-1 overflow-y-auto p-2">
              {isLoading ? (
                <p className="px-2 py-4 text-sm text-muted-foreground">{t('common.loading')}</p>
              ) : (nodes?.length ?? 0) === 0 ? (
                <p className="px-2 py-4 text-sm text-muted-foreground">{t('nodes.empty')}</p>
              ) : filtered.length === 0 ? (
                <p className="px-2 py-4 text-sm text-muted-foreground">{t('nodes.searchEmpty')}</p>
              ) : (
                filtered.map((node) => (
                  <NodeListRow
                    key={node.id}
                    node={node}
                    instanceCount={instanceCountByNode.get(node.id) ?? 0}
                    selected={node.id === selectedId}
                    onSelect={() => setSelectedId(node.id)}
                  />
                ))
              )}
            </div>
          </>
        )}
      </aside>

      {/* 右栏：选中节点详情（身份/操作/仪表 + 分段） */}
      <section className="min-h-0 min-w-0 flex-1 overflow-y-auto">
        {selected ? (
          <NodeDetailPane
            key={selected.id}
            node={selected}
            instanceCount={instanceCountByNode.get(selected.id) ?? 0}
            tab={tab}
            onTab={setTab}
            onToggleMaintenance={() => toggleMaintenance(selected)}
            onDrain={() => setPending({ kind: 'drain', node: selected })}
            onDelete={() => setPending({ kind: 'delete', node: selected })}
          />
        ) : (
          <div className="grid h-full place-items-center rounded-xl border border-dashed bg-card/40">
            <div className="flex flex-col items-center gap-2 text-center text-muted-foreground">
              <Server className="size-8 opacity-40" />
              <p className="text-sm">{t('nodes.selectHint')}</p>
            </div>
          </div>
        )}
      </section>

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

/** 右栏详情主体：身份块 + 资源仪表 + 分段 Tabs（切段稳定工具条，布局不重组）。 */
function NodeDetailPane({
  node,
  instanceCount,
  tab,
  onTab,
  onToggleMaintenance,
  onDrain,
  onDelete,
}: {
  node: NodeInfo
  instanceCount: number
  tab: DetailTab
  onTab: (t: DetailTab) => void
  onToggleMaintenance: () => void
  onDrain: () => void
  onDelete: () => void
}) {
  const { t } = useTranslation()
  const online = node.status === 1
  const level = nodeStatusLevel(node.status)
  const statusLabel = online ? t('nodes.online') : node.status === 2 ? t('nodes.starting') : t('nodes.offline')
  const loadPct = node.cpuCores > 0 ? ((node.loadAvg1 ?? 0) / node.cpuCores) * 100 : 0

  return (
    <div className="space-y-3">
      {/* 身份块：图标 + 名 + host + 系统/架构 + 状态徽标 + 操作 kebab */}
      <Panel bodyClassName="p-4">
        <div className="flex items-start gap-3">
          <span className={cn('flex size-11 shrink-0 items-center justify-center rounded-xl', toneChipClass(online ? 'primary' : 'neutral'))}>
            <Server className="size-5" />
          </span>
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2">
              <h2 className="truncate text-base font-semibold" title={node.name}>{node.name}</h2>
              <StatusBadge level={level} label={statusLabel} />
              {node.maintenance && (
                <Badge variant="outline" className="text-status-warning border-status-warning/50">
                  {t('nodes.maintenance')}
                </Badge>
              )}
            </div>
            <div className="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-xs text-muted-foreground">
              <span className="truncate" title={node.host}>{node.host}</span>
              <span>{node.os} {node.arch}</span>
              <span className="inline-flex items-center gap-1">
                <Box className="size-3" /> {instanceCount} {t('nodes.instancesUnit')}
              </span>
            </div>
          </div>
          <NodeActionsMenu node={node} onToggleMaintenance={onToggleMaintenance} onDrain={onDrain} onDelete={onDelete} />
        </div>

        {/* 资源仪表：CPU/内存/磁盘/负载（FR-061；离线归零空盘） */}
        <div className="mt-4 flex flex-wrap items-center gap-x-8 gap-y-3">
          <ResourceGauge label={t('nodes.cpu')} value={online ? (node.cpuUsage ?? 0) * 100 : 0} unit="%" size={78} />
          <ResourceGauge label={t('nodes.memory')} value={online ? (node.memoryUsage ?? 0) * 100 : 0} unit="%" size={78} />
          <ResourceGauge label={t('nodes.disk')} value={online ? (node.diskUsage ?? 0) * 100 : 0} unit="%" size={78} />
          <ResourceGauge label={t('nodes.load')} value={online ? loadPct : 0} unit="%" size={78} />
        </div>
      </Panel>

      {/* 分段 Tabs：固定工具条，切段不致下方内容上下重排（抽屉 UX 约束，FR-178 §5） */}
      <div className="flex flex-wrap gap-1 rounded-lg border bg-muted/30 p-1 text-sm">
        {DETAIL_TABS.map((k) => (
          <button
            key={k}
            type="button"
            onClick={() => onTab(k)}
            className={cn(
              'rounded-md px-3 py-1.5 transition-colors',
              tab === k ? 'bg-background font-medium shadow-sm' : 'text-muted-foreground hover:text-foreground',
            )}
          >
            {t(`nodes.tab.${k}`)}
          </button>
        ))}
      </div>

      <div>
        {tab === 'overview' && <NodeOverviewSection node={node} />}
        {tab === 'instances' && <NodeInstanceCompare node={node} range="24h" />}
        {tab === 'jdk' && <NodeJDKPanel nodeId={node.id} active />}
        {tab === 'cache' && <NodeArtifactCachePanel nodeId={node.id} active />}
        {tab === 'ports' && (
          <Panel title={t('ports.title')}>
            <NodePortsPanel nodeId={node.id} />
          </Panel>
        )}
        {tab === 'proxy' && (
          <Panel title={t('nodeProxy.title')}>
            <NodeProxyPanel nodeId={node.id} active />
          </Panel>
        )}
        {tab === 'monitor' && <NodeMonitorCharts node={node} />}
        {tab === 'repair' && <NodeRepairPanel node={node} active />}
      </div>
    </div>
  )
}
