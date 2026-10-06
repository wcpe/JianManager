import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Bell,
  Boxes,
  Check,
  ChevronDown,
  ChevronRight,
  KeyRound,
  Languages,
  PanelLeftOpen,
  Scale,
  ShieldCheck,
  UsersRound,
  Wrench,
  type LucideIcon,
} from 'lucide-react'

import { cn } from '@jianmanager/ui'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@jianmanager/ui/components/dropdown-menu'
import { SidebarNavLink } from '@jianmanager/ui/components/views/console/SidebarNavLink'
import { ThemeSwitcher, type ThemeSwitcherProps } from '@jianmanager/ui/components/views/console/ThemeSwitcher'
import type { SidebarLinkArgs } from '@jianmanager/ui/components/views/console/sidebar-link'
import type { NavGroup, NavSection } from '@jianmanager/ui/lib/nav-config'

/** 分节小标题图标（仅视觉，折叠态不显）。 */
const SECTION_ICON: Record<string, LucideIcon> = {
  'nav.identityAccess': UsersRound,
  'nav.taskSchedule': Bell,
  'nav.storageRuntime': Boxes,
  'nav.contentTemplates': Wrench,
  'nav.auditSettings': ShieldCheck,
  'nav.agentAccess': KeyRound,
  'nav.systemMaintenance': Wrench,
  // 兼容旧 key（若测试/外部仍引用）
  'nav.contentDistribution': Wrench,
  'nav.taskNotification': Bell,
  'nav.platformManagement': Wrench,
  'nav.accountAudit': ShieldCheck,
  'nav.admin': Wrench,
}

/** 各子块共用的注入面。 */
interface SidebarInjected {
  pathname: string
  renderLink: (args: SidebarLinkArgs) => ReactNode
  collapsedGroups: Record<string, boolean | undefined>
  onToggleGroup: (key: string) => void
  renderServerSelector: () => ReactNode
  renderSidebarServerList: () => ReactNode
  appVersion: string
  themeSwitcher: Pick<ThemeSwitcherProps, 'colorTheme' | 'theme' | 'onColorThemeChange' | 'onThemeChange'>
  language: { current: string; onChange: (lng: string) => void }
}

export interface ConsoleSidebarProps extends SidebarInjected {
  /** 权限裁剪后的导航分组（应用侧按权限/角色计算）。 */
  groups: NavGroup[]
  /** 折叠态（应用侧 store 驱动）。 */
  collapsed: boolean
  onToggleSidebar: () => void
}

/**
 * 运维控制台左侧栏（FR-268 / ADR-055）：常驻资源主轴侧栏。
 * 定高 flex column；分组导航区占据剩余高度并整体滚动（滚动条隐藏，FR-131）。
 * 侧栏只放跨服务器 / 平台级入口；服务器选择交给全部服务器页、节点页与全局搜索，
 * 另在「选择服务器」下方常驻收藏 + 最近打开列（FR-293，仅展开态）。
 * 底部保留主题、语言、版本与开源许可。
 *
 * 数据与路由一律由 props 注入（ADR-097）：本视图不触达 api / store / 路由 / i18n 切换。
 */
export function ConsoleSidebar({
  groups,
  collapsed,
  onToggleSidebar,
  pathname,
  renderLink,
  collapsedGroups,
  onToggleGroup,
  renderServerSelector,
  renderSidebarServerList,
  appVersion,
  themeSwitcher,
  language,
}: ConsoleSidebarProps) {
  const injected: SidebarInjected = {
    pathname,
    renderLink,
    collapsedGroups,
    onToggleGroup,
    renderServerSelector,
    renderSidebarServerList,
    appVersion,
    themeSwitcher,
    language,
  }

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
        <SidebarContent active={!collapsed} compact={false} groups={groups} onToggleSidebar={onToggleSidebar} {...injected} />
        <SidebarContent active={collapsed} compact groups={groups} onToggleSidebar={onToggleSidebar} {...injected} />
      </div>
    </aside>
  )
}

