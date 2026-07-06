import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useLocation, useNavigate } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, Bell, Boxes, Check, ChevronDown, Loader2, LogOut, RotateCw, Search, Server, UserRound } from 'lucide-react'

import { useAuthStore } from '@/stores/auth'
import { useConsoleStore } from '@/stores/console'
import { useInstance, useInstanceAggregate } from '@/api/instances'
import { useNodes } from '@/api/nodes'
import { useMetricOverview } from '@/api/metrics'
import { useTasks } from '@/api/tasks'
import { useNotificationFeed, useFeedUnreadCount, type FeedItem } from '@/api/notification-feed'
import { cn } from '@jianmanager/ui'
import PageBreadcrumb from './PageBreadcrumb'
import { searchBoxClass, slotVisibility, visibilityClass } from './header-layout'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@jianmanager/ui/components/dropdown-menu'

/** 角色值 → i18n key（复用 users.* 角色文案，避免重复维护）。 */
const ROLE_LABEL_KEY: Record<number, string> = {
  0: 'users.member',
  1: 'users.groupAdmin',
  10: 'users.platformAdmin',
}

/**
 * 全局顶栏（FR-162，重排见 FR-179；通知合并见 FR-216）：控制台外壳内容区上方常驻页眉，侧栏保持全高。
 * 左 = 当前页面包屑（自动占据剩余宽度）；右 = **靠右对齐的操作区**——
 * 常驻搜索框（占位，Ctrl/⌘+K 聚焦）+ 集群概览徽标 + **统一通知铃铛**（FR-216：合并原站内信收件箱 +
 * 告警铃铛为单一入口，消费统一通知流）+ 账户菜单（含退出登录，接管 FR-132）。
 * 搜索由 FR-162 的居中铺中部改为靠右紧贴操作图标（FR-179），窄屏隐藏不挤垮工作区。
 * 槽位顺序 / 响应式可见性逻辑下沉纯函数 `header-layout.ts`（vitest 覆盖）。
 */
export default function ConsoleHeader() {
  return (
    <header data-slot="console-header" className="jm-console-header flex h-14 shrink-0 items-center gap-2 border-b px-3 text-[13px] backdrop-blur-xl sm:px-4">
      <NodeScopeSelector />
      <TitleArea />
      {/* 右侧操作区：搜索、集群状态、任务、通知与账户（FR-268）。 */}
      <div className="ml-auto flex items-center gap-2 sm:gap-3">
        <SearchBox />
        <div className="flex items-center gap-0.5 sm:gap-1">
          <RefreshButton />
          <ClusterBadges />
          <TaskProgress />
          <NotificationBell />
          <AccountMenu />
        </div>
      </div>
    </header>
  )
}

