import { useMemo, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router'
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
  useArchivedNodes,
  usePurgeArchivedNode,
  type NodeInfo,
  type ArchivedNode,
  type NodeDeleteBlockedInstance,
} from '@/api/nodes'
import { useInstanceAggregate, useInstanceSearch } from '@/api/instances'
import { useMetricSeries, useMetricSeriesBatch } from '@/api/metrics'
import { Badge } from '@jianmanager/ui/components/badge'
import { ObjectPageHeader } from '@jianmanager/ui/components/shell'
import { Panel } from '@jianmanager/ui/components/panel'
import { Input } from '@jianmanager/ui/components/input'
import { MiniBar } from '@jianmanager/ui/components/mini-bar'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import { ResourceGauge } from '@jianmanager/ui/components/gauge'
import { StatCard } from '@jianmanager/ui/components/stat-card'
import { SummaryChips, type SummaryChip } from '@jianmanager/ui/components/summary-chips'
import { CardsGrid, DataPanelSkeleton, PageHeader, PageShell, ScopeBar, Segment, Segments } from '@jianmanager/ui/components/layout'
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
import NodeJDKTab from '@/components/nodes/NodeJDKTab'
import NodeLogRuntimeTab from '@/components/nodes/NodeLogRuntimeTab'
import NodePortsTab from '@/components/nodes/NodePortsTab'
import NodeArtifactCacheTab from '@/components/nodes/NodeArtifactCacheTab'
import NodeProxyTab from '@/components/nodes/NodeProxyTab'
import NodeProbeVersionTab from '@/components/nodes/NodeProbeVersionTab'
import NodeRepairTab from '@/components/nodes/NodeRepairTab'
import DangerConfirm from '@/components/DangerConfirm'
import AddNodeDialogContainer from '@/components/nodes/AddNodeDialogContainer'
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

/** 归档清理被实例守卫 409 拒绝的上下文（FR-394）。 */
type PurgeConflict = { node: ArchivedNode; instances: NodeDeleteBlockedInstance[] }

/** 页面级视图：活跃 | 归档（FR-393，URL `?view=` 可寻址，默认 active）。 */
type NodesView = 'active' | 'archive'

function readNodesView(searchParams: URLSearchParams): NodesView {
  return searchParams.get('view') === 'archive' ? 'archive' : 'active'
}

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

/**
 * 归档清理被实例守卫拒绝的清单模态（FR-394）：列出名下实例记录；
 * 提供「强制清理」入口（级联硬删平台记录、明示不清理远端文件）。
 */
