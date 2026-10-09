import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, Boxes, Server, Users } from 'lucide-react'

import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@jianmanager/ui/components/dropdown-menu'
import { cn } from '@jianmanager/ui'
import { slotVisibility, visibilityClass } from '@/lib/header-layout'

/** 集群统计浮窗行数上限（FR-294）：超出在底部提示「还有 N 个，查看全部」。 */
export const STAT_POPOVER_MAX_ROWS = 8

/** 浮窗槽位（外壳据此维护各自的 open 态，进而开关对应查询）。 */
export type ClusterSlot = 'nodes' | 'running' | 'crashed'

/** 在线节点浮窗的一行。 */
export interface ClusterNodeRow {
  id: number
  name: string
  /** 1 在线 / 其它离线。 */
  status: number
  /** 该节点上运行中的实例数。 */
  runningCount: number
}

/** 实例浮窗的一行（运行中 / 崩溃共用；崩溃行带 statusReason）。 */
export interface ClusterInstanceRow {
  id: number
  name: string
  nodeId: number
  nodeName?: string
  /** 崩溃原因（仅崩溃行有）。 */
  statusReason?: string
}

export interface ClusterBadgesProps {
  /** 在线节点数。 */
  online: number
  /** 运行中实例数。 */
  running: number
  /** 崩溃实例数。 */
  crashed: number
  /** 在线节点浮窗行（外壳取数，已按上限截断）。 */
  nodeRows?: ClusterNodeRow[]
  /** 运行中实例浮窗行。 */
  runningRows?: ClusterInstanceRow[]
  /** 崩溃实例浮窗行。 */
  crashedRows?: ClusterInstanceRow[]
  /** 各浮窗的「还有 N 个」剩余数。 */
  remaining: Record<ClusterSlot, number>
  /**
   * 每行的在线人数取数（外壳注入的 hook；包内按行顶层调用，符合 hooks 规则）。
   * 返回 undefined 表示无数据（不显示人数）。
   */
  useInstancePlayers?: (instanceId: number, enabled: boolean) => { available: boolean; online: number } | undefined
  /** 浮窗开合（外壳据此开关对应查询）。 */
  onSlotToggle: (slot: ClusterSlot, open: boolean) => void
  /** 跳节点详情（FR-128 深链 `/nodes?node=<id>`）。 */
  onOpenNode: (nodeId: number) => void
  /** 跳实例控制台。 */
  onOpenInstance: (instanceId: number) => void
  /** 各浮窗底部「查看全部」。 */
  onViewAll: (slot: ClusterSlot) => void
}

/**
 * 集群概览徽标组（FR-162；FR-294 点击改弹缩略浮窗）：在线节点 / 运行实例 / 崩溃数。
 * 徽标计数数据源不变（总览页同款聚合 + FR-247 聚合，避免页眉为计数拉全量实例）；
 * 原「点击跳筛选页」保留为浮窗底部「查看全部」。窄屏隐藏逻辑（header-layout）不变。
 *
 * 受控视图（ADR-097 c 范式）：三个计数、三个浮窗的行数据与全部跳转由外壳注入；
 * 浮窗自身的 open 态留视图内，经 `onSlotToggle` 上报，外壳据此把对应查询 enabled 绑到 open。
 */
export function ClusterBadges({
  online,
  running,
  crashed,
  nodeRows,
  runningRows,
  crashedRows,
  remaining,
  useInstancePlayers,
  onSlotToggle,
  onOpenNode,
  onOpenInstance,
  onViewAll,
}: ClusterBadgesProps) {
  const { t } = useTranslation()

  return (
    // `data-slot` 供外壳 CSS 在 lg~xl 之间收起本组（顶栏并回工作区切换后要腾宽度，见 index.css）。
    <div data-slot="console-header-badges" className={cn('items-center', visibilityClass(slotVisibility('clusterBadges')))}>
      <ClusterStatPopover icon={Server} value={online} label={t('header.onlineNodes')} onToggle={(o) => onSlotToggle('nodes', o)}>
        {() => (
          <NodeStatRows
            rows={nodeRows}
            remaining={remaining.nodes}
            onOpenNode={onOpenNode}
            onViewAll={() => onViewAll('nodes')}
          />
        )}
      </ClusterStatPopover>
      <ClusterStatPopover icon={Boxes} value={running} label={t('header.runningInstances')} onToggle={(o) => onSlotToggle('running', o)}>
        {(open) => (
          <RunningInstanceRows
            rows={runningRows}
            remaining={remaining.running}
            useInstancePlayers={useInstancePlayers}
            open={open}
            onOpenInstance={onOpenInstance}
            onViewAll={() => onViewAll('running')}
          />
        )}
      </ClusterStatPopover>
      <ClusterStatPopover
        icon={AlertTriangle}
        value={crashed}
        label={t('header.crashedInstances')}
        danger={crashed > 0}
        onToggle={(o) => onSlotToggle('crashed', o)}
      >
        {() => (
          <CrashedInstanceRows
            rows={crashedRows}
            remaining={remaining.crashed}
            onOpenInstance={onOpenInstance}
            onViewAll={() => onViewAll('crashed')}
          />
        )}
      </ClusterStatPopover>
    </div>
  )
}

