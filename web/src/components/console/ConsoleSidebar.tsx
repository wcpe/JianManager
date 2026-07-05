import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useLocation } from 'react-router'
import {
  Boxes,
  Check,
  ChevronDown,
  ChevronRight,
  Languages,
  PanelLeftClose,
  PanelLeftOpen,
  Scale,
  ShieldCheck,
  Wrench,
  type LucideIcon,
} from 'lucide-react'

import { useAuthStore } from '@/stores/auth'
import { useConsoleStore } from '@/stores/console'
import { changeLanguage } from '@/i18n'
import { cn } from '@jianmanager/ui'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@jianmanager/ui/components/dropdown-menu'
import ServerSelector from './ServerSelector'
import SidebarNavLink from './SidebarNavLink'
import ThemeSwitcher from './ThemeSwitcher'
import { logoToggleLabelKey } from './sidebar-logo'
import { navGroupsForRole, type NavGroup, type NavSection } from './nav-config'

/** 分节小标题图标（仅视觉，折叠态不显）。 */
const SECTION_ICON: Record<string, LucideIcon> = {
  'nav.contentDistribution': Wrench,
  'nav.storageRuntime': Boxes,
  'nav.taskNotification': ShieldCheck,
  'nav.accountAudit': ShieldCheck,
  'nav.admin': Wrench,
}
const SIDEBAR_CONTENT_SWAP_MS = 320

/**
 * 运维控制台左侧栏（FR-268 / ADR-055）：常驻资源主轴侧栏。
 * 定高 flex column；分组导航区占据剩余高度并整体滚动（滚动条隐藏，FR-131）。
 * 侧栏只放跨服务器 / 平台级入口；服务器选择交给全部服务器页、节点页与全局搜索。
 * 底部保留主题、语言、版本与开源许可。
 */
export default function ConsoleSidebar() {
  const role = useAuthStore((s) => s.role)
  const groups = useMemo(() => navGroupsForRole(role), [role])
  const collapsed = useConsoleStore((s) => s.sidebarCollapsed)
  const toggleSidebar = useConsoleStore((s) => s.toggleSidebar)
  const [renderCollapsed, setRenderCollapsed] = useState(collapsed)

  useEffect(() => {
    const delay = collapsed ? SIDEBAR_CONTENT_SWAP_MS : 0
    const timer = window.setTimeout(() => setRenderCollapsed(collapsed), delay)
    return () => window.clearTimeout(timer)
  }, [collapsed])

  return (
    <aside
      data-slot="console-sidebar"
      data-state={collapsed ? 'collapsed' : 'expanded'}
      className="jm-console-sidebar hidden h-full min-h-0 shrink-0 sm:block"
    >
      <div
        data-slot="sidebar-drawer"
        data-state={collapsed ? 'collapsed' : 'expanded'}
        className="jm-sidebar-drawer flex h-full min-h-0 flex-col"
      >
        <SidebarContent
          active={!renderCollapsed}
          collapsed={collapsed}
          compact={false}
          groups={groups}
          toggleSidebar={toggleSidebar}
        />
        <SidebarContent
          active={renderCollapsed}
          collapsed={collapsed}
          compact
          groups={groups}
          toggleSidebar={toggleSidebar}
        />
      </div>
    </aside>
  )
}

