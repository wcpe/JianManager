import { useTranslation } from 'react-i18next'
import { Link, useLocation } from 'react-router'
import {
  Activity,
  Archive,
  Bell,
  Bot,
  Box,
  Boxes,
  Check,
  ChevronDown,
  ChevronRight,
  Clock,
  Database,
  DownloadCloud,
  FileClock,
  Gamepad2,
  HardDrive,
  Languages,
  Layers,
  LayoutDashboard,
  LayoutTemplate,
  Monitor,
  Moon,
  Network,
  RefreshCw,
  Scale,
  ScrollText,
  Server,
  Settings,
  Settings2,
  Sun,
  User,
  UsersRound,
  type LucideIcon,
} from 'lucide-react'

import { useAuthStore } from '@/stores/auth'
import { useThemeStore } from '@/stores/theme'
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

/** 一个导航组：leaf=单链接；children=可展开子项；instances 标记内嵌实例树/节点切换。 */
interface NavGroup {
  key: string
  labelKey: string
  icon: LucideIcon
  to?: string
  children?: NavEntry[]
  instances?: boolean
}

/**
 * 多级侧栏信息架构（FR-061，整合原 FeatureNav/PlatformNav 三段为分组可展开侧栏）。
 * 能力不丢：实例树/节点切换并入「实例」组，用户/组/审计/设置并入「设置」组。
 */
const NAV_GROUPS: NavGroup[] = [
  { key: 'overview', labelKey: 'nav.dashboard', icon: LayoutDashboard, to: '/' },
  { key: 'nodes', labelKey: 'nav.nodes', icon: Server, to: '/nodes' },
  {
    key: 'instances',
    labelKey: 'nav.instances',
    icon: Boxes,
    instances: true,
    children: [
      { to: '/instances', labelKey: 'nav.allInstances', icon: Box },
      { to: '/networks', labelKey: 'nav.networks', icon: Network },
    ],
  },
  {
    key: 'monitor',
    labelKey: 'nav.monitor',
    icon: Activity,
    children: [
      { to: '/alerts', labelKey: 'nav.alerts', icon: Bell },
      { to: '/logs', labelKey: 'nav.logs', icon: ScrollText },
    ],
  },
  { key: 'players', labelKey: 'nav.players', icon: Gamepad2, to: '/players' },
  { key: 'bots', labelKey: 'nav.bots', icon: Bot, to: '/bots' },
  { key: 'schedules', labelKey: 'nav.schedules', icon: Clock, to: '/schedules' },
  {
    key: 'backup',
    labelKey: 'nav.backup',
    icon: Archive,
    children: [
      { to: '/backups', labelKey: 'nav.backups', icon: Archive },
      { to: '/backup-storages', labelKey: 'nav.backupStorages', icon: Database },
    ],
  },
  { key: 'templates', labelKey: 'nav.templates', icon: LayoutTemplate, to: '/templates' },
  {
    // 平台级资源/运维统一归此组（FR-112）：原 运行时与制品/客户端分发 为顶级叶子、
    // 平台存储/数据库/系统更新 埋在「设置」下，IA 不一致；现统一收入「平台」组。
    key: 'platform',
    labelKey: 'nav.platform',
    icon: Layers,
    children: [
      { to: '/runtime-assets', labelKey: 'nav.runtimeAssets', icon: Layers },
      { to: '/client-channels', labelKey: 'nav.clientChannels', icon: DownloadCloud },
      { to: '/storage', labelKey: 'nav.storage', icon: HardDrive },
    ],
  },
  {
    key: 'settings',
    labelKey: 'nav.settings',
    icon: Settings,
    children: [
      { to: '/users', labelKey: 'nav.users', icon: User },
      { to: '/groups', labelKey: 'nav.groups', icon: UsersRound },
      { to: '/audit', labelKey: 'nav.audit', icon: FileClock },
      { to: '/settings', labelKey: 'nav.systemSettings', icon: Settings2 },
    ],
  },
]

/** 平台管理员角色值（与后端 model.RolePlatformAdmin 对齐）。 */
const ROLE_PLATFORM_ADMIN = 10

/**
 * 按角色裁剪导航：平台管理员在「平台」组追加「数据库」（FR-084）与「系统更新」（FR-081）入口（FR-112 归类一致），
 * 均仅平台管理员可见。仅注入这两个入口，不改其余既有项。
 */
function navGroupsForRole(role: number | null): NavGroup[] {
  if (role !== ROLE_PLATFORM_ADMIN) return NAV_GROUPS
  return NAV_GROUPS.map((g) =>
    g.key === 'platform'
      ? {
          ...g,
          children: [
            ...(g.children ?? []),
            { to: '/database', labelKey: 'nav.database', icon: Database },
            { to: '/system-update', labelKey: 'nav.systemUpdate', icon: RefreshCw },
          ],
        }
      : g,
  )
}

/**
 * 运维控制台左侧栏（ADR-009 / FR-037 / FR-061）：常驻多级侧栏。
 * 定高 flex column；分组导航区占据剩余高度并整体滚动，「实例」组展开时内嵌节点切换 + 实例树；
 * 底部主题/语言/版本/开源许可固定可见（退出登录迁至全局顶栏账户菜单 FR-162）。
 */
