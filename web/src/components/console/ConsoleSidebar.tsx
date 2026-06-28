import { useTranslation } from 'react-i18next'
import { Link, useLocation } from 'react-router'
import {
  Activity,
  Archive,
  BarChart3,
  Bell,
  Bot,
  Box,
  Boxes,
  Check,
  ChevronDown,
  ChevronRight,
  Clapperboard,
  Clock,
  Database,
  DownloadCloud,
  FileClock,
  Gamepad2,
  HardDrive,
  Languages,
  Layers,
  LayoutDashboard,
  LayoutGrid,
  LayoutTemplate,
  ListChecks,
  Network,
  PanelLeftClose,
  PanelLeftOpen,
  RefreshCw,
  Scale,
  ScrollText,
  Server,
  Settings,
  Settings2,
  ShieldCheck,
  User,
  UsersRound,
  Wrench,
  type LucideIcon,
} from 'lucide-react'

import { useAuthStore } from '@/stores/auth'
import { useConsoleStore } from '@/stores/console'
import { changeLanguage } from '@/i18n'
import { cn } from '@/lib/utils'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import SidebarNavLink, { type NavEntry } from './SidebarNavLink'
import NodeSwitcher from './NodeSwitcher'
import InstanceTree from './InstanceTree'
import ThemeSwitcher from './ThemeSwitcher'
import { logoToggleLabelKey } from './sidebar-logo'

/**
 * 一个导航分区（leaf=单链接；children=可展开子项；instances 标记内嵌实例树/节点切换；
 * sections 标记带小标题的二级分节，用于「系统」域的「平台与维护 / 账户与审计」）。
 */
interface NavGroup {
  key: string
  labelKey: string
  icon: LucideIcon
  to?: string
  children?: NavEntry[]
  instances?: boolean
  sections?: NavSection[]
}

/** 「系统」域内的带标题二级分节（design §7）。 */
interface NavSection {
  labelKey: string
  children: NavEntry[]
}

/**
 * 五域导航信息架构（FR-131 / design §7；观测域 FR-215）：总览 / 集群 / 观测 / 运营 / 系统。
 * 从原 11 个粒度不一的一级精简为 5 个按运维心智分域的一级，高频域在上、低频「系统」沉底。
 * 能力不丢：实例树/节点切换并入「集群·实例」；监控总览/日志/统计归「观测」（任务中心迁「系统·平台与维护」，
 * 告警为 FR-216 接手前过渡留位）；模板/备份/定时归「运营」；平台资源/账户审计归「系统」两小节。
 */
