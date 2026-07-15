import { useMemo, useState } from 'react'
import { useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
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
  type NodeDeleteBlockedInstance,
} from '@/api/nodes'
import { useInstanceAggregate, useInstanceSearch } from '@/api/instances'
import { useMetricSeries, useMetricSeriesBatch } from '@/api/metrics'
import { Badge } from '@jianmanager/ui/components/badge'
import { Panel } from '@jianmanager/ui/components/panel'
import { Input } from '@jianmanager/ui/components/input'
import { MiniBar } from '@jianmanager/ui/components/mini-bar'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import { ResourceGauge } from '@jianmanager/ui/components/gauge'
import { StatCard } from '@jianmanager/ui/components/stat-card'
import { SummaryChips, type SummaryChip } from '@jianmanager/ui/components/summary-chips'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
} from '@jianmanager/ui/components/dropdown-menu'
import { TimeSeriesChart, type ChartSeries } from '@jianmanager/ui'
import { RangePicker, type MetricRange } from '@jianmanager/ui'
import { resourceLevel } from '@jianmanager/ui'
import { summarizeNodes } from '@/lib/node-summary'
import {
  nodeStatusLevel,
  filterNodes,
  resolveSelectedNode,
  loadNodeListCollapsed,
  persistNodeListCollapsed,
} from '@/lib/node-list'
import { toneChipClass } from '@/lib/tone'
import { cn } from '@jianmanager/ui'

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import {
  scrollableDialogContentClass,
  ScrollableDialogBody,
} from '@jianmanager/ui/components/scrollable-dialog'
import NodeJDKPanel from '@/components/NodeJDKPanel'
import NodePortsPanel from '@/components/NodePortsPanel'
import NodeArtifactCachePanel from '@/components/NodeArtifactCachePanel'
import NodeProxyPanel from '@/components/NodeProxyPanel'
import NodeRepairPanel from '@/components/NodeRepairPanel'
import DangerConfirm from '@/components/DangerConfirm'
import AddNodeDialog from '@/components/AddNodeDialog'
import { Button } from '@jianmanager/ui/components/button'

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

/** 节点下线被实例守卫 409 拒绝的上下文（FR-309）：节点 + 名下实例清单。 */
type DeleteConflict = { node: NodeInfo; instances: NodeDeleteBlockedInstance[] }

/**
 * 节点下线被实例守卫拒绝的清单模态（FR-309）：列出名下实例（名称 + 状态）；
 * 离线节点额外提供「强制下线」入口（级联删平台记录、明示不清理远端文件）。
 */
