/**
 * FR-496 阶段 5：工作区导航数据层（四工作区 × 组 × 页面）。
 *
 * 【为什么「新增」而不是「改写」nav-config.ts】
 * 这一步牵动权限裁剪与深链，是整轮重构中最敏感的一步，因此：
 * - `nav-config.ts` 保持原样不动，作为六域扁平 IA 的**对照物**与回滚路径；
 *   两套数据并存，由 `workspace-navigation.test.ts` 逐条证明「37 个导航目的地的可见性语义等价」；
 * - 归位依据是 `.tmp/设计/原路由-新入口对照.csv`（45 路由 = 37 导航 + 5 子路由 + 3 认证页）：
 *   本文件登记其中 42 条（37 + 5）并按工作区分组；3 个认证页见 {@link AUTH_ROUTE_PATHS}，不进工作区；
 * - 阶段 6 把 ConsoleSidebar / MobileConsoleNav / CommandPalette 切到本文件后，再删 `nav-config.ts`
 *   的六域定义（`Workspace.tsx` 的路由表承接 RouteRegistry 语义，不得为缩短侧栏删登记项）。
 *
 * 【权限语义：一个不发明、一个不省略】
 * - 37 个导航目的地：`perm` / `labelKey` / `icon` 与 `NAV_GROUPS` 同路径项**逐条同值**（含 any-of 数组），
 *   由测试机械核对，避免两套数据悄悄漂移；
 * - 5 个子路由：`NAV_GROUPS` 里没有对应项，故**继承其入口页的 perm**（如 `/instances/:id` 继承
 *   `/instances` 的 `instance.read`）。语义上「看不见列表页 ⇒ 也看不见它的深链」，
 *   空权限下不会漏出任何实例/发布/会话深链——这是新增项而非权限放宽；
 * - 裁剪算法（`permOk` / `filterGroups`）与 `nav-config.ts` 私有实现保持同形，只在本层复刻一份，
 *   不 import 私有函数（那需要改 nav-config.ts，违背本阶段约束）。
 *
 * 【i18n】`labelKey` 沿用现有 `nav.*` 风格：能用既有键的地方一律复用（观测 / 客户端分发 /
 * 身份与访问 / 存储与备份 / Agent 接入 / 系统与维护 / 实例），仅新增工作区、组与子路由所需的键，
 * 清单见 {@link WORKSPACE_I18N_KEYS_TO_ADD}，待阶段 6/7 补进 `src/i18n/{zh,en}.json`。
 * 本文件不调用 `t()`，因此不影响 `i18n/missing-keys.test.ts` 的缺键门禁。
 */

import {
  Activity,
  AlertTriangle,
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
  LayoutDashboard,
  LayoutGrid,
  Layers,
  LayoutTemplate,
  ListChecks,
  Network,
  RefreshCw,
  Scale,
  ScrollText,
  Server,
  Settings2,
  Shield,
  ShieldCheck,
  User,
  UsersRound,
} from 'lucide-react'

import { DEFAULT_ROLE_NODES, isPlatformAdmin } from '@/lib/roles'
import { type NavGroup } from './nav-config'

/** 四个工作区的稳定标识（顶栏切换、当前工作区判定、URL 前缀决策共用）。 */
export type WorkspaceKey = 'ops' | 'observability' | 'operation' | 'platform'

/** 一个工作区：一组带标题的分组，分组内部复用现有 {@link NavGroup} 结构（不另造平行类型）。 */
export interface WorkspaceDef {
  key: WorkspaceKey
  /** i18n 键，与现有 `labelKey` 风格一致（`nav.*`）。 */
  labelKey: string
  /** 分组列表；渲染前请一律走 {@link workspacesForPermissions} / {@link workspacesForRole} 裁剪。 */
  groups: NavGroup[]
}

/**
 * CSV 登记的 5 条子路由（深链，非侧栏导航目的地）。
 * 阶段 6 的 CommandPalette / 资源导航可用它把「深链」与「导航目的地」分开处理。
 */
export const SUB_ROUTE_PATTERNS = [
  '/instances/new',
  '/instances/:id',
  '/instances/:id/files',
  '/client-channels/:id/publish',
  '/bots/sessions/:id',
] as const

/** 独立认证页（CSV 登记）：登录 / 首启引导 / 接受邀请——不属于任何工作区。 */
export const AUTH_ROUTE_PATHS = ['/login', '/setup', '/invite'] as const