const NAV_GROUPS: NavGroup[] = [
  { key: 'overview', labelKey: 'nav.dashboard', icon: LayoutDashboard, to: '/' },
  {
    key: 'cluster',
    labelKey: 'nav.cluster',
    icon: Boxes,
    instances: true,
    children: [
      { to: '/nodes', labelKey: 'nav.nodes', icon: Server },
      { to: '/instances', labelKey: 'nav.allInstances', icon: Box },
      { to: '/networks', labelKey: 'nav.networks', icon: Network },
      // 跨实例超级工作台（FR-167 / design §9）：集群域独立入口。
      { to: '/super', labelKey: 'nav.superWorkbench', icon: LayoutGrid },
      // 工作区导播台（FR-168 / design §9）：多场景预热瞬切，集群域独立入口。
      { to: '/director', labelKey: 'nav.director', icon: Clapperboard },
    ],
  },
  {
    // 「监控」域升级为「观测」域（FR-215）：下设 监控/日志/统计 三子类。
    // 任务中心已迁出至「系统·平台与维护」；告警在 FR-216 已并入「系统/账户与审计」的统一
    // 通知中心，故观测域不再有独立告警项（FR-215 过渡留位已由 FR-216 收口移除）。
    key: 'observability',
    labelKey: 'nav.observability',
    icon: Activity,
    children: [
      { to: '/monitor', labelKey: 'nav.monitoring', icon: Activity },
      // 客户端分发监控（FR-218）：消费 FR-217 观测底座出分发时序趋势 + 分布/榜单（总览 + 频道筛选）。
      // 平级路径 /client-dist-monitor（非 /monitor/* 嵌套）避免「监控总览」NavLink 前缀匹配误高亮。
      { to: '/client-dist-monitor', labelKey: 'nav.clientDistMonitor', icon: DownloadCloud },
      { to: '/logs', labelKey: 'nav.logs', icon: ScrollText },
      { to: '/statistics', labelKey: 'nav.statistics', icon: BarChart3 },
    ],
  },
  {
    key: 'operations',
    labelKey: 'nav.operations',
    icon: Gamepad2,
    children: [
      { to: '/players', labelKey: 'nav.players', icon: Gamepad2 },
      { to: '/bots', labelKey: 'nav.bots', icon: Bot },
      // 客户端分发（FR-187 由「系统·平台与维护」迁入运营域；路由 /client-channels 不变、旧链接可达）。
      { to: '/client-channels', labelKey: 'nav.clientChannels', icon: DownloadCloud },
      { to: '/templates', labelKey: 'nav.templates', icon: LayoutTemplate },
      { to: '/backups', labelKey: 'nav.backups', icon: Archive },
      { to: '/backup-storages', labelKey: 'nav.backupStorages', icon: Database },
      { to: '/schedules', labelKey: 'nav.schedules', icon: Clock },
    ],
  },
  {
    // 低频沉底（design §7）：内分「平台与维护」「账户与审计」两小节。
    key: 'system',
    labelKey: 'nav.system',
    icon: Settings,
    sections: [
      {
        labelKey: 'nav.sysPlatform',
        children: [
          { to: '/runtime-assets', labelKey: 'nav.runtimeAssets', icon: Layers },
          { to: '/storage', labelKey: 'nav.storage', icon: HardDrive },
          // 任务中心由「监控(原)」迁入（FR-215）：平台运维执行流水属维护类。
          { to: '/tasks', labelKey: 'nav.tasks', icon: ListChecks },
        ],
      },
      {
        labelKey: 'nav.sysAccount',
        children: [
          // 通知中心（FR-216）：站内信 + 告警合并的统一通知流入口（页眉铃铛主入口，此为侧栏入口）。
          { to: '/notifications', labelKey: 'nav.notifications', icon: Bell },
          { to: '/users', labelKey: 'nav.users', icon: User },
          { to: '/groups', labelKey: 'nav.groups', icon: UsersRound },
          { to: '/settings', labelKey: 'nav.systemSettings', icon: Settings2 },
          { to: '/audit', labelKey: 'nav.audit', icon: FileClock },
          { to: '/licenses', labelKey: 'licenses.entry', icon: Scale },
        ],
      },
    ],
  },
]

/** 平台管理员角色值（与后端 model.RolePlatformAdmin 对齐）。 */
const ROLE_PLATFORM_ADMIN = 10

/**
 * 按角色裁剪导航：平台管理员在「系统·平台与维护」小节追加「数据库」（FR-084）与「系统更新」（FR-081），
 * 均仅平台管理员可见。仅注入这两个入口，不改其余既有项。
 */
function navGroupsForRole(role: number | null): NavGroup[] {
  if (role !== ROLE_PLATFORM_ADMIN) return NAV_GROUPS
  return NAV_GROUPS.map((g) =>
    g.key === 'system' && g.sections
      ? {
          ...g,
          sections: g.sections.map((sec, i) =>
            i === 0
              ? {
                  ...sec,
                  children: [
                    ...sec.children,
                    { to: '/database', labelKey: 'nav.database', icon: Database },
                    { to: '/system-update', labelKey: 'nav.systemUpdate', icon: RefreshCw },
                  ],
                }
              : sec,
          ),
        }
      : g,
  )
}

/** 「系统」小节小标题图标（仅视觉，折叠态不显）。 */
const SECTION_ICON: Record<string, LucideIcon> = {
  'nav.sysPlatform': Wrench,
  'nav.sysAccount': ShieldCheck,
}

/**
 * 运维控制台左侧栏（ADR-009 / FR-037 / FR-131 / design §7）：常驻五域侧栏。
 * 定高 flex column；分组导航区占据剩余高度并整体滚动（滚动条隐藏，FR-131），
 * 「集群·实例」展开时内嵌节点切换 + 实例树；可折叠为仅图标轨（hover tooltip 显 label）。
 * 底部全局主题切换器（FR-164）+ 版本/开源许可固定可见。
 */
