import {
  Activity,
  Archive,
  BarChart3,
  Bell,
  Bot,
  Box,
  Cable,
  Clapperboard,
  CloudUpload,
  Database,
  DownloadCloud,
  FileClock,
  GitBranch,
  GitCompareArrows,
  HardDrive,
  KeyRound,
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
  Shield,
  User,
  UsersRound,
  type LucideIcon,
} from 'lucide-react'

import { type NavEntry } from './SidebarNavLink'
import { DEFAULT_ROLE_NODES, isPlatformAdmin } from '@/lib/roles'

/**
 * 一个导航分区（leaf=单链接；children=可展开子项；sections=带小标题的二级分节）。
 * `perm` 为权限节点，数组表示 any-of（FR-431/432）。
 */
export interface NavGroup {
  key: string
  labelKey: string
  icon: LucideIcon
  to?: string
  perm?: string | string[]
  children?: NavEntry[]
  sections?: NavSection[]
}

/** 带标题的二级分节，用于「平台设置」域。 */
export interface NavSection {
  labelKey: string
  children: NavEntry[]
}

/**
 * 高密度控制台六域导航 IA（FR-431）：
 * 平台首页 / 服务器 / 群组网络 / 工作台 / 观测 / 客户端分发 / 平台设置（分节）。
 * 侧栏可见性与 API 共用权限节点；`/alerts` 维持无导航入口；`/templates` 归平台设置。
 */
export const NAV_GROUPS: NavGroup[] = [
  { key: 'platformHome', labelKey: 'nav.platformHome', icon: LayoutDashboard, to: '/' },
  {
    key: 'servers',
    labelKey: 'nav.servers',
    icon: Server,
    children: [
      { to: '/instances', labelKey: 'nav.allInstances', icon: Box, perm: 'instance.read' },
      { to: '/config-baselines', labelKey: 'nav.configBaselines', icon: GitCompareArrows, perm: ['file.read', 'instance.read'] },
      { to: '/nodes', labelKey: 'nav.nodes', icon: Server, perm: 'node.read' },
      { to: '/players', labelKey: 'nav.players', icon: User, perm: 'player.read' },
      { to: '/bots', labelKey: 'nav.bots', icon: Bot, perm: 'bot.read' },
    ],
  },
  {
    key: 'groupNetwork',
    labelKey: 'nav.groupNetwork',
    icon: Network,
    children: [
      { to: '/networks/topology', labelKey: 'nav.networkTopology', icon: GitBranch, perm: 'network.read' },
      { to: '/networks', labelKey: 'nav.groupManagement', icon: Network, perm: 'network.read' },
    ],
  },
  {
    key: 'workbench',
    labelKey: 'nav.workbench',
    icon: LayoutGrid,
    children: [
      { to: '/super', labelKey: 'nav.superWorkbench', icon: LayoutGrid, perm: 'super.read' },
      { to: '/director', labelKey: 'nav.director', icon: Clapperboard, perm: 'director.read' },
    ],
  },
  {
    key: 'observability',
    labelKey: 'nav.observability',
    icon: Activity,
    children: [
      { to: '/monitor', labelKey: 'nav.monitoring', icon: Activity, perm: 'monitor.read' },
      { to: '/logs', labelKey: 'nav.logs', icon: ScrollText, perm: 'log.read' },
      { to: '/statistics', labelKey: 'nav.statistics', icon: BarChart3, perm: 'stats.read' },
      { to: '/notifications', labelKey: 'nav.notifications', icon: Bell, perm: 'notification.read' },
    ],
  },
  {
    key: 'clientDistribution',
    labelKey: 'nav.clientDistribution',
    icon: DownloadCloud,
    children: [
      { to: '/client-channels', labelKey: 'nav.clientChannels', icon: DownloadCloud, perm: 'channel.read' },
      { to: '/client-dist-ops', labelKey: 'nav.clientDistOps', icon: ShieldCheck, perm: 'dist.ops.read' },
    ],
  },
  {
    key: 'platformSettings',
    labelKey: 'nav.platformSettings',
    icon: Settings,
    sections: [
      {
        labelKey: 'nav.identityAccess',
        children: [
          { to: '/users', labelKey: 'nav.users', icon: User, perm: 'user.read' },
          { to: '/groups', labelKey: 'nav.groups', icon: UsersRound, perm: 'group.read' },
          { to: '/permissions', labelKey: 'nav.permissions', icon: Shield, perm: 'rbac.read' },
        ],
      },
      {
        labelKey: 'nav.taskSchedule',
        children: [
          { to: '/tasks', labelKey: 'nav.tasks', icon: ListChecks, perm: 'task.read' },
          { to: '/schedules', labelKey: 'nav.schedules', icon: FileClock, perm: 'schedule.read' },
        ],
      },
      {
        labelKey: 'nav.storageRuntime',
        children: [
          { to: '/runtime-assets', labelKey: 'nav.runtimeAssets', icon: Layers, perm: 'node.read' },
          { to: '/storage', labelKey: 'nav.storage', icon: HardDrive, perm: 'node.manage' },
          { to: '/artifact-storages', labelKey: 'nav.artifactStorages', icon: CloudUpload, perm: 'node.manage' },
          { to: '/backup-storages', labelKey: 'nav.backupStorages', icon: Archive, perm: 'backup.read' },
          { to: '/backups', labelKey: 'nav.backups', icon: Archive, perm: 'backup.read' },
        ],
      },
      {
        labelKey: 'nav.contentTemplates',
        children: [
          { to: '/templates', labelKey: 'nav.templates', icon: LayoutTemplate, perm: 'template.read' },
        ],
      },
      {
        labelKey: 'nav.auditSettings',
        children: [
          { to: '/audit', labelKey: 'nav.audit', icon: FileClock, perm: 'audit.read' },
          { to: '/settings', labelKey: 'nav.systemSettings', icon: Settings2, perm: 'settings.read' },
          { to: '/licenses', labelKey: 'licenses.entry', icon: Scale, perm: 'license.read' },
        ],
      },
      {
        labelKey: 'nav.agentAccess',
        children: [
          { to: '/agent-tokens', labelKey: 'nav.agentTokens', icon: KeyRound, perm: 'agent.token.read' },
          { to: '/mcp-sessions', labelKey: 'nav.mcpSessions', icon: Cable, perm: 'agent.mcp.read' },
          { to: '/agent-call-logs', labelKey: 'nav.agentCallLogs', icon: ScrollText, perm: 'agent.calllog.read' },
        ],
      },
      {
        labelKey: 'nav.systemMaintenance',
        children: [
          { to: '/artifact-versions', labelKey: 'nav.artifactVersions', icon: Archive, perm: ['system.update', 'node.manage'] },
          { to: '/database', labelKey: 'nav.database', icon: Database, perm: 'system.db.browse' },
          { to: '/system-update', labelKey: 'nav.systemUpdate', icon: RefreshCw, perm: 'system.update' },
        ],
      },
    ],
  },
]