function NodePurgeBlockedDialog({
  conflict,
  onClose,
  onForce,
}: {
  conflict: PurgeConflict | null
  onClose: () => void
  onForce: () => void
}) {
  const { t } = useTranslation()
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
  return (
    <Dialog open={conflict !== null} onOpenChange={(v: boolean) => { if (!v) onClose() }}>
      <DialogContent className={scrollableDialogContentClass}>
        <DialogHeader>
          <DialogTitle>{t('nodes.purgeBlockedTitle')}</DialogTitle>
          <DialogDescription>
            {t('nodes.purgeBlockedDesc', { name: conflict?.node.name, count: conflict?.instances.length })}
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
          <p className="pt-1 text-xs text-muted-foreground">{t('nodes.purgeBlockedForceHint')}</p>
        </ScrollableDialogBody>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>{t('common.cancel')}</Button>
          <Button variant="destructive" onClick={onForce}>{t('nodes.forcePurge')}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** 右栏分段（FR-177 §3.3 + FR-185）：概览/实例/JDK/缓存/端口/代理/监控/坏节点修复。 */
type DetailTab = 'overview' | 'instances' | 'runtime' | 'cache' | 'ports' | 'proxy' | 'probe' | 'monitor' | 'repair'
const DETAIL_TABS: DetailTab[] = ['overview', 'instances', 'runtime', 'cache', 'ports', 'proxy', 'probe', 'monitor', 'repair']

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

/**
 * 节点卡片（阶段 6 重做，原型 `.node-card`）：列表态的主展示单元。
 *
 * 与 `NodeListRow` 分工不同——那是双栏形态下给「已选中节点」当导航用的紧凑行；
 * 卡片墙要在一屏里横向铺开、彼此可比，故把 host / 核数 / 内存 / 实例数 / 三项水位都摆出来。
 */
function NodeCard({ node, instanceCount, onOpen }: { node: NodeInfo; instanceCount: number; onOpen: () => void }) {
  const { t } = useTranslation()
  const online = node.status === 1
  const level = nodeStatusLevel(node.status)
  const dotColor = `var(--status-${level === 'neutral' ? 'info' : level})`
  const statusLabel = node.maintenance ? t('nodes.maintenance') : online ? t('nodes.online') : t('nodes.offline')

  return (
    <button
      type="button"
      onClick={onOpen}
      // 整张卡片是一个按钮，若不指定可访问名，读屏会把卡片里的全部文本（名 / host /
      // 核数 / 内存 / 实例数 / 三项水位）串成一长串念出来；而且那串里含「在线」与数字，
      // 会与状态 chip 的正则断言互相干扰（实测触发 strict mode 冲突）。
      // 收敛为「节点名 + 状态」——状态 chip 已是页面级的概览，卡片这里表达的是「这台怎么样」。
      aria-label={`${node.name} ${statusLabel}`}
      className={cn(
        'flex flex-col gap-2 rounded-lg border bg-card p-3 text-left shadow-soft transition-colors hover:bg-accent/40',
        !online && 'opacity-70',
      )}
    >
      <div className="flex items-center gap-2">
        <span
          className={cn('size-2 shrink-0 rounded-full', online && 'animate-breathing')}
          style={{ backgroundColor: dotColor, color: dotColor }}
          aria-hidden
        />
        <span className="min-w-0 flex-1 truncate text-sm font-semibold" title={node.name}>
          {node.name}
        </span>
        <StatusBadge level={level} label={statusLabel} />
      </div>
      <div className="truncate text-[11px] text-muted-foreground" title={node.host}>
        {node.host}
      </div>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
        <span>
          {node.cpuCores} {t('nodes.cpuCores')}
        </span>
        <span>{Math.round(node.memoryMb / 1024)} GB</span>
        <span className="inline-flex items-center gap-0.5">
          <Box className="size-3" />
          <span className="tabular-nums">{instanceCount}</span>
        </span>
      </div>
      <div className="flex items-center gap-3">
        <RowUsage label={t('nodes.cpu')} pct={(node.cpuUsage ?? 0) * 100} online={online} />
        <RowUsage label={t('nodes.memory')} pct={(node.memoryUsage ?? 0) * 100} online={online} />
        <RowUsage label={t('nodes.disk')} pct={(node.diskUsage ?? 0) * 100} online={online} />
      </div>
    </button>
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
  // 选中节点与激活分段均入 URL（FR-128 可寻址）：`?node=<id>` 深链（命令面板 FR-241 跳转携带）、
  // `?tab=<DetailTab>` 激活分段（默认 overview 省略）；`?view=active|archive` 页面视图（FR-393）。
  const [searchParams, setSearchParams] = useSearchParams()
  const view = readNodesView(searchParams)
  const isArchive = view === 'archive'
  const { data: nodes, isLoading } = useNodes({
    refetchInterval: 30_000,
    enabled: !isArchive,
  })
  const { data: archivedNodes, isLoading: archivedLoading } = useArchivedNodes({
    enabled: isArchive,
  })
  // 各节点实例数走服务端聚合（FR-247/FR-270）：byNode 每节点计数已在服务端算好，
  // 本页不再全量拉取实例再前端归并。
  const { data: aggregate } = useInstanceAggregate()

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
  // FR-394：归档清理确认 / 实例守卫 / force 确认。
  const [purgeTarget, setPurgeTarget] = useState<ArchivedNode | null>(null)
  const [purgeConflict, setPurgeConflict] = useState<PurgeConflict | null>(null)
  const [forcePurgePending, setForcePurgePending] = useState<PurgeConflict | null>(null)
  const [addOpen, setAddOpen] = useState(false)

  // 选中节点写入 URL（保留当前 tab/view 等其它参数）。
  const setSelectedId = (id: number) => {
    const next = new URLSearchParams(searchParams)
    next.set('node', String(id))
    setSearchParams(next)
  }
  // 切换活跃/归档视图（FR-393）；切视图时清 node/tab，避免跨视图幽灵选中。
  const setView = (next: NodesView) => {
    const params = new URLSearchParams(searchParams)
    if (next === 'active') params.delete('view')
    else params.set('view', 'archive')
    params.delete('node')
    params.delete('tab')
    setSearchParams(params)
    setQuery('')
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
  const purge = usePurgeArchivedNode()

  // 集群汇总（FR-144）：在线/离线/维护计数 + 在线节点资源水位均值。
  const summary = useMemo(() => summarizeNodes(nodes ?? []), [nodes])
  // 各节点实例数（服务端聚合 byNode，列表/详情共用）。
  const instanceCountByNode = useMemo(() => {
    const map = new Map<number, number>()
    for (const { nodeId, count } of aggregate?.byNode ?? []) map.set(nodeId, count)
    return map
  }, [aggregate])

  const filtered = useMemo(() => filterNodes(nodes ?? [], query), [nodes, query])
  const filteredArchived = useMemo(() => {
    const q = query.trim().toLowerCase()
    const list = archivedNodes ?? []
    if (!q) return list
    return list.filter((n) => n.name.toLowerCase().includes(q) || n.host.toLowerCase().includes(q))
  }, [archivedNodes, query])
  // 有效选中（FR-232 进入默认选第一个 + FR-177 幽灵选中回退）：基于搜索后的 filtered 派生——
  // 未显式选中/选中项不在筛选结果内 → 回退筛选结果第一个；搜索无匹配 → null（右栏落空态，不留旧详情）。
  // 派生而非用 effect 同步 state（避免 set-state-in-effect 级联；selectedId 仍保留用户最后点选）。
  const effectiveSelectedId = useMemo(() => {
    const pool = isArchive ? filteredArchived : filtered
    if (pool.length === 0) return null
    if (selectedId !== null && pool.some((n) => n.id === selectedId)) return selectedId
    return pool[0].id
  }, [filtered, filteredArchived, isArchive, selectedId])
  // 选中节点解析为实时列表对象（节点下线→回退第一个，右栏随轮询刷新而非陈旧快照）。
  const selected = useMemo(
    () => (isArchive ? null : resolveSelectedNode(filtered, effectiveSelectedId)),
    [filtered, effectiveSelectedId, isArchive],
  )
  const selectedArchived = useMemo(() => {
    if (!isArchive || effectiveSelectedId === null) return null
    return filteredArchived.find((n) => n.id === effectiveSelectedId) ?? null
  }, [filteredArchived, effectiveSelectedId, isArchive])

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

  // FR-394：归档清理（无 force）；409 → 实例清单 → force DangerConfirm。
  const confirmPurge = () => {
    if (!purgeTarget) return
    const node = purgeTarget
    setPurgeTarget(null)
    purge.mutate(
      { id: node.id },
      {
        onSuccess: () => toast.success(t('nodes.purged')),
        onError: (e: Error & { response?: { status?: number; data?: { error?: string; message?: string; instances?: NodeDeleteBlockedInstance[] } } }) => {
          if (e?.response?.status === 409 && e.response.data?.error === 'NODE_HAS_INSTANCES') {
            setPurgeConflict({ node, instances: e.response.data.instances ?? [] })
            return
          }
          toast.error(e?.response?.data?.message || t('common.error'))
        },
      },
    )
  }

  const confirmForcePurge = () => {
    if (!forcePurgePending) return
    const { node } = forcePurgePending
    setForcePurgePending(null)
    purge.mutate(
      { id: node.id, force: true },
      {
        onSuccess: (res) => toast.success(t('nodes.forcePurged', { count: res.data.instancesPurged })),
        onError: (e: Error & { response?: { data?: { message?: string } } }) =>
          toast.error(e?.response?.data?.message || t('common.error')),
      },
    )
  }

  const summaryChips: SummaryChip[] = [
    { key: 'online', label: t('nodes.online'), count: summary.online, level: 'success', breathing: summary.online > 0 },
    { key: 'offline', label: t('nodes.offline'), count: summary.offline, level: 'danger' },
    { key: 'maintenance', label: t('nodes.maintenance'), count: summary.maintenance, level: 'warning' },
  ]
  const gauge = (pct: number | null) => (pct === null ? '--' : `${pct.toFixed(0)}%`)
  const listLoading = isArchive ? archivedLoading : isLoading

  // 阶段 6 页面重做（照原型 `nodesPage()`）：本页拆成**列表态**与**详情态**。
  //
  // 判据用 `selectedId` —— 它就是「URL 里有没有 node 参数」（null 即没有）。
  // 不能用 `effectiveSelectedId`：后者在无参数时会回退到第一个节点，
  // 那样列表态会错误地自动选中一个，用户以为进了详情。
  if (selectedId === null) {
    return (
      <PageShell data-page="nodes">
        <PageHeader
          title={t('nodes.title')}
          description={t('nodes.subtitle')}
          actions={
            <Button onClick={() => setAddOpen(true)}>
              <Plus className="size-4" />
              {t('nodes.enroll.addNode')}
            </Button>
          }
        />
        {/* 作用域条：状态计数即本页范围入口（点它按状态筛）+ 本地搜索（原型同款，
            搜索框在 scope-bar 内而不是页头操作区）。 */}
        <ScopeBar>
          <SummaryChips chips={summaryChips} />
          <div className="relative ml-auto">
            <Search className="pointer-events-none absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder={t('nodes.searchPlaceholder')}
              aria-label={t('nodes.searchPlaceholder')}
              className="h-8 w-56 pl-7"
            />
          </div>
        </ScopeBar>
        {listLoading ? (
          <DataPanelSkeleton />
        ) : (
          <CardsGrid className="min-h-0 flex-1">
            {filtered.map((node) => (
              <NodeCard
                key={node.id}
                node={node}
                instanceCount={instanceCountByNode.get(node.id) ?? 0}
                onOpen={() => setSelectedId(node.id)}
              />
            ))}
          </CardsGrid>
        )}
        <AddNodeDialogContainer open={addOpen} onClose={() => setAddOpen(false)} />
      </PageShell>
    )
  }

  return (
    // 全量对齐：详情态双栏（左栏节点列表 + 右栏详情）改用布局层 PageShell 的 fixed 壳态
    // ——固定视口、内部区域自行滚动，与列表态共用同一套留白与纵向节奏。
    // 原为手写骨架：jm-page-stack + h-[calc(100vh-8.25rem)] 硬编码视口高度 + 自定 gap。
    <PageShell variant="fixed" data-page="nodes" className="gap-3 lg:flex-row">
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
              {isArchive
                ? filteredArchived.map((node) => (
                    <button
                      key={node.id}
                      type="button"
                      onClick={() => setSelectedId(node.id)}
                      title={node.name}
                      aria-label={node.name}
                      className={cn(
                        'grid size-9 place-items-center rounded-lg border text-xs font-semibold transition-colors',
                        node.id === effectiveSelectedId
                          ? 'border-primary/40 bg-primary/10 text-primary'
                          : 'border-transparent text-muted-foreground hover:bg-accent/60',
                      )}
                    >
                      {node.name.slice(0, 1).toUpperCase()}
                    </button>
                  ))
                : filtered.map((node) => (
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
              {/* 活跃 | 归档 页面级分段（FR-393，URL ?view=） */}
              <Segments className="w-full" aria-label={t('nodes.title')}>
                {([
                  ['active', 'nodes.viewActive'],
                  ['archive', 'nodes.viewArchive'],
                ] as const).map(([k, labelKey]) => (
                  <Segment key={k} className="flex-1" active={view === k} onClick={() => setView(k)}>
                    {t(labelKey)}
                  </Segment>
                ))}
              </Segments>
              {!isArchive && (
                <>
                  {/* 集群汇总头：状态计数 chip + CPU/内存/磁盘聚合水位（复用 summarizeNodes，FR-144） */}
                  <SummaryChips chips={summaryChips} />
                  <div className="grid grid-cols-3 gap-1.5">
                    <StatCard label={t('nodes.cpu')} value={gauge(summary.cpuPct)} bar={summary.cpuPct !== null ? { value: summary.cpuPct, level: resourceLevel(summary.cpuPct) } : undefined} />
                    <StatCard label={t('nodes.memory')} value={gauge(summary.memPct)} bar={summary.memPct !== null ? { value: summary.memPct, level: resourceLevel(summary.memPct) } : undefined} />
                    <StatCard label={t('nodes.disk')} value={gauge(summary.diskPct)} bar={summary.diskPct !== null ? { value: summary.diskPct, level: resourceLevel(summary.diskPct) } : undefined} />
                  </div>
                </>
              )}
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
              {!isArchive && (
                <Button size="sm" className="w-full" onClick={() => setAddOpen(true)}>
                  <Plus className="size-4" /> {t('nodes.enroll.addNode')}
                </Button>
              )}
            </div>
            <div className="min-h-0 flex-1 space-y-1 overflow-y-auto p-2">
              {listLoading ? (
                <p className="px-2 py-4 text-sm text-muted-foreground">{t('common.loading')}</p>
              ) : isArchive ? (
                (archivedNodes?.length ?? 0) === 0 ? (
                  <p className="px-2 py-4 text-sm text-muted-foreground">{t('nodes.archivedEmpty')}</p>
                ) : filteredArchived.length === 0 ? (
                  <p className="px-2 py-4 text-sm text-muted-foreground">{t('nodes.searchEmpty')}</p>
                ) : (
                  filteredArchived.map((node) => (
                    <ArchivedNodeListRow
                      key={node.id}
                      node={node}
                      selected={node.id === effectiveSelectedId}
                      onSelect={() => setSelectedId(node.id)}
                    />
                  ))
                )
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

      {/* 右栏：活跃详情 / 归档只读详情（FR-393） */}
      {/* 阶段 6 第二步：详情态改用 PageShell 的 tool 变体。
          它原是裸 <section>——那时左栏列表与它同处一个 flex-row；两态分离后右栏独占整页，
          滚动收口到它自身（tool 变体是 gap-0 p-0，不叠加默认留白）。 */}
      <PageShell variant="tool" className="min-h-0 min-w-0 flex-1 overflow-y-auto">
        {isArchive ? (
          selectedArchived ? (
            <ArchivedNodeDetailPane
              key={selectedArchived.id}
              node={selectedArchived}
              onPurge={() => setPurgeTarget(selectedArchived)}
              purging={purge.isPending}
            />
          ) : (
            <div className="grid h-full place-items-center rounded-lg border border-dashed bg-card/50 shadow-soft">
              <div className="flex flex-col items-center gap-2 text-center text-muted-foreground">
                <Server className="size-8 opacity-40" />
                <p className="text-sm">{t('nodes.archiveSelectHint')}</p>
              </div>
            </div>
          )
        ) : selected ? (
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
      </PageShell>

      <AddNodeDialogContainer open={addOpen} onClose={() => setAddOpen(false)} />
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
      {/* FR-394：归档清理确认 + 实例守卫 + force 确认（文案明示不清理远端文件）。 */}
      <DangerConfirm
        open={purgeTarget !== null}
        title={t('nodes.purgeConfirmTitle')}
        description={t('nodes.purgeConfirmDesc', { name: purgeTarget?.name })}
        confirmLabel={t('nodes.purge')}
        confirmText={purgeTarget?.name}
        scope="platform"
        pending={purge.isPending}
        onConfirm={confirmPurge}
        onCancel={() => setPurgeTarget(null)}
      />
      <NodePurgeBlockedDialog
        conflict={purgeConflict}
        onClose={() => setPurgeConflict(null)}
        onForce={() => {
          setForcePurgePending(purgeConflict)
          setPurgeConflict(null)
        }}
      />
      <DangerConfirm
        open={forcePurgePending !== null}
        title={t('nodes.forcePurgeConfirmTitle')}
        description={t('nodes.forcePurgeConfirmDesc', {
          name: forcePurgePending?.node.name,
          count: forcePurgePending?.instances.length,
        })}
        confirmLabel={t('nodes.forcePurge')}
        confirmText={forcePurgePending?.node.name}
        scope="platform"
        pending={purge.isPending}
        onConfirm={confirmForcePurge}
        onCancel={() => setForcePurgePending(null)}
      />
    </PageShell>
  )
}

/** 归档列表行：名称 / host / 下线时间（FR-393）。 */
function ArchivedNodeListRow({
  node,
  selected,
  onSelect,
}: {
  node: ArchivedNode
  selected: boolean
  onSelect: () => void
}) {
  const { t } = useTranslation()
  const when = node.deletedAt ? new Date(node.deletedAt).toLocaleString() : '--'
  return (
    <button
      type="button"
      onClick={onSelect}
      className={cn(
        'flex w-full flex-col gap-0.5 rounded-lg border px-3 py-2 text-left transition-colors',
        selected ? 'border-primary/40 bg-primary/5' : 'border-transparent hover:bg-accent/50',
      )}
    >
      <span className="truncate text-sm font-medium" title={node.name}>{node.name}</span>
      <span className="truncate text-xs text-muted-foreground" title={node.host}>{node.host}</span>
      <span className="truncate text-[11px] text-muted-foreground">
        {t('nodes.deletedAt')}: {when}
      </span>
    </button>
  )
}

/** 归档只读详情 + 清理按钮（FR-393/394）。 */
function ArchivedNodeDetailPane({
  node,
  onPurge,
  purging,
}: {
  node: ArchivedNode
  onPurge: () => void
  purging: boolean
}) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const when = node.deletedAt ? new Date(node.deletedAt).toLocaleString() : '--'
  const rows: { label: string; value: React.ReactNode }[] = [
    { label: t('nodes.ip'), value: node.host },
    { label: t('nodes.system'), value: `${node.os || '--'} ${node.arch || ''}`.trim() },
    { label: t('nodes.cpuCores'), value: node.cpuCores > 0 ? node.cpuCores : '--' },
    { label: t('nodes.deletedAt'), value: when },
    { label: 'UUID', value: <span className="font-mono text-xs break-all">{node.uuid}</span> },
  ]
  return (
    <div className="space-y-3">
      {/* 阶段 6 收尾：归档详情与活跃详情用同一对象头形态（此处无分段工具，故不传 tools）。
          归档徽标走 status，「清理」走 actions，host 走 meta。 */}
      <ObjectPageHeader
        breadcrumbs={[
          { label: t('nodes.title'), to: '/nodes' },
          { label: node.name },
        ]}
        icon={<Server className="size-5" />}
        title={node.name}
        status={{ tone: 'default', label: t('nodes.viewArchive') }}
        meta={[{ label: t('nodes.ip'), value: node.host }]}
        actions={
          <Button variant="destructive" size="sm" onClick={onPurge} disabled={purging}>
            {t('nodes.purge')}
          </Button>
        }
        onNavigate={navigate}
      />
      <Panel title={t('nodes.overviewSection')}>
        <dl className="grid gap-2 sm:grid-cols-2">
          {rows.map((r) => (
            <div key={r.label} className="rounded-md border px-3 py-2">
              <dt className="text-[11px] text-muted-foreground">{r.label}</dt>
              <dd className="mt-0.5 text-sm">{r.value}</dd>
            </div>
          ))}
        </dl>
      </Panel>
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
  const navigate = useNavigate()
  const online = node.status === 1
  const statusLabel = online ? t('nodes.online') : node.status === 2 ? t('nodes.starting') : t('nodes.offline')
  const loadPct = node.cpuCores > 0 ? ((node.loadAvg1 ?? 0) / node.cpuCores) * 100 : 0

  return (
    <div className="space-y-3">
      {/* 阶段 6 第二步：身份块与分段 Tabs 合并为对象头（原型 `object-head` 的六段：
          面包屑 → 名/状态/元信息/操作 → 指标条 → 工具导航）。
          两个取舍：① 4 个环形仪表**不进 metrics**——原型 `object-stats` 是文字格，但
          FR-311 v2 明确「资源仪表内联右置」，那个设计比原型新且是有意的，故留在内容区；
          ② 隧道/维护徽标同理，对象头只有单个 status，它们在仪表行一并呈现。 */}
      <ObjectPageHeader
        breadcrumbs={[
          { label: t('nodes.title'), to: '/nodes' },
          { label: node.name },
        ]}
        icon={<Server className="size-5" />}
        title={node.name}
        status={{ tone: online ? 'success' : node.status === 2 ? 'warning' : 'default', label: statusLabel }}
        meta={[
          { label: t('nodes.ip'), value: node.host },
          { label: t('nodes.system'), value: `${node.os} ${node.arch}` },
          { label: t('nodes.instancesUnit'), value: instanceCount },
        ]}
        actions={
          <NodeActionsMenu node={node} onToggleMaintenance={onToggleMaintenance} onDrain={onDrain} onDelete={onDelete} />
        }
        tools={DETAIL_TABS.map((k) => ({
          key: k,
          label: t(`nodes.tab.${k}`),
          active: k === tab,
          onSelect: () => onTab(k),
        }))}
        toolsLabel={t('nodes.tools')}
        onNavigate={navigate}
      />

      {/* 资源仪表 + 隧道/维护徽标：对象头之下的独立一行（见上注的取舍①）。 */}
      <Panel bodyClassName="p-4">
        <div className="flex flex-wrap items-center gap-x-5 gap-y-2">
          <ResourceGauge label={t('nodes.cpu')} value={online ? (node.cpuUsage ?? 0) * 100 : 0} unit="%" size={56} />
          <ResourceGauge label={t('nodes.memory')} value={online ? (node.memoryUsage ?? 0) * 100 : 0} unit="%" size={56} />
          <ResourceGauge label={t('nodes.disk')} value={online ? (node.diskUsage ?? 0) * 100 : 0} unit="%" size={56} />
          <ResourceGauge label={t('nodes.load')} value={online ? loadPct : 0} unit="%" size={56} />
          <div className="flex flex-wrap items-center gap-2">
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
        </div>
      </Panel>

      <div>
        {tab === 'overview' && <NodeOverviewSection node={node} />}
        {tab === 'instances' && <NodeInstanceCompare node={node} range="24h" />}
        {tab === 'runtime' && (
          <div className="space-y-4">
            <NodeJDKTab nodeId={node.id} active />
            <NodeLogRuntimeTab nodeId={node.id} os={node.os} arch={node.arch} online={online} />
          </div>
        )}
        {tab === 'cache' && <NodeArtifactCacheTab nodeId={node.id} />}
        {tab === 'ports' && (
          <Panel title={t('ports.title')}>
            <NodePortsTab nodeId={node.id} />
          </Panel>
        )}
        {tab === 'proxy' && (
          <Panel title={t('nodeProxy.title')}>
            <NodeProxyTab nodeId={node.id} />
          </Panel>
        )}
        {tab === 'probe' && (
          <Panel title={t('probe.nodeVersionTitle')}>
            <NodeProbeVersionTab nodeId={node.id} />
          </Panel>
        )}
        {tab === 'monitor' && <NodeMonitorCharts node={node} />}
        {tab === 'repair' && <NodeRepairTab node={node} active />}
      </div>
    </div>
  )
}