/**
 * 四工作区定义（未裁剪的原始数据）。
 * 顺序即顶栏顺序：服务器运维 → 观测与自动化 → 运营与分发 → 平台管理。
 */
export const WORKSPACES: WorkspaceDef[] = [
  {
    key: 'ops',
    labelKey: 'nav.workspaceOps',
    groups: [
      {
        // CSV「资源」：集群总览 / 全部实例 / 节点（侧栏主快捷键区）
        key: 'opsResources',
        labelKey: 'nav.resources',
        icon: LayoutDashboard,
        children: [
          { to: '/', labelKey: 'nav.platformHome', icon: LayoutDashboard },
          { to: '/instances', labelKey: 'nav.allInstances', icon: Box, perm: 'instance.read' },
          { to: '/nodes', labelKey: 'nav.nodes', icon: Server, perm: 'node.read' },
        ],
      },
      {
        // CSV「群组与工作台」：拓扑 / 群组 + 侧栏底部两个功能入口
        key: 'opsGroupsWorkbench',
        labelKey: 'nav.groupsWorkbench',
        icon: Network,
        children: [
          { to: '/networks/topology', labelKey: 'nav.networkTopology', icon: GitBranch, perm: 'network.read' },
          { to: '/networks', labelKey: 'nav.groupManagement', icon: Network, perm: 'network.read' },
          { to: '/super', labelKey: 'nav.superWorkbench', icon: LayoutGrid, perm: 'super.read' },
          { to: '/director', labelKey: 'nav.director', icon: Clapperboard, perm: 'director.read' },
        ],
      },
      {
        // CSV「实例」：3 条实例深链。perm 继承 `/instances` 的 instance.read（见文件头说明）
        key: 'opsInstances',
        labelKey: 'nav.instances',
        icon: Box,
        children: [
          { to: '/instances/new', labelKey: 'nav.instanceWizard', icon: Box, perm: 'instance.read' },
          { to: '/instances/:id', labelKey: 'nav.instanceConsole', icon: Box, perm: 'instance.read' },
          { to: '/instances/:id/files', labelKey: 'nav.instanceFiles', icon: FileClock, perm: 'instance.read' },
        ],
      },
    ],
  },
  {
    key: 'observability',
    labelKey: 'nav.workspaceObservability',
    groups: [
      {
        // CSV「观测」
        key: 'obsMonitoring',
        labelKey: 'nav.observability',
        icon: Activity,
        children: [
          { to: '/monitor', labelKey: 'nav.monitoring', icon: Activity, perm: 'monitor.read' },
          { to: '/logs', labelKey: 'nav.logs', icon: ScrollText, perm: 'log.read' },
          { to: '/statistics', labelKey: 'nav.statistics', icon: BarChart3, perm: 'stats.read' },
          // 告警（FR-085）：读或管任一权限即可见——只读用户也能查事件/规则，无写权限时页内入口自动收敛。
          { to: '/alerts', labelKey: 'nav.alerts', icon: AlertTriangle, perm: ['alert.read', 'alert.manage'] },
        ],
      },
      {
        // CSV「自动化与治理」：任务 / 定时 / 备份 / 配置基线（后三者原属服务器域与平台设置域）
        key: 'obsAutomation',
        labelKey: 'nav.automationGovernance',
        icon: ListChecks,
        children: [
          { to: '/tasks', labelKey: 'nav.tasks', icon: ListChecks, perm: 'task.read' },
          { to: '/schedules', labelKey: 'nav.schedules', icon: FileClock, perm: 'schedule.read' },
          { to: '/backups', labelKey: 'nav.backups', icon: Archive, perm: 'backup.read' },
          { to: '/config-baselines', labelKey: 'nav.configBaselines', icon: GitCompareArrows, perm: ['file.read', 'instance.read'] },
        ],
      },
      {
        // CSV「消息」：通知中心单独成组
        key: 'obsMessages',
        labelKey: 'nav.messages',
        icon: Bell,
        children: [
          { to: '/notifications', labelKey: 'nav.notifications', icon: Bell, perm: 'notification.read' },
        ],
      },
    ],
  },
  {
    key: 'operation',
    labelKey: 'nav.workspaceOperation',
    groups: [
      {
        // CSV「玩家与压测」：玩家 / Bot 与压测（原属服务器域）
        key: 'opPlayersLoad',
        labelKey: 'nav.playersAndLoad',
        icon: User,
        children: [
          { to: '/players', labelKey: 'nav.players', icon: User, perm: 'player.read' },
          { to: '/bots', labelKey: 'nav.bots', icon: Bot, perm: 'bot.read' },
          { to: '/bots/sessions/:id', labelKey: 'nav.botLoadSession', icon: Bot, perm: 'bot.read' },
        ],
      },
      {
        // CSV「客户端分发」；发布向导为频道页深链，perm 继承 `/client-channels` 的 channel.read
        key: 'opClientDistribution',
        labelKey: 'nav.clientDistribution',
        icon: DownloadCloud,
        children: [
          { to: '/client-channels', labelKey: 'nav.clientChannels', icon: DownloadCloud, perm: 'channel.read' },
          { to: '/client-dist-ops', labelKey: 'nav.clientDistOps', icon: ShieldCheck, perm: 'dist.ops.read' },
          { to: '/client-channels/:id/publish', labelKey: 'nav.clientPublishWizard', icon: DownloadCloud, perm: 'channel.read' },
        ],
      },
    ],
  },
  {
    key: 'platform',
    labelKey: 'nav.workspacePlatform',
    groups: [
      {
        // CSV「身份与访问」
        key: 'platformIdentity',
        labelKey: 'nav.identityAccess',
        icon: UsersRound,
        children: [
          { to: '/users', labelKey: 'nav.users', icon: User, perm: 'user.read' },
          { to: '/groups', labelKey: 'nav.groups', icon: UsersRound, perm: 'group.read' },
          { to: '/permissions', labelKey: 'nav.permissions', icon: Shield, perm: 'rbac.read' },
        ],
      },
      {
        // CSV「运行时与内容」：原「存储与运行时」「内容模板」两节交叉重组
        key: 'platformRuntimeContent',
        labelKey: 'nav.runtimeAndContent',
        icon: Layers,
        children: [
          { to: '/runtime-assets', labelKey: 'nav.runtimeAssets', icon: Layers, perm: 'node.read' },
          { to: '/templates', labelKey: 'nav.templates', icon: LayoutTemplate, perm: 'template.read' },
          { to: '/artifact-versions', labelKey: 'nav.artifactVersions', icon: Archive, perm: ['system.update', 'node.manage'] },
        ],
      },
      {
        // CSV「存储与备份」：备份存储在此，全量/增量备份入口在观测与自动化的「自动化与治理」
        key: 'platformStorageBackup',
        labelKey: 'nav.storageRuntime',
        icon: HardDrive,
        children: [
          { to: '/storage', labelKey: 'nav.storage', icon: HardDrive, perm: 'node.manage' },
          { to: '/artifact-storages', labelKey: 'nav.artifactStorages', icon: CloudUpload, perm: 'node.manage' },
          { to: '/backup-storages', labelKey: 'nav.backupStorages', icon: Archive, perm: 'backup.read' },
        ],
      },
      {
        // CSV「Agent 接入」
        key: 'platformAgentAccess',
        labelKey: 'nav.agentAccess',
        icon: KeyRound,
        children: [
          { to: '/agent-tokens', labelKey: 'nav.agentTokens', icon: KeyRound, perm: 'agent.token.read' },
          { to: '/mcp-activity', labelKey: 'nav.mcpActivity', icon: Cable, perm: 'agent.mcp.read' },
          { to: '/agent-call-logs', labelKey: 'nav.agentCallLogs', icon: ScrollText, perm: 'agent.calllog.read' },
        ],
      },
      {
        // CSV「系统与维护」
        key: 'platformSystemMaintenance',
        labelKey: 'nav.systemMaintenance',
        icon: Settings2,
        children: [
          { to: '/audit', labelKey: 'nav.audit', icon: FileClock, perm: 'audit.read' },
          { to: '/settings', labelKey: 'nav.systemSettings', icon: Settings2, perm: 'settings.read' },
          { to: '/database', labelKey: 'nav.database', icon: Database, perm: 'system.db.browse' },
          { to: '/system-update', labelKey: 'nav.systemUpdate', icon: RefreshCw, perm: 'system.update' },
          { to: '/licenses', labelKey: 'licenses.entry', icon: Scale, perm: 'license.read' },
        ],
      },
    ],
  },
]