function SidebarContent({
  active,
  compact,
  groups,
  onToggleSidebar,
  ...injected
}: {
  active: boolean
  compact: boolean
  groups: NavGroup[]
  onToggleSidebar: () => void
} & SidebarInjected) {
  const { t } = useTranslation()

  return (
    <div data-mode={compact ? 'collapsed' : 'expanded'} aria-hidden={!active} inert={!active ? true : undefined} className="jm-sidebar-mode">
      {!compact && (
        <div className="shrink-0 border-b bg-card/35 p-2">
          {/* 选择器与常驻服务器列是应用侧接线层（自带取数与路由），由外壳注入渲染。 */}
          {injected.renderServerSelector()}
          {/* 常驻服务器列（FR-293）：收藏置顶 + 最近打开；compact 图标轨不渲染本块。 */}
          {injected.renderSidebarServerList()}
        </div>
      )}

      {/* 滚动条隐藏但保留滚动（FR-131）：scrollbar-none 工具类见 index.css */}
      <nav className={cn('min-h-0 flex-1 space-y-1 overflow-y-auto scrollbar-none p-2', compact && 'px-1.5')}>
        {compact && (
          <button
            type="button"
            onClick={onToggleSidebar}
            aria-label={t('nav.expandSidebar')}
            title={t('nav.expandSidebar')}
            className="mb-1 grid w-full place-items-center rounded-md py-1.5 text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
          >
            <PanelLeftOpen className="size-4" />
          </button>
        )}
        {groups.map((g) =>
          compact ? (
            <CollapsedGroup key={g.key} group={g} {...injected} />
          ) : g.to ? (
            <LeafGroup key={g.key} group={g} {...injected} />
          ) : (
            <ExpandableGroup key={g.key} group={g} {...injected} />
          ),
        )}
      </nav>

      <SidebarFooter collapsed={compact} {...injected} />
    </div>
  )
}

/**
 * 折叠态：仅图标。leaf 直接导航；分类图标导航到该分类下第一个可见子页（FR-332，
 * groups 已按角色裁剪，childRoutes[0] 即权限过滤后的第一页）。hover tooltip 显 label。
 * 旧行为「点分类图标展开侧栏」废弃——真机上等同无反应，展开另有 logo 与展开按钮承担。
 */
function CollapsedGroup({ group, pathname, renderLink }: { group: NavGroup } & SidebarInjected) {
  const { t } = useTranslation()
  const Icon = group.icon
  const childRoutes = groupRoutes(group)
  const active = group.to
    ? pathname === group.to
    : childRoutes.some((r) => pathname === r || pathname.startsWith(r + '/'))
  const to = group.to ?? childRoutes[0]
  if (!to) return null

  const cls = cn(
    'grid w-full place-items-center rounded-md py-2 transition-colors',
    active ? 'bg-accent text-primary shadow-[inset_3px_0_0_var(--primary)]' : 'text-foreground/80 hover:bg-accent/60 hover:text-foreground',
  )

  return renderLink({
    to,
    ariaLabel: t(group.labelKey),
    title: t(group.labelKey),
    dataActive: active ? 'true' : 'false',
    className: cls,
    children: <Icon className="size-4" />,
  })
}

/** 单链接组（总览）。 */
function LeafGroup({ group, renderLink }: { group: NavGroup } & SidebarInjected) {
  return <SidebarNavLink to={group.to!} labelKey={group.labelKey} icon={group.icon} renderLink={renderLink} />
}

/** 收集一个分组下所有子路由（用于激活态判断）。 */
function groupRoutes(group: NavGroup): string[] {
  if (group.to) return [group.to]
  const fromChildren = group.children?.map((c) => c.to) ?? []
  const fromSections = group.sections?.flatMap((s) => s.children.map((c) => c.to)) ?? []
  return [...fromChildren, ...fromSections]
}