function SidebarContent({
  active,
  collapsed,
  compact,
  groups,
  toggleSidebar,
}: {
  active: boolean
  collapsed: boolean
  compact: boolean
  groups: NavGroup[]
  toggleSidebar: () => void
}) {
  const { t } = useTranslation()

  return (
    <div data-mode={compact ? 'collapsed' : 'expanded'} aria-hidden={!active} inert={!active ? true : undefined} className="jm-sidebar-mode">
      <div className={cn('flex shrink-0 items-center border-b py-3.5', compact ? 'justify-center px-2' : 'gap-2 px-3.5')}>
        {/* logo 整体可点折叠/展开（FR-181，复用 toggleSidebar）：折叠态仅图标仍可点回展开。 */}
        <button
          type="button"
          onClick={toggleSidebar}
          aria-label={t(logoToggleLabelKey(collapsed))}
          title={t(logoToggleLabelKey(collapsed))}
          className={cn(
            'flex min-w-0 items-center rounded-md transition-colors hover:bg-accent/60',
            compact ? 'justify-center' : 'flex-1 gap-2 px-1.5 py-1 -mx-1.5',
          )}
        >
          <span className="grid size-8 shrink-0 place-items-center rounded-md border border-primary/15 bg-card shadow-soft">
            <img src="/brand/jianmanager-mark.svg" alt="" aria-hidden="true" className="size-7" />
          </span>
          {!compact && <h2 className="min-w-0 flex-1 truncate text-left text-base font-bold tracking-tight text-foreground">JianManager</h2>}
        </button>
        {!compact && (
          <button
            type="button"
            onClick={toggleSidebar}
            aria-label={t('nav.collapseSidebar')}
            title={t('nav.collapseSidebar')}
            className="grid size-7 shrink-0 place-items-center rounded-md text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
          >
            <PanelLeftClose className="size-4" />
          </button>
        )}
      </div>

      {!compact && (
        <div className="shrink-0 border-b bg-card/35 p-2">
          <ServerSelector />
        </div>
      )}

      {/* 滚动条隐藏但保留滚动（FR-131）：scrollbar-none 工具类见 index.css */}
      <nav className={cn('min-h-0 flex-1 space-y-1 overflow-y-auto scrollbar-none p-2', compact && 'px-1.5')}>
        {compact && (
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
          compact ? (
            <CollapsedGroup key={g.key} group={g} />
          ) : g.to ? (
            <LeafGroup key={g.key} group={g} />
          ) : (
            <ExpandableGroup key={g.key} group={g} />
          ),
        )}
      </nav>

      <SidebarFooter collapsed={compact} />
    </div>
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
    active ? 'bg-accent text-primary shadow-[inset_3px_0_0_var(--primary)]' : 'text-foreground/80 hover:bg-accent/60 hover:text-foreground',
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
  const groupClosed = Boolean(collapsed)
  const hasActiveChild = groupRoutes(group).some((r) => pathname === r || pathname.startsWith(r + '/'))

  return (
    <div>
      <button
        type="button"
        onClick={() => toggleGroup(group.key)}
        aria-expanded={!groupClosed}
        className={cn(
          'flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-[13px] transition-colors hover:bg-accent/60',
          hasActiveChild ? 'bg-card/70 font-semibold text-foreground shadow-soft' : 'text-foreground/80',
        )}
      >
        <Icon className="size-4 shrink-0" />
        <span className="flex-1 truncate text-left">{t(group.labelKey)}</span>
        {groupClosed ? <ChevronRight className="size-3.5 opacity-60" /> : <ChevronDown className="size-3.5 opacity-60" />}
      </button>

      <div
        data-slot="sidebar-nav-group-content"
        data-state={groupClosed ? 'closed' : 'open'}
        aria-hidden={groupClosed}
        className="jm-sidebar-group-content mt-0.5"
      >
        <div className="jm-sidebar-group-content-inner space-y-0.5">
          {group.children?.map((c) => <SidebarNavLink key={c.to} {...c} nested />)}
          {group.sections?.map((sec) => <SidebarSection key={sec.labelKey} section={sec} />)}
        </div>
      </div>
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
    <div className={cn('shrink-0 space-y-1.5 border-t bg-card/45 p-2 backdrop-blur-sm', collapsed && 'px-1.5')}>
      <div className={cn('flex items-center gap-2', collapsed && 'flex-col gap-1.5')}>
        <ThemeSwitcher compact={collapsed} />
        <LanguageSwitcher compact={collapsed} />
      </div>

      {!collapsed && (
        <div className="flex items-center justify-between gap-2 px-1">
          <span className="inline-flex items-center gap-1.5 text-[11px] text-muted-foreground/80">
            <span className="size-1.5 rounded-full bg-status-success" />
            v{__APP_VERSION__}
          </span>
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