/**
 * 集群概览徽标 + 缩略浮窗外壳（FR-294，复用 FR-216 铃铛的 DropdownMenu 范式、同款视觉）：
 * 徽标点击由「直接跳筛选页」改为弹缩略浮窗；受控 open 态经 render prop 传给内容，
 * 让浮窗数据查询 enabled 绑定 open——数据仅浮窗打开时拉取。danger 时计数着红。
 */
function ClusterStatPopover({
  icon: Icon,
  value,
  label,
  danger,
  onToggle,
  children,
}: {
  icon: typeof Server
  value: number
  label: string
  danger?: boolean
  /** 开合上报：外壳据此开关对应查询。 */
  onToggle: (open: boolean) => void
  /** 浮窗内容，接收当前 open 态用于绑定查询 enabled。 */
  children: (open: boolean) => ReactNode
}) {
  const [open, setOpen] = useState(false)
  return (
    <DropdownMenu
      open={open}
      onOpenChange={(v) => {
        setOpen(v)
        onToggle(v)
      }}
    >
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          title={`${label}: ${value}`}
          aria-label={`${label}: ${value}`}
          className="flex items-center gap-1 rounded-md px-1.5 py-1 text-xs text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
        >
          <Icon className={cn('size-3.5', danger && 'text-status-danger')} />
          <span className={cn('tabular-nums', danger && 'font-medium text-status-danger')}>{value}</span>
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-72">
        <div className="px-2 py-1.5 text-xs font-medium">{label}</div>
        <DropdownMenuSeparator />
        {children(open)}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/** 浮窗底部「查看全部」项（FR-294）：行数超上限时改为提示剩余数。 */
function StatPopoverFooter({ remaining, label, onClick }: { remaining: number; label: string; onClick: () => void }) {
  const { t } = useTranslation()
  return (
    <>
      <DropdownMenuSeparator />
      <DropdownMenuItem onClick={onClick} className="justify-center text-xs text-muted-foreground">
        {remaining > 0 ? t('header.moreCount', { count: remaining }) : label}
      </DropdownMenuItem>
    </>
  )
}

/**
 * 在线节点浮窗内容（FR-294）：行 = 节点名 + 在线状态点 + 该节点运行实例数。
 * 节点列表与每节点运行数由外壳取数（复用页眉已缓存的 ['nodes'] 与 FR-247 聚合，不额外发请求）；
 * 行点击 → /nodes?node=<id> 定位该节点（FR-128 深链）。
 */
function NodeStatRows({
  rows,
  remaining,
  onOpenNode,
  onViewAll,
}: {
  rows?: ClusterNodeRow[]
  remaining: number
  onOpenNode: (nodeId: number) => void
  onViewAll: () => void
}) {
  const { t } = useTranslation()
  const visible = (rows ?? []).slice(0, STAT_POPOVER_MAX_ROWS)

  return (
    <>
      {rows && visible.length === 0 ? (
        <div className="px-2 py-6 text-center text-xs text-muted-foreground">{t('header.noNodes')}</div>
      ) : (
        visible.map((node) => (
          <DropdownMenuItem key={node.id} onClick={() => onOpenNode(node.id)} className="text-xs">
            <span
              className={cn('size-1.5 shrink-0 rounded-full', node.status === 1 ? 'bg-status-success' : 'bg-muted-foreground/50')}
            />
            <span className="min-w-0 flex-1 truncate">{node.name}</span>
            <span className="shrink-0 tabular-nums text-muted-foreground">
              {t('header.runningOnNode', { count: node.runningCount })}
            </span>
          </DropdownMenuItem>
        ))
      )}
      <StatPopoverFooter remaining={remaining} label={t('header.viewAllNodes')} onClick={onViewAll} />
    </>
  )
}

/**
 * 运行中服务器浮窗内容（FR-294）：行 = 实例名 + 节点名 + 在线人数（有数据时）。
 * 行数据由外壳复用 FR-247 分页搜索（status=RUNNING，pageSize=行数上限）提供；
 * 行点击 → /instances/:id 该服控制台，底部「查看全部」→ /instances?status=RUNNING。
 */
function RunningInstanceRows({
  rows,
  remaining,
  useInstancePlayers,
  open,
  onOpenInstance,
  onViewAll,
}: {
  rows?: ClusterInstanceRow[]
  remaining: number
  useInstancePlayers?: ClusterBadgesProps['useInstancePlayers']
  open: boolean
  onOpenInstance: (instanceId: number) => void
  onViewAll: () => void
}) {
  const { t } = useTranslation()
  const items = rows ?? []

  return (
    <>
      {rows && items.length === 0 ? (
        <div className="px-2 py-6 text-center text-xs text-muted-foreground">{t('header.noRunningInstances')}</div>
      ) : (
        items.map((inst) => (
          <RunningInstanceRow
            key={inst.id}
            instance={inst}
            useInstancePlayers={useInstancePlayers}
            open={open}
            onOpen={() => onOpenInstance(inst.id)}
          />
        ))
      )}
      <StatPopoverFooter remaining={remaining} label={t('header.viewAll')} onClick={onViewAll} />
    </>
  )
}

/**
 * 运行中服务器浮窗单行（FR-294）：在线人数复用既有实例 metrics 查询（FR-060），
 * enabled 绑定浮窗 open 态（行仅在浮窗打开时挂载），无数据时不显示人数。
 */
function RunningInstanceRow({
  instance,
  useInstancePlayers,
  open,
  onOpen,
}: {
  instance: ClusterInstanceRow
  useInstancePlayers?: ClusterBadgesProps['useInstancePlayers']
  open: boolean
  onOpen: () => void
}) {
  const { t } = useTranslation()
  // 取数 hook 由外壳注入；未注入时不显示人数（等价于「无数据」）。
  // 注入与否在组件生命周期内不变（props 契约如此），故条件调用在运行时是稳定的；
  // 规则无法得知该前提，按例外显式豁免。
  // eslint-disable-next-line react-hooks/rules-of-hooks
  const players = useInstancePlayers ? useInstancePlayers(instance.id, open) : undefined
  return (
    <DropdownMenuItem onClick={onOpen} className="text-xs">
      <span className="size-1.5 shrink-0 rounded-full bg-status-success" />
      <span className="min-w-0 flex-1 truncate">{instance.name}</span>
      {instance.nodeName && <span className="shrink-0 text-muted-foreground">{instance.nodeName}</span>}
      {players && (
        <span className="flex shrink-0 items-center gap-1 tabular-nums text-muted-foreground">
          <Users className="size-3" />
          {players.available ? t('header.onlinePlayersCount', { count: players.online }) : t('metrics.unavailable')}
        </span>
      )}
    </DropdownMenuItem>
  )
}

/**
 * 崩溃服务器浮窗内容（FR-294）：行 = 实例名 + 节点名 + 崩溃原因（statusReason，有则显）；
 * 空态显示友好文案。行数据由外壳复用 FR-247 分页搜索（status=CRASHED）提供；
 * 行点击 → /instances/:id，底部「查看全部」→ /instances?status=CRASHED。
 */
function CrashedInstanceRows({
  rows,
  remaining,
  onOpenInstance,
  onViewAll,
}: {
  rows?: ClusterInstanceRow[]
  remaining: number
  onOpenInstance: (instanceId: number) => void
  onViewAll: () => void
}) {
  const { t } = useTranslation()
  const items = rows ?? []

  return (
    <>
      {rows && items.length === 0 ? (
        <div className="px-2 py-6 text-center text-xs text-muted-foreground">{t('header.noCrashedInstances')}</div>
      ) : (
        items.map((inst) => (
          <DropdownMenuItem key={inst.id} onClick={() => onOpenInstance(inst.id)} className="items-start text-xs">
            <span className="mt-1 size-1.5 shrink-0 rounded-full bg-status-danger" />
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-1.5">
                <span className="min-w-0 flex-1 truncate">{inst.name}</span>
                {inst.nodeName && (
                  <span className="shrink-0 text-muted-foreground">{inst.nodeName}</span>
                )}
              </div>
              {inst.statusReason && <p className="mt-0.5 truncate text-[11px] text-muted-foreground">{inst.statusReason}</p>}
            </div>
          </DropdownMenuItem>
        ))
      )}
      <StatPopoverFooter remaining={remaining} label={t('header.viewAll')} onClick={onViewAll} />
    </>
  )
}