/** 可展开域（集群/观测/运营/系统）：头部可折叠；集群域额外内嵌节点切换 + 实例树；系统域分两小节。 */
function ExpandableGroup({
  group,
  pathname,
  renderLink,
  collapsedGroups,
  onToggleGroup,
}: { group: NavGroup } & SidebarInjected) {
  const { t } = useTranslation()
  const groupClosed = Boolean(collapsedGroups[group.key])
  const Icon = group.icon
  const hasActiveChild = groupRoutes(group).some((r) => pathname === r || pathname.startsWith(r + '/'))

  return (
    <div>
      <button
        type="button"
        onClick={() => onToggleGroup(group.key)}
        aria-expanded={!groupClosed}
        data-active={hasActiveChild ? 'true' : 'false'}
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
          {group.children?.map((c) => <SidebarNavLink key={c.to} {...c} nested renderLink={renderLink} />)}
          {group.sections?.map((sec) => <SidebarSection key={sec.labelKey} section={sec} renderLink={renderLink} />)}
        </div>
      </div>
    </div>
  )
}

/** 「系统」域的带标题二级分节（平台与维护 / 账户与审计）。 */
function SidebarSection({ section, renderLink }: { section: NavSection } & Pick<SidebarInjected, 'renderLink'>) {
  const { t } = useTranslation()
  const SecIcon = SECTION_ICON[section.labelKey]
  return (
    <div className="mt-1.5">
      <div className="flex items-center gap-1.5 px-2.5 py-1 text-[11px] font-medium uppercase tracking-wide text-muted-foreground/60">
        {SecIcon && <SecIcon className="size-3" />}
        <span className="truncate">{t(section.labelKey)}</span>
      </div>
      <div className="space-y-0.5">
        {section.children.map((c) => <SidebarNavLink key={c.to} {...c} nested renderLink={renderLink} />)}
      </div>
    </div>
  )
}

/**
 * 底部控件：全局主题切换器（FR-164，主题色圆点 + 明暗）+ 语言切换（FR-132，图标 + 语言名）；
 * 「版本号左下 · 开源许可入口右下」（FR-132；开源许可页 FR-135）。折叠态纵向紧凑、隐藏文字。
 */
function SidebarFooter({
  collapsed,
  appVersion,
  renderLink,
  themeSwitcher,
  language,
}: { collapsed: boolean } & SidebarInjected) {
  const { t } = useTranslation()

  return (
    <div className={cn('shrink-0 space-y-1.5 border-t bg-card/45 p-2 backdrop-blur-sm', collapsed && 'px-1.5')}>
      <div className={cn('flex items-center gap-2', collapsed && 'flex-col gap-1.5')}>
        <ThemeSwitcher compact={collapsed} {...themeSwitcher} />
        <LanguageSwitcher compact={collapsed} language={language} />
      </div>

      {!collapsed && (
        <div className="flex items-center justify-between gap-2 px-1">
          <span className="inline-flex items-center gap-1.5 text-[11px] text-muted-foreground/80">
            <span className="size-1.5 rounded-full bg-status-success" />
            v{appVersion}
          </span>
          {renderLink({
            to: '/licenses',
            className:
              'flex items-center gap-1 rounded text-[11px] text-muted-foreground/70 transition-colors hover:text-foreground hover:underline',
            children: (
              <>
                <Scale className="size-3 shrink-0" />
                {t('licenses.entry')}
              </>
            ),
          })}
        </div>
      )}
    </div>
  )
}

/** 语言切换（FR-132）：图标 + 语言名，dropdown 直选；切语言同步 `<html lang>`（由外壳的 i18n 负责）。折叠态仅图标。 */
function LanguageSwitcher({
  compact,
  language,
}: {
  compact: boolean
  language: { current: string; onChange: (lng: string) => void }
}) {
  const { t } = useTranslation()
  const currentLang = language.current === 'en' ? 'en' : 'zh'
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
          <DropdownMenuItem key={lng} onClick={() => language.onChange(lng)}>
            <span className="flex-1">{t(`language.${lng}`)}</span>
            {currentLang === lng && <Check className="size-3.5" />}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}