function permOk(perm: string | string[] | undefined, nodes: Set<string>, admin: boolean): boolean {
  if (admin) return true
  if (perm === undefined) return true
  if (Array.isArray(perm)) return perm.some((p) => nodes.has(p))
  return nodes.has(perm)
}

function filterNavGroups(groups: NavGroup[], nodes: Set<string>, admin: boolean): NavGroup[] {
  const out: NavGroup[] = []
  for (const g of groups) {
    if (g.to !== undefined) {
      if (permOk(g.perm, nodes, admin)) out.push(g)
      continue
    }
    const children = (g.children ?? []).filter((c) => permOk(c.perm, nodes, admin))
    const sections = (g.sections ?? [])
      .map((s) => ({ ...s, children: s.children.filter((c) => permOk(c.perm, nodes, admin)) }))
      .filter((s) => s.children.length > 0)
    if (children.length === 0 && sections.length === 0) continue
    out.push({
      ...g,
      children: children.length > 0 ? children : undefined,
      sections: sections.length > 0 ? sections : undefined,
    })
  }
  return out
}

/**
 * 按有效权限节点裁剪导航（FR-431）。平台管理员全开；
 * `nodes` 为 null 时仅保留无 perm 要求的入口（保守），一般由 navGroupsForRole 先映射种子。
 */
export function navGroupsForPermissions(nodes: Set<string> | null, admin: boolean): NavGroup[] {
  if (admin) return filterNavGroups(NAV_GROUPS, new Set(), true)
  return filterNavGroups(NAV_GROUPS, nodes ?? new Set<string>(), false)
}

/**
 * 旧角色值包装：权限尚未加载时用 DEFAULT_ROLE_NODES 种子映射可见性。
 * 平台管理员全路径可见；未知角色 / 无种子 → 仅首页（避免清空权限后仍显示旧路由）。
 */
export function navGroupsForRole(role: number | null): NavGroup[] {
  if (isPlatformAdmin(role)) return navGroupsForPermissions(null, true)
  const seed = DEFAULT_ROLE_NODES[role ?? -1] ?? []
  if (seed.length === 0) {
    // 无种子时只保留无 perm 要求的入口（平台首页）
    return filterNavGroups(NAV_GROUPS, new Set<string>(), false)
  }
  return navGroupsForPermissions(new Set(seed), false)
}

function flattenGroups(groups: NavGroup[]): { to: string; labelKey: string }[] {
  const out: { to: string; labelKey: string }[] = []
  for (const g of groups) {
    if (g.to) out.push({ to: g.to, labelKey: g.labelKey })
    for (const c of g.children ?? []) out.push({ to: c.to, labelKey: c.labelKey })
    for (const s of g.sections ?? []) for (const c of s.children) out.push({ to: c.to, labelKey: c.labelKey })
  }
  return out
}

/**
 * 扁平化导航为 `{to, labelKey}` 列表。
 * 传 role（number|null）走角色种子包装；传 Set+admin 走权限裁剪。
 */
export function flatNavItems(
  nodesOrRole: Set<string> | number | null,
  admin?: boolean,
): { to: string; labelKey: string }[] {
  if (typeof nodesOrRole === 'number' || nodesOrRole === null) {
    return flattenGroups(navGroupsForRole(nodesOrRole))
  }
  return flattenGroups(navGroupsForPermissions(nodesOrRole, admin ?? false))
}
