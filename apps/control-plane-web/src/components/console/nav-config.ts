import {
  Activity,
  Archive,
  BarChart3,
  Bell,
  Bot,
  Box,
  Clapperboard,
  CloudUpload,
  Database,
  DownloadCloud,
  FileClock,
  GitBranch,
  HardDrive,
  Layers,
  LayoutDashboard,
  LayoutGrid,
  LayoutTemplate,
  ListChecks,
  Network,
  RefreshCw,
  Scale,
  ScrollText,
  Server,
  Settings,
  Settings2,
  ShieldCheck,
  User,
  UsersRound,
  type LucideIcon,
} from 'lucide-react'

import { type NavEntry } from './SidebarNavLink'

/**
 * 一个导航分区（leaf=单链接；children=可展开子项；sections=带小标题的二级分节）。
 */
export interface NavGroup {
  key: string
  labelKey: string
  icon: LucideIcon
  to?: string
  children?: NavEntry[]
  sections?: NavSection[]
}

/** 带标题的二级分节，用于「平台管理」域。 */
export interface NavSection {
  labelKey: string
  children: NavEntry[]
}

/**
 * 高密度控制台导航 IA（FR-268 / ADR-055）：平台首页 / 服务器 / 群组网络 / 观测 / 平台管理。
 * 侧栏只放跨服务器或平台级入口；单服操作仍统一收进服务器控制台。
 */
export const NAV_GROUPS: NavGroup[] = [
  { key: 'platformHome', labelKey: 'nav.platformHome', icon: LayoutDashboard, to: '/' },
  {
    key: 'servers',
    labelKey: 'nav.servers',
    icon: Server,
    children: [
      { to: '/instances', labelKey: 'nav.allInstances', icon: Box },
      { to: '/players', labelKey: 'nav.players', icon: User },
      { to: '/bots', labelKey: 'nav.bots', icon: Bot },
      { to: '/nodes', labelKey: 'nav.nodes', icon: Server },
      { to: '/super', labelKey: 'nav.superWorkbench', icon: LayoutGrid },
      { to: '/director', labelKey: 'nav.director', icon: Clapperboard },
    ],
  },
  {
    key: 'groupNetwork',
    labelKey: 'nav.groupNetwork',
    icon: Network,
    children: [
      { to: '/networks/topology', labelKey: 'nav.networkTopology', icon: GitBranch },
      { to: '/networks', labelKey: 'nav.groupManagement', icon: Network },
    ],
  },
  {
    key: 'observability',
    labelKey: 'nav.observability',
    icon: Activity,
    children: [
      { to: '/monitor', labelKey: 'nav.monitoring', icon: Activity },
      { to: '/logs', labelKey: 'nav.logs', icon: ScrollText },
      { to: '/statistics', labelKey: 'nav.statistics', icon: BarChart3 },
      { to: '/client-dist-monitor', labelKey: 'nav.clientDistMonitor', icon: DownloadCloud },
    ],
  },
  {
    key: 'platformManagement',
    labelKey: 'nav.platformManagement',
    icon: Settings,
    sections: [
      {
        labelKey: 'nav.contentDistribution',
        children: [
          { to: '/templates', labelKey: 'nav.templates', icon: LayoutTemplate },
          { to: '/client-channels', labelKey: 'nav.clientChannels', icon: DownloadCloud },
          { to: '/client-dist-security', labelKey: 'nav.clientDistSecurity', icon: ShieldCheck },
        ],
      },
      {
        labelKey: 'nav.storageRuntime',
        children: [
          { to: '/runtime-assets', labelKey: 'nav.runtimeAssets', icon: Layers },
          { to: '/storage', labelKey: 'nav.storage', icon: HardDrive },
          { to: '/artifact-storages', labelKey: 'nav.artifactStorages', icon: CloudUpload },
          { to: '/backup-storages', labelKey: 'nav.backupStorages', icon: Archive },
          { to: '/backups', labelKey: 'nav.backups', icon: Archive },
        ],
      },
      {
        labelKey: 'nav.taskNotification',
        children: [
          { to: '/tasks', labelKey: 'nav.tasks', icon: ListChecks },
          { to: '/schedules', labelKey: 'nav.schedules', icon: FileClock },
          { to: '/notifications', labelKey: 'nav.notifications', icon: Bell },
        ],
      },
      {
        labelKey: 'nav.accountAudit',
        children: [
          { to: '/users', labelKey: 'nav.users', icon: User },
          { to: '/groups', labelKey: 'nav.groups', icon: UsersRound },
          { to: '/audit', labelKey: 'nav.audit', icon: FileClock },
          { to: '/settings', labelKey: 'nav.systemSettings', icon: Settings2 },
          { to: '/licenses', labelKey: 'licenses.entry', icon: Scale },
        ],
      },
    ],
  },
]

/** 平台管理员角色值（与后端 model.RolePlatformAdmin 对齐）。 */
const ROLE_PLATFORM_ADMIN = 10

/**
 * 按角色裁剪导航：平台管理员在「平台管理」域追加「管理员」小节。
 */
export function navGroupsForRole(role: number | null): NavGroup[] {
  if (role !== ROLE_PLATFORM_ADMIN) return NAV_GROUPS
  return NAV_GROUPS.map((g) =>
    g.key === 'platformManagement' && g.sections
      ? {
          ...g,
          sections: [
            ...g.sections,
            {
              labelKey: 'nav.admin',
              children: [
                { to: '/database', labelKey: 'nav.database', icon: Database },
                { to: '/system-update', labelKey: 'nav.systemUpdate', icon: RefreshCw },
              ],
            },
          ],
        }
      : g,
  )
}

/**
 * 扁平化导航为 `{to, labelKey}` 列表（含按角色裁剪后的全部一级/子项/分节项）。
 * 供命令面板（FR-241）检索「页面」类目标，复用唯一 IA 真源，避免另维护一份路由表。
 */
export function flatNavItems(role: number | null): { to: string; labelKey: string }[] {
  const out: { to: string; labelKey: string }[] = []
  for (const g of navGroupsForRole(role)) {
    if (g.to) out.push({ to: g.to, labelKey: g.labelKey })
    for (const c of g.children ?? []) out.push({ to: c.to, labelKey: c.labelKey })
    for (const s of g.sections ?? []) for (const c of s.children) out.push({ to: c.to, labelKey: c.labelKey })
  }
  return out
}
