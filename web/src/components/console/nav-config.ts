import {
  Activity,
  Archive,
  BarChart3,
  Bell,
  Bot,
  Box,
  Boxes,
  Clapperboard,
  Clock,
  Database,
  DownloadCloud,
  FileClock,
  Gamepad2,
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
  User,
  UsersRound,
  type LucideIcon,
} from 'lucide-react'

import { type NavEntry } from './SidebarNavLink'

/**
 * 一个导航分区（leaf=单链接；children=可展开子项；instances 标记内嵌实例树/节点切换；
 * sections 标记带小标题的二级分节，用于「系统」域的「平台与维护 / 账户与审计」）。
 */
export interface NavGroup {
  key: string
  labelKey: string
  icon: LucideIcon
  to?: string
  children?: NavEntry[]
  instances?: boolean
  sections?: NavSection[]
}

/** 「系统」域内的带标题二级分节（design §7）。 */
export interface NavSection {
  labelKey: string
  children: NavEntry[]
}

/**
 * 五域导航信息架构（FR-131 / design §7；观测域 FR-215）：总览 / 集群 / 观测 / 运营 / 系统。
 * 从原 11 个粒度不一的一级精简为 5 个按运维心智分域的一级，高频域在上、低频「系统」沉底。
 * 能力不丢：实例树/节点切换并入「集群·实例」；监控总览/日志/统计归「观测」（任务中心迁「系统·平台与维护」，
 * 告警为 FR-216 接手前过渡留位）；模板/备份/定时归「运营」；平台资源/账户审计归「系统」两小节。
 */
export const NAV_GROUPS: NavGroup[] = [
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
export function navGroupsForRole(role: number | null): NavGroup[] {
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