export default function ConsoleSidebar() {
  const { t } = useTranslation()
  const role = useAuthStore((s) => s.role)
  const groups = navGroupsForRole(role)
  const collapsed = useConsoleStore((s) => s.sidebarCollapsed)
  const toggleSidebar = useConsoleStore((s) => s.toggleSidebar)

  return (
    <aside className={cn('flex h-full min-h-0 flex-col border-r bg-card/40 transition-[width] duration-200 ease-ios', collapsed ? 'w-14' : 'w-60')}>
      <div className={cn('flex shrink-0 items-center border-b py-3', collapsed ? 'justify-center px-2' : 'gap-2 px-4')}>
        {/* logo 整体可点折叠/展开（FR-181，复用 toggleSidebar）：折叠态仅图标仍可点回展开。 */}
        <button
          type="button"
          onClick={toggleSidebar}
          aria-label={t(logoToggleLabelKey(collapsed))}
          title={t(logoToggleLabelKey(collapsed))}
          className={cn(
            'flex min-w-0 items-center rounded transition-colors hover:bg-accent/60',
            collapsed ? 'justify-center' : 'flex-1 gap-2 px-1 -mx-1',
          )}
        >
          <span className="grid size-6 shrink-0 place-items-center rounded bg-primary text-primary-foreground">
            <Boxes className="size-4" />
          </span>
          {!collapsed && <h2 className="min-w-0 flex-1 truncate text-left text-base font-bold tracking-tight">JianManager</h2>}
        </button>
        {!collapsed && (
          <button
            type="button"
            onClick={toggleSidebar}
            aria-label={t('nav.collapseSidebar')}
            title={t('nav.collapseSidebar')}
            className="grid size-6 shrink-0 place-items-center rounded text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
          >
            <PanelLeftClose className="size-4" />
          </button>
        )}
      </div>

      {/* 滚动条隐藏但保留滚动（FR-131）：scrollbar-none 工具类见 index.css */}
      <nav className={cn('min-h-0 flex-1 space-y-0.5 overflow-y-auto scrollbar-none p-2', collapsed && 'px-1.5')}>
        {collapsed && (
          <button
            type="button"
            onClick={toggleSidebar}
            aria-label={t('nav.expandSidebar')}
            title={t('nav.expandSidebar')}
            className="mb-1 grid w-full place-items-center rounded-md py-1.5 text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
          >
            <PanelLeftOpen className="size-4" />
          </button>
        )}
        {groups.map((g) =>
          collapsed ? (
            <CollapsedGroup key={g.key} group={g} />
          ) : g.to ? (
            <LeafGroup key={g.key} group={g} />
          ) : (
            <ExpandableGroup key={g.key} group={g} />
          ),
        )}
      </nav>

      <SidebarFooter collapsed={collapsed} />
    </aside>
  )
}

/** 折叠态：仅图标。leaf 直接导航；分组点击展开侧栏（再选子项）。hover tooltip 显 label。 */
function CollapsedGroup({ group }: { group: NavGroup }) {
  const { t } = useTranslation()
  const { pathname } = useLocation()
  const toggleSidebar = useConsoleStore((s) => s.toggleSidebar)
  const closeInstance = useConsoleStore((s) => s.closeInstance)
  const Icon = group.icon
  const childRoutes = groupRoutes(group)
  const active = group.to
    ? pathname === group.to
    : childRoutes.some((r) => pathname === r || pathname.startsWith(r + '/'))

  const cls = cn(
    'grid w-full place-items-center rounded-md py-2 transition-colors',
    active ? 'bg-primary/15 text-primary' : 'text-foreground/80 hover:bg-accent/60 hover:text-foreground',
  )

  if (group.to) {
    return (
      <Link to={group.to} onClick={() => closeInstance()} aria-label={t(group.labelKey)} title={t(group.labelKey)} className={cls}>
        <Icon className="size-4" />
      </Link>
    )
  }
  return (
    <button type="button" onClick={() => toggleSidebar()} aria-label={t(group.labelKey)} title={t(group.labelKey)} className={cls}>
      <Icon className="size-4" />
    </button>
  )
}

/** 单链接组（总览）。 */
function LeafGroup({ group }: { group: NavGroup }) {
  return <SidebarNavLink to={group.to!} labelKey={group.labelKey} icon={group.icon} />
}

/** 收集一个分组下所有子路由（用于激活态判断）。 */
function groupRoutes(group: NavGroup): string[] {
  if (group.to) return [group.to]
  const fromChildren = group.children?.map((c) => c.to) ?? []
  const fromSections = group.sections?.flatMap((s) => s.children.map((c) => c.to)) ?? []
  return [...fromChildren, ...fromSections]
}