/** 节点作用域选择器（FR-268）：全部节点 / 单节点，影响用户上下文理解。 */
function NodeScopeSelector() {
  const { t } = useTranslation()
  const selectedNodeId = useConsoleStore((s) => s.selectedNodeId)
  const setSelectedNodeId = useConsoleStore((s) => s.setSelectedNodeId)
  const { data: nodes = [] } = useNodes({ refetchInterval: 30_000 })
  const selected = nodes.find((n) => n.id === selectedNodeId)
  const label = selected?.name ?? t('console.allNodes')

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          aria-label={t('header.nodeScope')}
          className="flex h-9 max-w-[180px] items-center gap-1.5 rounded-md border bg-card/90 px-2.5 text-xs text-foreground shadow-soft transition-colors hover:bg-muted/60"
        >
          <span className={cn('size-1.5 rounded-full', selected ? 'bg-status-success' : 'bg-primary')} />
          <span className="truncate">{label}</span>
          <ChevronDown className="size-3.5 text-muted-foreground" />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-56">
        <DropdownMenuItem onClick={() => setSelectedNodeId(null)}>
          <span className="flex-1">{t('console.allNodes')}</span>
          {selectedNodeId == null && <Check className="size-3.5" />}
        </DropdownMenuItem>
        {nodes.map((node) => (
          <DropdownMenuItem key={node.id} onClick={() => setSelectedNodeId(node.id)}>
            <span className={cn('size-1.5 rounded-full', node.status === 1 ? 'bg-status-success' : 'bg-muted-foreground/50')} />
            <span className="min-w-0 flex-1 truncate">{node.name}</span>
            {selectedNodeId === node.id && <Check className="size-3.5" />}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/**
 * 左侧面包屑（FR-134 + FR-162）：打开实例工作区时末级补实例名（域›实例›名称），
 * 否则按路由渲染「域 › 页面」轨迹。统一页头组件 `PageBreadcrumb` 承载。
 */
function TitleArea() {
  const location = useLocation()
  const routeInstanceId = Number(location.pathname.match(/^\/instances\/(\d+)/)?.[1] ?? 0)
  const activeInstanceId = routeInstanceId > 0 ? routeInstanceId : null
  const { data: openInst } = useInstance(activeInstanceId ?? 0)
  // min-w-0 让面包屑可截断，避免长轨迹把右侧操作区挤出页眉（窄屏防翻屏）。
  return (
    <div className="min-w-0 flex-1">
      <PageBreadcrumb leaf={openInst?.name} />
    </div>
  )
}

/**
 * 靠右常驻搜索入口（FR-179 重排 + FR-241）：点击或 Ctrl/⌘+K 打开全局命令面板（`CommandPalette`），
 * 检索实例/节点/页面/操作并跳转。本身不再是输入框，仅作开面板的按钮（Ctrl+K 由面板全局监听）。
 * 由 FR-162 的居中铺满改为靠右固定上限宽度（`header-layout.searchBoxClass`），紧贴右侧操作图标；
 * 窄屏（<md）隐藏不挤垮工作区。
 */
function SearchBox() {
  const { t } = useTranslation()
  const openPalette = useConsoleStore((s) => s.setCommandPaletteOpen)

  return (
    <div className={searchBoxClass()}>
      <button
        type="button"
        onClick={() => openPalette(true)}
        aria-label={t('header.searchPlaceholder')}
        className="flex h-9 w-full items-center gap-2 rounded-md border bg-card/90 pl-2.5 pr-2 text-sm text-muted-foreground shadow-soft transition-colors hover:bg-muted/55"
      >
        <Search className="size-4 shrink-0" />
        <span className="min-w-0 flex-1 truncate text-left">{t('header.searchPlaceholder')}</span>
        <kbd className="hidden shrink-0 rounded border bg-background px-1.5 py-0.5 text-[10px] font-medium xl:inline-block">
          Ctrl K
        </kbd>
      </button>
    </div>
  )
}

/**
 * 全局刷新（FR-232）：重拉当前页所有活跃查询（invalidateQueries），转动图标给反馈。
 * 解决「页面无刷新入口」——不整页 reload，仅失效并重取数据。
 */
function RefreshButton() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [spinning, setSpinning] = useState(false)
  const refresh = () => {
    setSpinning(true)
    void queryClient.invalidateQueries().finally(() => {
      setTimeout(() => setSpinning(false), 500)
    })
  }
  return (
    <button
      type="button"
      onClick={refresh}
      aria-label={t('header.refresh')}
      title={t('header.refresh')}
      className="rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
    >
      <RotateCw className={cn('size-4', spinning && 'animate-spin')} />
    </button>
  )
}

/** 单个集群概览徽标：图标 + 计数，可点跳转对应筛选。danger 时计数着红。 */
function ClusterBadge({
  icon: Icon,
  value,
  label,
  danger,
  onClick,
}: {
  icon: typeof Server
  value: number
  label: string
  danger?: boolean
  onClick: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      title={`${label}: ${value}`}
      aria-label={`${label}: ${value}`}
      className="flex items-center gap-1 rounded-md px-1.5 py-1 text-xs text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
    >
      <Icon className={cn('size-3.5', danger && 'text-status-danger')} />
      <span className={cn('tabular-nums', danger && 'font-medium text-status-danger')}>{value}</span>
    </button>
  )
}

/** 集群概览徽标组（FR-162）：在线节点 / 运行实例 / 崩溃数；点击跳转对应筛选。窄屏隐藏。 */
function ClusterBadges() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  // 复用总览页同款聚合；崩溃数走 FR-247 聚合，避免页眉为计数拉全量实例。
  const { data: overview } = useMetricOverview('24h')
  const { data: aggregate } = useInstanceAggregate()
  const online = overview?.totals.onlineNodeCount ?? 0
  const running = overview?.totals.runningInstances ?? 0
  const crashed = aggregate?.byStatus.CRASHED ?? 0

  return (
    <div className={cn('items-center', visibilityClass(slotVisibility('clusterBadges')))}>
      <ClusterBadge icon={Server} value={online} label={t('header.onlineNodes')} onClick={() => navigate('/nodes')} />
      <ClusterBadge
        icon={Boxes}
        value={running}
        label={t('header.runningInstances')}
        onClick={() => navigate('/instances?status=RUNNING')}
      />
      <ClusterBadge
        icon={AlertTriangle}
        value={crashed}
        label={t('header.crashedInstances')}
        danger={crashed > 0}
        onClick={() => navigate('/instances?status=CRASHED')}
      />
    </div>
  )
}

/**
 * 页眉任务进度（FR-226）：有在跑任务（pending/running）时显示数量 + 平均进度，点击进任务中心定位。
 * 无在跑任务时隐藏（任务中心入口在侧栏「系统」）；轮询复用 useTasks（有活跃任务时短轮询、空闲停）。
 */
function TaskProgress() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { data: tasks } = useTasks()
  const active = (tasks ?? []).filter((tk) => tk.state === 'running' || tk.state === 'pending')
  if (active.length === 0) return null
  const avg = Math.round(active.reduce((s, tk) => s + tk.progress, 0) / active.length)
  const label = t('header.tasksRunning', { count: active.length, progress: avg })
  return (
    <button
      type="button"
      onClick={() => navigate('/tasks')}
      title={label}
      aria-label={label}
      className="flex items-center gap-1.5 rounded-md px-1.5 py-1 text-xs text-primary transition-colors hover:bg-accent/60"
    >
      <Loader2 className="size-3.5 animate-spin" />
      <span className="font-medium tabular-nums">{active.length}</span>
      <span className="tabular-nums text-muted-foreground">{avg}%</span>
    </button>
  )
}

/** 统一通知级别 → 圆点配色类（站内信四档；告警三档已在后端就近映射到此）。 */
function feedLevelDotClass(level: string): string {
  if (level === 'error') return 'bg-status-danger'
  if (level === 'warning') return 'bg-status-warning'
  if (level === 'success') return 'bg-status-success'
  return 'bg-status-info'
}

/**
 * 统一通知铃铛（FR-216，见 ADR-048）：合并原「站内信收件箱」+「告警铃铛」为单一入口。
 * 未读计数（统一：本人站内信 + 全局告警，30s 轮询）+ 下拉只读最近通知（消息/告警混合，各带来源标识与级别色点）；
 * 点「查看全部」进通知中心页。处置（确认/认领）仍在告警页，本下拉只读预览。
 */
function NotificationBell() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { data: unread = 0 } = useFeedUnreadCount()
  const { data: feed } = useNotificationFeed({ pageSize: 8 })
  const recent = feed?.items ?? []

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          aria-label={t('header.notifications')}
          className="relative rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
        >
          <Bell className="size-4" />
          {unread > 0 && (
            <span className="absolute -right-0.5 -top-0.5 grid min-w-4 place-items-center rounded-full bg-status-danger px-1 text-[10px] font-semibold leading-4 text-white">
              {unread > 99 ? '99+' : unread}
            </span>
          )}
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-80">
        <div className="flex items-center justify-between px-2 py-1.5 text-xs font-medium">
          <span>{t('header.notifications')}</span>
          {unread > 0 && <span className="text-muted-foreground">{t('header.unreadCount', { count: unread })}</span>}
        </div>
        <DropdownMenuSeparator />
        {recent.length === 0 ? (
          <div className="px-2 py-6 text-center text-xs text-muted-foreground">{t('notificationCenter.empty')}</div>
        ) : (
          <div className="max-h-72 overflow-y-auto">
            {recent.map((it) => (
              <NotificationPreviewRow key={`${it.source}-${it.id}`} item={it} />
            ))}
          </div>
        )}
        <DropdownMenuSeparator />
        <DropdownMenuItem onClick={() => navigate('/notifications')}>
          {t('header.viewAllNotifications')}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/** 下拉内单条通知预览：级别色点 + 来源徽标 + 标题/正文 + 时间 + 未读点。 */
function NotificationPreviewRow({ item }: { item: FeedItem }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const sourceLabel = item.source === 'alert' ? t('notificationCenter.badgeAlert') : t('notificationCenter.badgeMessage')
  // 快捷跳转（FR-226）：任务类站内信→任务中心定位该任务；告警→告警页；其余→通知中心。
  const jump = () => {
    if (item.source === 'message' && item.taskId) navigate(`/tasks?task=${item.taskId}`)
    else if (item.source === 'alert') navigate('/alerts')
    else navigate('/notifications')
  }
  return (
    <button type="button" onClick={jump} className="flex w-full items-start gap-2 px-2 py-1.5 text-left text-xs transition-colors hover:bg-accent/60">
      <span className={cn('mt-1 size-1.5 shrink-0 rounded-full', feedLevelDotClass(item.level))} />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-1.5">
          <span
            className={cn(
              'shrink-0 rounded px-1 py-px text-[10px] font-medium',
              item.source === 'alert'
                ? 'bg-status-warning/15 text-status-warning'
                : 'bg-primary/10 text-primary',
            )}
          >
            {sourceLabel}
          </span>
          <p className="truncate text-foreground">{item.title}</p>
        </div>
        {item.body && <p className="mt-0.5 truncate text-[11px] text-muted-foreground">{item.body}</p>}
        <p className="text-[11px] text-muted-foreground">{new Date(item.createdAt).toLocaleString()}</p>
      </div>
      {!item.read && <span className="mt-1 size-1.5 shrink-0 rounded-full bg-primary" />}
    </button>
  )
}

/** 账户菜单（FR-162）：显示用户名 / 角色 + 退出登录（接管 FR-132 的退出图标化）。 */
function AccountMenu() {
  const { t } = useTranslation()
  const username = useAuthStore((s) => s.username)
  const role = useAuthStore((s) => s.role)
  const logout = useAuthStore((s) => s.logout)
  const roleKey = role != null ? ROLE_LABEL_KEY[role] : undefined

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          aria-label={t('header.account')}
          className="flex items-center gap-1.5 rounded-md px-1.5 py-1 text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
        >
          <span className="grid size-6 shrink-0 place-items-center rounded-full bg-primary/15 text-primary">
            <UserRound className="size-3.5" />
          </span>
          <span className="hidden max-w-32 truncate text-xs font-medium text-foreground sm:block">
            {username ?? t('header.account')}
          </span>
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-56">
        <div className="px-2 py-1.5">
          <p className="truncate text-sm font-medium">{username ?? '—'}</p>
          {roleKey && <p className="text-xs text-muted-foreground">{t(roleKey)}</p>}
        </div>
        <DropdownMenuSeparator />
        <DropdownMenuItem variant="destructive" onClick={logout}>
          <LogOut className="size-4" />
          {t('common.logout')}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