/**
 * 本层新增、尚未进语言包的 i18n 键（阶段 6/7 补进 `src/i18n/{zh,en}.json`）。
 * 测试会同时守住两个方向：新增键必须登记在此，登记在此的键必须真被本层使用。
 */
export const WORKSPACE_I18N_KEYS_TO_ADD: string[] = [
  // 工作区名
  'nav.workspaceOps',
  'nav.workspaceObservability',
  'nav.workspaceOperation',
  'nav.workspacePlatform',
  // 新增分组名（其余分组复用既有 nav.* 键）
  'nav.resources',
  'nav.groupsWorkbench',
  'nav.automationGovernance',
  'nav.messages',
  'nav.playersAndLoad',
  'nav.runtimeAndContent',
  // 子路由页面名
  'nav.instanceWizard',
  'nav.instanceConsole',
  'nav.instanceFiles',
  'nav.clientPublishWizard',
  'nav.botLoadSession',
]

/**
 * 权限判定：与 `nav-config.ts` 的私有 `permOk` 同语义（FR-431/432）。
 * `perm` 为数组时是 any-of；管理员全开；无 `perm` 的入口对所有人可见。
 */
function permOk(perm: string | string[] | undefined, nodes: Set<string>, admin: boolean): boolean {
  if (admin) return true
  if (perm === undefined) return true
  if (Array.isArray(perm)) return perm.some((p) => nodes.has(p))
  return nodes.has(perm)
}

