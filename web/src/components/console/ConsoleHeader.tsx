import { useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router'
import { AlertTriangle, Bell, Boxes, LogOut, Search, Server, UserRound } from 'lucide-react'

import { useAuthStore } from '@/stores/auth'
import { useConsoleStore } from '@/stores/console'
import { useInstances } from '@/api/instances'
import { useMetricOverview } from '@/api/metrics'
import { useAlertEvents, useUnreadAlertCount } from '@/api/alerts'
import { cn } from '@/lib/utils'
import { Input } from '@/components/ui/input'
import PageBreadcrumb from './PageBreadcrumb'
import NotificationInbox from '@/components/NotificationInbox'
import { searchBoxClass, slotVisibility, visibilityClass } from './header-layout'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'

/** 角色值 → i18n key（复用 users.* 角色文案，避免重复维护）。 */
const ROLE_LABEL_KEY: Record<number, string> = {
  0: 'users.member',
  1: 'users.groupAdmin',
  10: 'users.platformAdmin',
}

/**
 * 全局顶栏（FR-162，重排见 FR-179）：控制台外壳内容区上方常驻页眉，侧栏保持全高。
 * 左 = 当前页面包屑（自动占据剩余宽度）；右 = **靠右对齐的操作区**——
 * 常驻搜索框（占位，Ctrl/⌘+K 聚焦）+ 集群概览徽标 + 站内信收件箱挂载点（FR-183 预留）+
 * 告警铃铛 + 账户菜单（含退出登录，接管 FR-132）。
 * 搜索由 FR-162 的居中铺中部改为靠右紧贴操作图标（FR-179），窄屏隐藏不挤垮工作区。
 * 槽位顺序 / 响应式可见性逻辑下沉纯函数 `header-layout.ts`（vitest 覆盖）。
 */
export default function ConsoleHeader() {
  return (
    <header className="flex h-12 shrink-0 items-center gap-3 border-b bg-card/40 px-3 sm:px-4">
      <TitleArea />
      {/* 右侧操作区：ml-auto 推到右缘，搜索靠右紧贴操作图标（FR-179）。 */}
      <div className="ml-auto flex items-center gap-2 sm:gap-3">
        <SearchBox />
        <div className="flex items-center gap-0.5 sm:gap-1">
          <ClusterBadges />
          <InboxSlot />
          <AlertBell />
          <AccountMenu />
        </div>
      </div>
    </header>
  )
}

/**
 * 左侧面包屑（FR-134 + FR-162）：打开实例工作区时末级补实例名（域›实例›名称），
 * 否则按路由渲染「域 › 页面」轨迹。统一页头组件 `PageBreadcrumb` 承载。
 */
function TitleArea() {
  const openInstanceId = useConsoleStore((s) => s.openInstanceId)
  const { data: instances } = useInstances()
  const openInst = openInstanceId != null ? instances?.find((i) => i.id === openInstanceId) : undefined
  // min-w-0 让面包屑可截断，避免长轨迹把右侧操作区挤出页眉（窄屏防翻屏）。
  return (
    <div className="min-w-0 flex-1">
      <PageBreadcrumb leaf={openInst?.name} />
    </div>
  )
}

/**
 * 靠右常驻搜索框（FR-179 重排）：本期仅 UI + 聚焦快捷键（Ctrl/⌘+K），检索逻辑留后续 FR。
 * 由 FR-162 的居中铺满改为靠右固定上限宽度（`header-layout.searchBoxClass`），紧贴右侧操作图标；
 * 窄屏（<md）隐藏不挤垮工作区。
 */
function SearchBox() {
  const { t } = useTranslation()
  const ref = useRef<HTMLInputElement>(null)

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        ref.current?.focus()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  return (
    <div className={searchBoxClass()}>
      <Search className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
      <Input
        ref={ref}
        type="search"
        placeholder={t('header.searchPlaceholder')}
        aria-label={t('header.searchPlaceholder')}
        className="h-8 rounded-lg bg-muted/60 pl-8 pr-12 text-sm transition-colors focus-visible:bg-card"
      />
      <kbd className="pointer-events-none absolute right-2 top-1/2 hidden -translate-y-1/2 rounded border bg-muted px-1.5 py-0.5 text-[10px] font-medium text-muted-foreground xl:inline-block">
        Ctrl K
      </kbd>
    </div>
  )
}

/**
 * 站内信收件箱（FR-183）：位于集群徽标与告警铃铛之间，紧邻铃铛之前。
 * 挂载 FR-183 的收件箱组件（收件箱图标 + 未读计数 + 下拉，接站内信 API）。
 */
function InboxSlot() {
  return <NotificationInbox />
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
      className="flex items-center gap-1 rounded-lg px-1.5 py-1 text-xs text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
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
  // 复用总览页同款聚合（queryKey 共享缓存，不额外加压）。崩溃数按实例列表本地统计。
  const { data: overview } = useMetricOverview('24h')
  const { data: instances } = useInstances()
  const online = overview?.totals.onlineNodeCount ?? 0
  const running = overview?.totals.runningInstances ?? 0
  const crashed = instances?.filter((i) => i.status === 'CRASHED').length ?? 0

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

/** 告警级别 → 圆点配色类。 */
function levelDotClass(level: string): string {
  const l = level.toLowerCase()
  if (l === 'critical' || l === 'error' || l === 'danger') return 'bg-status-danger'
  if (l === 'warning' || l === 'warn') return 'bg-status-warning'
  return 'bg-status-info'
}

/** 告警铃铛（FR-162）：未读计数（30s 轮询）+ 下拉只读最近告警；接现有告警体系，不在此确认/处置。 */
function AlertBell() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { data: unread = 0 } = useUnreadAlertCount()
  const { data: events } = useAlertEvents()
  const recent = (events?.items ?? [])
    .slice()
    .sort((a, b) => (a.firedAt < b.firedAt ? 1 : -1))
    .slice(0, 6)

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          aria-label={t('header.alerts')}
          className="relative rounded-lg p-1.5 text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
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
          <span>{t('header.alerts')}</span>
          {unread > 0 && <span className="text-muted-foreground">{t('header.unreadCount', { count: unread })}</span>}
        </div>
        <DropdownMenuSeparator />
        {recent.length === 0 ? (
          <div className="px-2 py-6 text-center text-xs text-muted-foreground">{t('header.noAlerts')}</div>
        ) : (
          <div className="max-h-72 overflow-y-auto">
            {recent.map((e) => (
              <div key={e.id} className="flex items-start gap-2 px-2 py-1.5 text-xs">
                <span className={cn('mt-1 size-1.5 shrink-0 rounded-full', levelDotClass(e.level))} />
                <div className="min-w-0 flex-1">
                  <p className="truncate text-foreground">{e.message || e.rule?.name || `#${e.ruleId}`}</p>
                  <p className="text-[11px] text-muted-foreground">{new Date(e.firedAt).toLocaleString()}</p>
                </div>
                {!e.read && <span className="mt-1 size-1.5 shrink-0 rounded-full bg-primary" />}
              </div>
            ))}
          </div>
        )}
        <DropdownMenuSeparator />
        <DropdownMenuItem onClick={() => navigate('/alerts')}>{t('header.viewAllAlerts')}</DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
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
          className="flex items-center gap-1.5 rounded-lg px-1.5 py-1 text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
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
