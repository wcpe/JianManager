import { useTranslation } from 'react-i18next'
import { Box } from 'lucide-react'
import { cn } from '@jianmanager/ui'
import { Badge } from '@jianmanager/ui/components/badge'
import { Card } from '@jianmanager/ui/components/card'
import { MiniBar } from '@jianmanager/ui/components/mini-bar'
import { nodeStatusLevel } from '@/lib/nodes/node-list'
import { Button } from '@jianmanager/ui/components/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@jianmanager/ui/components/dropdown-menu'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import type { ArchivedNode, NodeInfo } from '@/lib/nodes/node-types'
/** 
 * 节点身份块操作菜单：进入/解除维护、排空、下线收入「⋯」kebab。
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
/** 
 * 单条资源水位。
 *
 * 【离线不画 0% 空轨】原先写的是 `value={online ? pct : 0}`：离线节点渲染成一条空轨，
 * 既不像「无数据」（看着像真的是 0%），又占着三格宽度，是最没信息量的一种表达。
 * 现在离线时直接不画条、改成一个明确的「无实时数据」占位短横，
 * 并保留 label 让用户知道这里本该有什么。
 */
function RowUsage({ label, pct, online }: { label: string; pct: number; online: boolean }) {
  const { t } = useTranslation()
  return (
    <div className="flex items-center gap-1">
      <span className="text-[9px] font-medium text-muted-foreground">{label}</span>
      {online ? (
        <MiniBar value={pct} className="w-10" />
      ) : (
        <span
          className="text-[9px] tabular-nums text-muted-foreground/50"
          title={t('nodes.noLiveData', '离线，无实时数据')}
          aria-label={t('nodes.noLiveData', '离线，无实时数据')}
        >
          --
        </span>
      )}
    </div>
  )
}

/** 
 * 节点卡片（阶段 6 重做，原型 `.node-card`）：列表态的主展示单元。
 *
 * 与 `NodeListRow` 分工不同——那是双栏形态下给「已选中节点」当导航用的紧凑行；
 * 卡片墙要在一屏里横向铺开、彼此可比，故把 host / 核数 / 内存 / 实例数 / 三项水位都摆出来。
 */
export function NodeCard({
  node,
  instanceCount,
  onOpen,
  selectable,
  selected,
  onToggleSelect,
}: {
  node: NodeInfo
  instanceCount: number
  onOpen: () => void
  /** 显示勾选框（批量操作）。 */
  selectable?: boolean
  selected?: boolean
  onToggleSelect?: (id: number) => void
}) {
  const { t } = useTranslation()
  const online = node.status === 1
  const level = nodeStatusLevel(node.status)
  const dotColor = `var(--status-${level === 'neutral' ? 'info' : level})`
  const statusLabel = node.maintenance ? t('nodes.maintenance') : online ? t('nodes.online') : t('nodes.offline')

  return (
    /* 整块可点击卡片 → Card 的 interactive（hover 抬升 + 焦点环）；额外 hover 涂层沿用原值。
       语义：原为 <button>，Card 渲染 div，故补 role/tabIndex 与 Enter/Space 键处理保持键盘可达。 */
    <Card
      interactive
      role="button"
      tabIndex={0}
      onClick={onOpen}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          onOpen()
        }
      }}
      // 整张卡片是一个按钮，若不指定可访问名，读屏会把卡片里的全部文本（名 / host /
      // 核数 / 内存 / 实例数 / 三项水位）串成一长串念出来；而且那串里含「在线」与数字，
      // 会与状态 chip 的正则断言互相干扰（实测触发 strict mode 冲突）。
      // 收敛为「节点名 + 状态」——状态 chip 已是页面级的概览，卡片这里表达的是「这台怎么样」。
      aria-label={`${node.name} ${statusLabel}`}
      className={cn(
        'cursor-pointer gap-2 p-3 text-left transition-colors hover:bg-accent/40',
        !online && 'opacity-70',
      )}
    >
      <div className="flex items-center gap-2">
        {/*
          勾选框放卡片左上：与整卡点击共存，必须 stopPropagation，
          否则勾选会同时把卡片打开（批量选择直接变成跳转）。
        */}
        {selectable && (
          <input
            type="checkbox"
            className="size-3.5 shrink-0 cursor-pointer accent-[var(--primary)]"
            checked={!!selected}
            onChange={(e) => {
              e.stopPropagation()
              onToggleSelect?.(node.id)
            }}
            onClick={(e) => e.stopPropagation()}
            aria-label={t('nodes.selectNode', { name: node.name })}
            data-testid={`node-card-select-${node.id}`}
          />
        )}
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
      {/*
        卡片补齐与表格一致的关键字段：端口 / 系统 / 隧道 / 心跳。
        原先卡片只有 名/host/核数/内存/实例数，切到卡片视图就丢掉了排障要看的那几项，
        用户得切回表格才能确认「这台是不是走隧道、端口是多少」。
      */}
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
        <span className="tabular-nums" title={t('nodes.grpcPort')}>
          {node.grpcPort || '--'}/{node.wsPort || '--'}
        </span>
        <span className="truncate" title={`${node.os} ${node.arch}`}>
          {node.os || '--'} {node.arch || ''}
        </span>
        <span className={node.tunnelConnected ? 'text-status-success' : undefined}>
          {node.tunnelConnected ? t('nodes.tunnelConnected') : t('nodes.tunnelDirect')}
        </span>
        <span title={node.lastHeartbeat ?? ''}>
          {relativeAge(node.lastHeartbeat)}
        </span>
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
    </Card>
  )
}