/**
 * 裁剪一层的分组：与 `nav-config.ts` 的私有 `filterNavGroups` 同语义——
 * 叶子分组按自身 `perm` 判定；容器分组过滤 children/sections，两级都空则整组丢弃。
 */
function filterGroups(groups: NavGroup[], nodes: Set<string>, admin: boolean): NavGroup[] {
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
 * 按有效权限裁剪后的工作区列表（语义与 `navGroupsForPermissions` 一致）。
 * 额外做一件 nav-config 不需要做的事：把裁剪后没有任何分组的工作区整块丢弃
 * （顶栏不应出现一个点进去空空如也的工作区）。
 * `nodes` 为 null 时仅保留无 `perm` 要求的入口（保守）。
 */
export function workspacesForPermissions(nodes: Set<string> | null, admin: boolean): WorkspaceDef[] {
  const effective = nodes ?? new Set<string>()
  const out: WorkspaceDef[] = []
  for (const ws of WORKSPACES) {
    const groups = admin
      ? filterGroups(ws.groups, new Set<string>(), true)
      : filterGroups(ws.groups, effective, false)
    if (groups.length === 0) continue
    out.push({ ...ws, groups })
  }
  return out
}

/**
 * 角色种子包装（语义与 `navGroupsForRole` 一致）：权限尚未加载时用 DEFAULT_ROLE_NODES 种子的可见性。
 * 平台管理员全路径可见；未知角色 / 无种子 → 仅无 `perm` 入口（避免清空权限后仍显示旧路由）。
 */
export function workspacesForRole(role: number | null): WorkspaceDef[] {
  if (isPlatformAdmin(role)) return workspacesForPermissions(null, true)
  const seed = DEFAULT_ROLE_NODES[role ?? -1] ?? []
  if (seed.length === 0) return workspacesForPermissions(new Set<string>(), false)
  return workspacesForPermissions(new Set(seed), false)
}

/** 扁平化分组为 `{to, labelKey}` 列表（与 `nav-config.ts` 的 flattenGroups 同序同形状）。 */
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
 * 扁平化全部工作区入口（供 CommandPalette 索引），返回形状与现有 `flatNavItems` 相同。
 * 传 role（number|null）走角色种子包装；传 Set+admin 走权限裁剪。
 * 注意：结果含 5 条子路由深链（{@link SUB_ROUTE_PATTERNS}），这不是权限放宽——
 * 它们继承入口页的 perm，父页不可见时同样不可见。
 */
export function flatWorkspaceItems(
  nodesOrRole: Set<string> | number | null,
  admin?: boolean,
): { to: string; labelKey: string }[] {
  const workspaces =
    typeof nodesOrRole === 'number' || nodesOrRole === null
      ? workspacesForRole(nodesOrRole)
      : workspacesForPermissions(nodesOrRole, admin ?? false)
  const out: { to: string; labelKey: string }[] = []
  for (const ws of workspaces) out.push(...flattenGroups(ws.groups))
  return out
}

/** 一条可参与路径匹配的登记项（导航目的地或子路由深链）。 */
interface PathRegistration {
  /** 展示路径；含 `:param` 的动态段用于深链模式匹配。 */
  pattern: string
  workspace: WorkspaceKey
  groupKey: string
}

const PATH_REGISTRATIONS: PathRegistration[] = (() => {
  const out: PathRegistration[] = []
  for (const ws of WORKSPACES) {
    for (const g of ws.groups) {
      if (g.to) out.push({ pattern: g.to, workspace: ws.key, groupKey: g.key })
      for (const c of g.children ?? []) out.push({ pattern: c.to, workspace: ws.key, groupKey: g.key })
      for (const s of g.sections ?? []) {
        for (const c of s.children) out.push({ pattern: c.to, workspace: ws.key, groupKey: g.key })
      }
    }
  }
  return out
})()

/** 路径归一化：去 query/hash、折叠重复斜杠、去尾斜杠；空串归为根路径 `/`。 */
function normalizePath(pathname: string): string {
  const cut = pathname.split('?')[0].split('#')[0]
  const collapsed = cut.replace(/\/{2,}/g, '/')
  const trimmed = collapsed.replace(/\/+$/, '')
  if (trimmed === '') return '/'
  return trimmed.startsWith('/') ? trimmed : `/${trimmed}`
}

function splitSegments(path: string): string[] {
  return path.split('/').filter((s) => s.length > 0)
}

/**
 * 匹配优先级（分数越高越优先；同分取登记顺序先者）：
 *   3 + 段数：静态路径完全相等（`/instances/new` 因此不会被 `/instances/:id` 抢走）；
 *   2 + 段数：动态模式完全相等（段数一致、静态段逐段一致、`:param` 匹配非空段）；
 *   1 + 段数：静态路径是目标路径的段对齐祖先（未知子路径仍归入其父级工作区，如 `/monitor/history`）；
 *   0：不匹配。根路径只在精确命中时归位，否则每个路径都会落在 `/`。
 */
function matchScore(pattern: string, path: string): number {
  const patternSegs = splitSegments(pattern)
  const pathSegs = splitSegments(path)

  if (patternSegs.length === 0) return pathSegs.length === 0 ? 3 : 0

  const dynamic = patternSegs.some((s) => s.startsWith(':'))
  if (dynamic) {
    if (patternSegs.length !== pathSegs.length) return 0
    for (let i = 0; i < patternSegs.length; i++) {
      const seg = patternSegs[i]
      if (seg.startsWith(':')) {
        if (pathSegs[i] === '') return 0
      } else if (seg !== pathSegs[i]) {
        return 0
      }
    }
    return 2 + patternSegs.length
  }

  if (patternSegs.length === pathSegs.length) {
    return patternSegs.every((seg, i) => seg === pathSegs[i]) ? 3 + patternSegs.length : 0
  }
  // 祖先匹配按「模式 + /」判断，段边界对齐：`/databases` 不会命中 `/database`
  if (pathSegs.length > patternSegs.length && path.startsWith(pattern + '/')) return 1 + patternSegs.length
  return 0
}

/**
 * 路径 → 工作区 / 分组 / 命中的登记项。用于顶栏高亮、「当前工作区」判定与面包屑兜底；
 * 未命中返回 null。静态路径优先于动态模式（`/instances/new` 归向导，不归 `:id` 控制台）。
 */
export function resolveWorkspacePath(
  pathname: string,
): { key: WorkspaceKey; groupKey: string; pattern: string } | null {
  const path = normalizePath(pathname)
  let best: { entry: PathRegistration; score: number } | null = null
  for (const entry of PATH_REGISTRATIONS) {
    const score = matchScore(entry.pattern, path)
    if (score === 0) continue
    if (!best || score > best.score) best = { entry, score }
  }
  if (!best) return null
  return { key: best.entry.workspace, groupKey: best.entry.groupKey, pattern: best.entry.pattern }
}

/** 路径 → 所属工作区；用于顶栏高亮与「当前工作区」判定，未命中返回 null。 */
export function workspaceOfPath(pathname: string): WorkspaceKey | null {
  return resolveWorkspacePath(pathname)?.key ?? null
}
