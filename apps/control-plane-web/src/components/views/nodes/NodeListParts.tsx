import { useTranslation } from 'react-i18next'
import { Box } from 'lucide-react'
import { cn } from '@jianmanager/ui'
import { Badge } from '@jianmanager/ui/components/badge'
import { MiniBar } from '@jianmanager/ui/components/mini-bar'
import { Panel } from '@jianmanager/ui/components/panel'
import { nodeStatusLevel } from '@/lib/node-list'
import { Button } from '@jianmanager/ui/components/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@jianmanager/ui/components/dropdown-menu'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import type { ArchivedNode, NodeInfo } from '@jianmanager/ui/lib/node-types'

/** 将字节数格式化为人类可读的大小（B/KB/MB/GB）。 */
export function formatBytes(bytes: number): string {
  if (bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  const value = bytes / Math.pow(1024, i)
  return `${value.toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}
/** 详情「概览」分段：硬件 + 系统 + 网络等次要信息（FR-144）。 */
export function NodeOverviewSection({ node }: { node: NodeInfo }) {
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
/**
 * 节点身份块操作菜单（FR-144/FR-177）：进入/解除维护、排空、下线收入「⋯」kebab。
 * 排空与下线标危险色；下线在线节点禁用 + tooltip。
 */
export function NodeActionsMenu({
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
export function NodeCard({ node, instanceCount, onOpen }: { node: NodeInfo; instanceCount: number; onOpen: () => void }) {
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
export function NodeListRow({
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
export function NodeRailIcon({
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
/** 归档列表行：名称 / host / 下线时间（FR-393）。 */
export function ArchivedNodeListRow({
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