export default function ConsoleSidebar() {
  const role = useAuthStore((s) => s.role)
  const groups = navGroupsForRole(role)
  return (
    <aside className="flex h-full min-h-0 w-60 flex-col border-r bg-card/40">
      <div className="flex shrink-0 items-center gap-2 border-b px-4 py-3">
        <span className="grid size-6 place-items-center rounded bg-primary text-primary-foreground">
          <Boxes className="size-4" />
        </span>
        <h2 className="text-base font-bold tracking-tight">JianManager</h2>
      </div>

      <nav className="min-h-0 flex-1 space-y-0.5 overflow-y-auto p-2">
        {groups.map((g) => (g.to ? <LeafGroup key={g.key} group={g} /> : <ExpandableGroup key={g.key} group={g} />))}
      </nav>

      <SidebarFooter />
    </aside>
  )
}

/** 单链接组（总览/节点/玩家/Bot/定时任务/模板）。 */
function LeafGroup({ group }: { group: NavGroup }) {
  return <SidebarNavLink to={group.to!} labelKey={group.labelKey} icon={group.icon} />
}

/** 可展开组（实例/监控/备份/设置）：头部可折叠，子项嵌套；实例组额外内嵌节点切换 + 实例树。 */
function ExpandableGroup({ group }: { group: NavGroup }) {
  const { t } = useTranslation()
  const { pathname } = useLocation()
  const collapsed = useConsoleStore((s) => s.collapsedGroups[group.key])
  const toggleGroup = useConsoleStore((s) => s.toggleGroup)
  const Icon = group.icon
  const hasActiveChild = (group.children ?? []).some((c) => pathname === c.to || pathname.startsWith(c.to + '/'))

  return (
    <div>
      <button
        type="button"
        onClick={() => toggleGroup(group.key)}
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
              <div className="max-h-[40vh] overflow-y-auto">
                <InstanceTree />
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  )
}

/** 主题三态选项：图标 + 文字，dropdown 内直选（非盲循环，FR-132）。 */
const THEME_OPTIONS: Array<{ value: 'light' | 'dark' | 'system'; icon: LucideIcon; labelKey: string }> = [
  { value: 'light', icon: Sun, labelKey: 'theme.light' },
  { value: 'dark', icon: Moon, labelKey: 'theme.dark' },
  { value: 'system', icon: Monitor, labelKey: 'theme.system' },
]

/**
 * 底部控件（FR-132）：主题 / 语言改 lucide 图标 + 文字（与导航项同款式，dropdown 三态直选）；
 * 切语言同步 `<html lang>`（见 `i18n.changeLanguage`）；底部「版本号左下 · 开源许可入口右下」。
 * 退出登录迁至全局顶栏账户菜单（FR-162），此处不再保留。
 */
function SidebarFooter() {
  const { t, i18n } = useTranslation()
  const { theme, setTheme } = useThemeStore()
  const closeInstance = useConsoleStore((s) => s.closeInstance)
  const currentLang = i18n.language === 'en' ? 'en' : 'zh'
  const ThemeIcon = theme === 'light' ? Sun : theme === 'dark' ? Moon : Monitor

  return (
    <div className="shrink-0 space-y-1.5 border-t p-2">
      <div className="flex items-center gap-1">
        {/* 主题：图标 + 文字，dropdown 三态直选 */}
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button
              type="button"
              className="flex flex-1 items-center gap-2 rounded-md px-2.5 py-1.5 text-[13px] text-foreground/80 transition-colors hover:bg-accent/60"
            >
              <ThemeIcon className="size-4 shrink-0" />
              <span className="flex-1 truncate text-left">{t(`theme.${theme}`)}</span>
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent side="top" align="start" className="w-44">
            {THEME_OPTIONS.map(({ value, icon: Icon, labelKey }) => (
              <DropdownMenuItem key={value} onClick={() => setTheme(value)}>
                <Icon className="size-4" />
                <span className="flex-1">{t(labelKey)}</span>
                {theme === value && <Check className="size-3.5" />}
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>

        {/* 语言：图标 + 语言名，dropdown 直选 */}
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button
              type="button"
              aria-label={t(`language.${currentLang}`)}
              className="flex items-center gap-1.5 rounded-md px-2 py-1.5 text-[13px] text-foreground/80 transition-colors hover:bg-accent/60"
            >
              <Languages className="size-4 shrink-0" />
              <span className="truncate">{t(`language.${currentLang}`)}</span>
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent side="top" align="end" className="w-32">
            {(['zh', 'en'] as const).map((lng) => (
              <DropdownMenuItem key={lng} onClick={() => changeLanguage(lng)}>
                <span className="flex-1">{t(`language.${lng}`)}</span>
                {currentLang === lng && <Check className="size-3.5" />}
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      {/* 版本号左下 · 开源许可入口右下（开源许可页见 FR-135） */}
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
    </div>
  )
}