function NodeDeleteBlockedDialog({
  conflict,
  onClose,
  onForce,
}: {
  conflict: DeleteConflict | null
  onClose: () => void
  onForce: () => void
}) {
  const { t } = useTranslation()
  // 实例状态 → 既有 instances.* i18n 文案；未知状态原样展示兜底。
  const statusText = (status: string) => {
    const keys: Record<string, string> = {
      STOPPED: 'instances.stopped',
      STARTING: 'instances.starting',
      RUNNING: 'instances.running',
      STOPPING: 'instances.stopping',
      CRASHED: 'instances.crashed',
    }
    return keys[status] ? t(keys[status]) : status
  }
  const offline = conflict !== null && conflict.node.status !== 1
  return (
    <Dialog open={conflict !== null} onOpenChange={(v: boolean) => { if (!v) onClose() }}>
      <DialogContent className={scrollableDialogContentClass}>
        <DialogHeader>
          <DialogTitle>{t('nodes.deleteBlockedTitle')}</DialogTitle>
          <DialogDescription>
            {t('nodes.deleteBlockedDesc', { name: conflict?.node.name, count: conflict?.instances.length })}
          </DialogDescription>
        </DialogHeader>
        <ScrollableDialogBody className="space-y-1.5">
          {(conflict?.instances ?? []).map((inst) => (
            <div key={inst.id} className="flex items-center justify-between gap-2 rounded-md border px-3 py-1.5 text-sm">
              <span className="min-w-0 truncate" title={inst.name}>{inst.name}</span>
              <Badge variant="outline" className="shrink-0 text-[11px] text-muted-foreground">
                {statusText(inst.status)}
              </Badge>
            </div>
          ))}
          {offline && (
            <p className="pt-1 text-xs text-muted-foreground">{t('nodes.deleteBlockedForceHint')}</p>
          )}
        </ScrollableDialogBody>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>{t('common.cancel')}</Button>
          {offline && (
            <Button variant="destructive" onClick={onForce}>{t('nodes.forceDelete')}</Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** 右栏分段（FR-177 §3.3 + FR-185）：概览/实例/JDK/缓存/端口/代理/监控/坏节点修复。 */
type DetailTab = 'overview' | 'instances' | 'runtime' | 'cache' | 'ports' | 'proxy' | 'monitor' | 'repair'
const DETAIL_TABS: DetailTab[] = ['overview', 'instances', 'runtime', 'cache', 'ports', 'proxy', 'monitor', 'repair']

/** 从 URL `?tab=` 解析激活分段（FR-128 可寻址；非法值回退默认 overview）。 */
function readDetailTab(searchParams: URLSearchParams): DetailTab {
  const tab = searchParams.get('tab')
  if (tab === 'jdk') return 'runtime' // 旧链接兼容：tab=jdk → 运行时
  return DETAIL_TABS.includes(tab as DetailTab) ? (tab as DetailTab) : 'overview'
}

/** 各实例对比图可切的指标（FR-060 #2：节点上各实例 TPS/MSPT/堆/线程对比）。 */
const COMPARE_METRICS: { key: string; labelKey: string; fmt: (v: number) => string }[] = [
  { key: 'inst_tps', labelKey: 'metrics.tps', fmt: (v) => v.toFixed(1) },
  { key: 'inst_mspt', labelKey: 'metrics.mspt', fmt: (v) => `${v.toFixed(1)}ms` },
  { key: 'inst_heap_used', labelKey: 'metrics.heap', fmt: formatBytes },
  { key: 'inst_threads', labelKey: 'metrics.threads', fmt: (v) => v.toFixed(0) },
]

/** 对比图可读性上限：一图最多 12 条线（超出仅取名称升序前 12，FR-340）。50 是后端硬上限。 */
const COMPARE_TARGET_CAP = 12

/**
 * 节点上各实例同一指标对比：每实例一条线，可切 TPS/MSPT/堆/线程（FR-060 #2）。
 * FR-340：实例清单走服务端按节点分页（`/instances/search`）取前 12（名称升序），
 * 指标一次批量查询（`/metrics/series/batch`）拆分为各线，消 N+1 请求风暴。
 */
function NodeInstanceCompare({ node, range }: { node: NodeInfo; range: MetricRange }) {
  const { t } = useTranslation()
  const [metric, setMetric] = useState('inst_tps')
  const spec = COMPARE_METRICS.find((m) => m.key === metric) ?? COMPARE_METRICS[0]

  // 服务端按节点过滤分页取前 12（名称升序）；total 为该节点实例总数（提示中的 N）。
  const { data: search } = useInstanceSearch({
    nodeId: node.id,
    pageSize: COMPARE_TARGET_CAP,
    sort: 'name',
    order: 'asc',
  })
  const nodeInstances = search?.items ?? []
  const total = search?.total ?? 0
  const targetIds = nodeInstances.map((i) => i.uuid)

  // 单条批量查询替代逐实例 useQueries × N（FR-340）。
  const { data: batch } = useMetricSeriesBatch({ scope: 'instance', targetIds, range, metrics: [metric] })

  const series: ChartSeries[] = nodeInstances.map((inst) => {
    const s = batch?.series[inst.uuid]?.find((x) => x.metricKey === metric && x.world === '')
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
      {total > COMPARE_TARGET_CAP && (
        <p className="mb-2 text-xs text-muted-foreground">
          {t('nodes.compareCap', { shown: COMPARE_TARGET_CAP, total })}
        </p>
      )}
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
  // 各节点实例数走服务端聚合（FR-247/FR-270）：byNode 每节点计数已在服务端算好，
  // 本页不再全量拉取实例再前端归并。
  const { data: aggregate } = useInstanceAggregate()

  // 选中节点与激活分段均入 URL（FR-128 可寻址）：`?node=<id>` 深链（命令面板 FR-241 跳转携带）、
  // `?tab=<DetailTab>` 激活分段（默认 overview 省略）。直接从 searchParams 派生，浏览器前进/后退自动生效。
  const [searchParams, setSearchParams] = useSearchParams()
  const selectedId = (() => {
    const n = Number(searchParams.get('node'))
    return Number.isFinite(n) && n > 0 ? n : null
  })()
  const tab = readDetailTab(searchParams)
  const [query, setQuery] = useState('')
  const [pending, setPending] = useState<PendingAction | null>(null)
  // FR-309：下线被实例守卫 409 拒绝 → 清单模态；离线节点走强制下线需再过一道输入名称确认。
  const [conflict, setConflict] = useState<DeleteConflict | null>(null)
  const [forcePending, setForcePending] = useState<DeleteConflict | null>(null)
  const [addOpen, setAddOpen] = useState(false)

  // 选中节点写入 URL（保留当前 tab 等其它参数）。
  const setSelectedId = (id: number) => {
    const next = new URLSearchParams(searchParams)
    next.set('node', String(id))
    setSearchParams(next)
  }
  // 切换激活分段写入 URL（默认 overview 省略，保持链接简洁）。
  const setTab = (next: DetailTab) => {
    const params = new URLSearchParams(searchParams)
    if (next === 'overview') params.delete('tab')
    else params.set('tab', next)
    setSearchParams(params)
  }
  // 左栏收缩为窄图标轨（FR-177）：收缩态持久化（localStorage）。
  const [collapsed, setCollapsed] = useState(loadNodeListCollapsed)

  const setMaintenance = useSetNodeMaintenance()
  const drain = useDrainNode()
  const del = useDeleteNode()

  // 集群汇总（FR-144）：在线/离线/维护计数 + 在线节点资源水位均值。
  const summary = useMemo(() => summarizeNodes(nodes ?? []), [nodes])
  // 各节点实例数（服务端聚合 byNode，列表/详情共用）。
  const instanceCountByNode = useMemo(() => {
    const map = new Map<number, number>()
    for (const { nodeId, count } of aggregate?.byNode ?? []) map.set(nodeId, count)
    return map
  }, [aggregate])

  const filtered = useMemo(() => filterNodes(nodes ?? [], query), [nodes, query])
  // 有效选中（FR-232 进入默认选第一个 + FR-177 幽灵选中回退）：基于搜索后的 filtered 派生——
  // 未显式选中/选中项不在筛选结果内 → 回退筛选结果第一个；搜索无匹配 → null（右栏落空态，不留旧详情）。
  // 派生而非用 effect 同步 state（避免 set-state-in-effect 级联；selectedId 仍保留用户最后点选）。
  const effectiveSelectedId = useMemo(() => {
    if (filtered.length === 0) return null
    if (selectedId !== null && filtered.some((n) => n.id === selectedId)) return selectedId
    return filtered[0].id
  }, [filtered, selectedId])
  // 选中节点解析为实时列表对象（节点下线→回退第一个，右栏随轮询刷新而非陈旧快照）。
  const selected = useMemo(() => resolveSelectedNode(filtered, effectiveSelectedId), [filtered, effectiveSelectedId])

  const toggleCollapsed = () => {
    setCollapsed((c) => {
      const next = !c
      persistNodeListCollapsed(next)
      return next
    })
  }

  const [maintenanceTarget, setMaintenanceTarget] = useState<NodeInfo | null>(null)

  const runMaintenance = (node: NodeInfo, enabled: boolean) => {
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

  // 进入维护会中断新实例调度（不影响运行实例、可退出回退），故进入方向加二次确认；退出无害直接执行。
  const toggleMaintenance = (node: NodeInfo) => {
    if (!node.maintenance) {
      setMaintenanceTarget(node)
      return
    }
    runMaintenance(node, false)
  }

  const confirmMaintenance = () => {
    if (!maintenanceTarget) return
    runMaintenance(maintenanceTarget, true)
    setMaintenanceTarget(null)
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
      del.mutate({ id: node.id }, {
        onSuccess: () => toast.success(t('nodes.deleted')),
        onError: (e: Error & { response?: { status?: number; data?: { error?: string; message?: string; instances?: NodeDeleteBlockedInstance[] } } }) => {
          // FR-309：名下有实例被守卫拒绝 → 弹实例清单模态（离线节点内含强制下线入口）。
          if (e?.response?.status === 409 && e.response.data?.error === 'NODE_HAS_INSTANCES') {
            setConflict({ node, instances: e.response.data.instances ?? [] })
            return
          }
          toast.error(e?.response?.data?.message || t('common.error'))
        },
      })
    }
  }

  // FR-309 强制下线（仅离线节点）：级联删除名下实例的平台记录，明示不清理远端文件。
  const confirmForceDelete = () => {
    if (!forcePending) return
    const { node } = forcePending
    setForcePending(null)
    del.mutate({ id: node.id, force: true }, {
      onSuccess: (res) => toast.success(t('nodes.forceDeleted', { count: res.data.instancesPurged })),
      onError: (e: Error & { response?: { data?: { message?: string } } }) =>
        toast.error(e?.response?.data?.message || t('common.error')),
    })
  }

  const summaryChips: SummaryChip[] = [
    { key: 'online', label: t('nodes.online'), count: summary.online, level: 'success', breathing: summary.online > 0 },
    { key: 'offline', label: t('nodes.offline'), count: summary.offline, level: 'danger' },
    { key: 'maintenance', label: t('nodes.maintenance'), count: summary.maintenance, level: 'warning' },
  ]
  const gauge = (pct: number | null) => (pct === null ? '--' : `${pct.toFixed(0)}%`)

  return (
    <div data-page="nodes" className="jm-page-stack flex h-auto min-h-0 flex-col gap-3 lg:h-[calc(100vh-8.25rem)] lg:flex-row">
      {/* 左栏：可收缩节点列表（窄图标轨 ⇄ 展开），收缩态持久 */}
      <aside
        className={cn(
          'flex min-h-0 shrink-0 flex-col rounded-lg border bg-card/95 shadow-soft backdrop-blur-sm transition-[width] duration-200 ease-ios',
          collapsed ? 'w-full lg:w-14' : 'w-full lg:w-72',
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
                  selected={node.id === effectiveSelectedId}
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
                    selected={node.id === effectiveSelectedId}
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
          <div className="grid h-full place-items-center rounded-lg border border-dashed bg-card/50 shadow-soft">
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
      {/* FR-309：下线被实例守卫拒绝的清单模态 + 离线节点强制下线确认（输入名称）。 */}
      <NodeDeleteBlockedDialog
        conflict={conflict}
        onClose={() => setConflict(null)}
        onForce={() => {
          setForcePending(conflict)
          setConflict(null)
        }}
      />
      <DangerConfirm
        open={forcePending !== null}
        title={t('nodes.forceDeleteConfirmTitle')}
        description={t('nodes.forceDeleteConfirmDesc', {
          name: forcePending?.node.name,
          count: forcePending?.instances.length,
        })}
        confirmLabel={t('nodes.forceDelete')}
        confirmText={forcePending?.node.name}
        scope="platform"
        onConfirm={confirmForceDelete}
        onCancel={() => setForcePending(null)}
      />
      <DangerConfirm
        open={maintenanceTarget !== null}
        title={t('nodes.maintenanceConfirmTitle')}
        description={t('nodes.maintenanceConfirmDesc', { name: maintenanceTarget?.name ?? '' })}
        confirmLabel={t('nodes.enterMaintenance')}
        pending={setMaintenance.isPending}
        onConfirm={confirmMaintenance}
        onCancel={() => setMaintenanceTarget(null)}
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
              {/* 反向隧道状态（FR-281，见 ADR-066）：仅在线节点有意义——隧道已连=指令免入站；直拨回退=走 node.Host:GRPCPort */}
              {online && (
                <Badge
                  variant="outline"
                  className={node.tunnelConnected ? 'text-status-success border-status-success/50' : 'text-muted-foreground'}
                  title={node.tunnelConnected ? t('nodes.tunnelConnectedHint') : t('nodes.tunnelDirectHint')}
                >
                  {node.tunnelConnected ? t('nodes.tunnelConnected') : t('nodes.tunnelDirect')}
                </Badge>
              )}
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
          {/* 资源仪表内联右置（FR-311 v2）：用掉身份行右侧空区，紧凑一排贴着 kebab；
              仪表与身份同排信息密度高、不再单独占一行摊开。窄屏（<md）降级为下方 2×2。 */}
          <div className="hidden shrink-0 items-center gap-5 md:flex">
            <ResourceGauge label={t('nodes.cpu')} value={online ? (node.cpuUsage ?? 0) * 100 : 0} unit="%" size={56} />
            <ResourceGauge label={t('nodes.memory')} value={online ? (node.memoryUsage ?? 0) * 100 : 0} unit="%" size={56} />
            <ResourceGauge label={t('nodes.disk')} value={online ? (node.diskUsage ?? 0) * 100 : 0} unit="%" size={56} />
            <ResourceGauge label={t('nodes.load')} value={online ? loadPct : 0} unit="%" size={56} />
          </div>
          <NodeActionsMenu node={node} onToggleMaintenance={onToggleMaintenance} onDrain={onDrain} onDelete={onDelete} />
        </div>

        {/* 窄屏降级：仪表 2×2 紧凑网格（md 以上已内联到身份行）。 */}
        <div className="mt-3 grid grid-cols-2 gap-2 md:hidden">
          <div className="flex justify-center"><ResourceGauge label={t('nodes.cpu')} value={online ? (node.cpuUsage ?? 0) * 100 : 0} unit="%" size={56} /></div>
          <div className="flex justify-center"><ResourceGauge label={t('nodes.memory')} value={online ? (node.memoryUsage ?? 0) * 100 : 0} unit="%" size={56} /></div>
          <div className="flex justify-center"><ResourceGauge label={t('nodes.disk')} value={online ? (node.diskUsage ?? 0) * 100 : 0} unit="%" size={56} /></div>
          <div className="flex justify-center"><ResourceGauge label={t('nodes.load')} value={online ? loadPct : 0} unit="%" size={56} /></div>
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
        {tab === 'runtime' && <NodeJDKPanel nodeId={node.id} active />}
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