/** 心跳相对时间（卡片用）：`3s` / `5m` / `2h` / `3d`。 */
function relativeAge(iso: string | null): string {
  if (!iso) return '--'
  const sec = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000))
  if (!Number.isFinite(sec)) return '--'
  if (sec < 60) return `${sec}s`
  const min = Math.round(sec / 60)
  if (min < 60) return `${min}m`
  const hour = Math.round(min / 60)
  if (hour < 24) return `${hour}h`
  return `${Math.round(hour / 24)}d`
}

/** 左栏节点列表行：状态点呼吸灯 + 名 + host + mini 水位（CPU/内存）+ 实例数；选中高亮、离线置灰。 */
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
    // 列表行整行可点：卡片式外观（描边/圆角/选中态）全由 className 决定，
    // 故取 invisible + auto —— 按钮语义与焦点环交给 Button，视觉保持原样。
    <Button
      type="button"
      variant="invisible"
      size="auto"
      onClick={onSelect}
      aria-current={selected}
      className={cn(
        // block + font-normal：Button 基类是 inline-flex/font-medium，会把三行子块排成一行、
        // 且把 host 与水位的弱化文字一并加粗，故在此显式还原。
        'block w-full rounded-lg border px-2.5 py-2 text-left font-normal transition-colors',
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
    </Button>
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
    // 窄轨节点格：36px 方块的描边/选中态都是自绘，故取 invisible + auto。
    <Button
      type="button"
      variant="invisible"
      size="auto"
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
    </Button>
  )
}
/** 归档列表行：名称 / host / 下线时间。 */
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
    // 归档列表行：整行可点、外观自绘，故取 invisible + auto。
    <Button
      type="button"
      variant="invisible"
      size="auto"
      onClick={onSelect}
      className={cn(
        // font-normal：抵消基类的 font-medium，三行里只有名称该加粗（由子元素控制）。
        'flex w-full flex-col gap-0.5 rounded-lg border px-3 py-2 text-left font-normal transition-colors',
        selected ? 'border-primary/40 bg-primary/5' : 'border-transparent hover:bg-accent/50',
      )}
    >
      <span className="truncate text-sm font-medium" title={node.name}>{node.name}</span>
      <span className="truncate text-xs text-muted-foreground" title={node.host}>{node.host}</span>
      <span className="truncate text-[11px] text-muted-foreground">
        {t('nodes.deletedAt')}: {when}
      </span>
    </Button>
  )
}

/**
 * 归档节点列表（已下线节点）。
 *
 * 【为什么单独一个表】归档节点没有实时水位（CPU/内存/磁盘用量），塞进 `NodeTable`
 * 会出现一整列空条；这里只展示「它是谁、什么时候下线的、还剩什么要清理」。
 * 它也是归档视图唯一的入口——此前 `?view=archive` 只能手输 URL 到达，列表还显示活跃节点。
 */
export function ArchivedNodeList({
  nodes,
  onOpen,
  onPurge,
  purging,
}: {
  nodes: ArchivedNode[]
  onOpen: (id: number) => void
  onPurge: (node: ArchivedNode) => void
  purging: boolean
}) {
  const { t } = useTranslation()
  if (nodes.length === 0) {
    return <p className="py-10 text-center text-sm text-muted-foreground">{t('nodes.archivedEmpty')}</p>
  }
  return (
    <ul className="divide-y rounded-lg border bg-card/60">
      {nodes.map((n) => (
        <li key={n.id} className="flex items-start gap-3 px-3 py-2">
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-1.5">
              <span className="truncate text-sm font-medium text-foreground">{n.name}</span>
              <Badge variant="outline" className="text-[10px] text-muted-foreground">
                {t('nodes.viewArchive')}
              </Badge>
            </div>
            <div className="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-[11px] text-muted-foreground">
              <span className="font-mono">{n.host || '--'}</span>
              <span className="tabular-nums">
                {n.grpcPort || '--'} / {n.wsPort || '--'}
              </span>
              <span>{t('nodes.archivedAt')} {formatArchivedAt(n.deletedAt)}</span>
            </div>
          </div>
          <div className="flex shrink-0 items-center gap-1">
            <Button variant="ghost" size="sm" onClick={() => onOpen(n.id)}>
              {t('common.details')}
            </Button>
            <Button variant="outline" size="sm" className="text-destructive" disabled={purging} onClick={() => onPurge(n)}>
              {t('nodes.purge')}
            </Button>
          </div>
        </li>
      ))}
    </ul>
  )
}

/** 归档时间：`YYYY-MM-DD HH:mm`；无效值回退 `--`。 */
function formatArchivedAt(iso: string | null | undefined): string {
  if (!iso) return '--'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '--'
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}