/** 可展开域（集群/观测/运营/系统）：头部可折叠；集群域额外内嵌节点切换 + 实例树；系统域分两小节。 */
function ExpandableGroup({ group }: { group: NavGroup }) {
  const { t } = useTranslation()
  const { pathname } = useLocation()
  const collapsed = useConsoleStore((s) => s.collapsedGroups[group.key])
  const toggleGroup = useConsoleStore((s) => s.toggleGroup)
  const Icon = group.icon
  const hasActiveChild = groupRoutes(group).some((r) => pathname === r || pathname.startsWith(r + '/'))

  return (
    <div>
      <button
        type="button"
        onClick={() => toggleGroup(group.key)}
        aria-expanded={!collapsed}
        className={cn(
          'flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-[13px] transition-colors hover:bg-accent/60',
          hasActiveChild ? 'font-medium text-foreground' : 'text-foreground/80',
        )}
      >
        <Icon className="size-4 shrink-0" />
        <span className="flex-1 truncate text-left">{t(group.labelKey)}</span>
        {collapsed ? <ChevronRight className="size-3.5 opacity-60" /> : <ChevronDown className="size-3.5 opacity-60" />}
      </button>

      {!collapsed && (
        <div className="mt-0.5 space-y-0.5">
          {group.children?.map((c) => <SidebarNavLink key={c.to} {...c} nested />)}
          {group.instances && (
            <div className="mt-1 space-y-1 pl-2">
              <NodeSwitcher />
              <div className="max-h-[40vh] overflow-y-auto scrollbar-none">
                <InstanceTree />
              </div>
            </div>
          )}
          {group.sections?.map((sec) => <SidebarSection key={sec.labelKey} section={sec} />)}
        </div>
      )}
    </div>
  )
}

/** 「系统」域的带标题二级分节（平台与维护 / 账户与审计）。 */
function SidebarSection({ section }: { section: NavSection }) {
  const { t } = useTranslation()
  const SecIcon = SECTION_ICON[section.labelKey]
  return (
    <div className="mt-1.5">
      <div className="flex items-center gap-1.5 px-2.5 py-1 text-[11px] font-medium uppercase tracking-wide text-muted-foreground/60">
        {SecIcon && <SecIcon className="size-3" />}
        <span className="truncate">{t(section.labelKey)}</span>
      </div>
      <div className="space-y-0.5">
        {section.children.map((c) => <SidebarNavLink key={c.to} {...c} nested />)}
      </div>
    </div>
  )
}

/**
 * 底部控件：全局主题切换器（FR-164，主题色圆点 + 明暗）+ 语言切换（FR-132，图标 + 语言名）；
 * 「版本号左下 · 开源许可入口右下」（FR-132；开源许可页 FR-135）。折叠态纵向紧凑、隐藏文字。
 */
function SidebarFooter({ collapsed }: { collapsed: boolean }) {
  const { t } = useTranslation()
  const closeInstance = useConsoleStore((s) => s.closeInstance)

  return (
    <div className={cn('shrink-0 space-y-1.5 border-t p-2', collapsed && 'px-1.5')}>
      <div className={cn('flex items-center gap-2', collapsed && 'flex-col gap-1.5')}>
        <ThemeSwitcher compact={collapsed} />
        <LanguageSwitcher compact={collapsed} />
      </div>

      {!collapsed && (
        <div className="flex items-center justify-between gap-2 px-1">
          <span className="text-[11px] text-muted-foreground/70">v{__APP_VERSION__}</span>
          <Link
            to="/licenses"
            onClick={() => closeInstance()}
            className="flex items-center gap-1 rounded text-[11px] text-muted-foreground/70 transition-colors hover:text-foreground hover:underline"
          >
            <Scale className="size-3 shrink-0" />
            {t('licenses.entry')}
          </Link>
        </div>
      )}
    </div>
  )
}

/** 语言切换（FR-132）：图标 + 语言名，dropdown 直选；切语言同步 `<html lang>`（见 i18n）。折叠态仅图标。 */
function LanguageSwitcher({ compact }: { compact: boolean }) {
  const { t, i18n } = useTranslation()
  const currentLang = i18n.language === 'en' ? 'en' : 'zh'
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          aria-label={t(`language.${currentLang}`)}
          title={t(`language.${currentLang}`)}
          className={cn(
            'flex items-center gap-1.5 rounded-md px-2 py-1.5 text-[13px] text-foreground/80 transition-colors hover:bg-accent/60 hover:text-foreground',
            compact ? 'px-0 py-0' : 'ml-auto',
          )}
        >
          <Languages className="size-4 shrink-0" />
          {!compact && <span className="truncate">{t(`language.${currentLang}`)}</span>}
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent side="top" align={compact ? 'center' : 'end'} className="w-32">
        {(['zh', 'en'] as const).map((lng) => (
          <DropdownMenuItem key={lng} onClick={() => changeLanguage(lng)}>
            <span className="flex-1">{t(`language.${lng}`)}</span>
            {currentLang === lng && <Check className="size-3.5" />}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
