import { describe, expect, it } from 'vitest'
import type { LucideIcon } from 'lucide-react'

import {
  AUTH_ROUTE_PATHS,
  SUB_ROUTE_PATTERNS,
  WORKSPACES,
  WORKSPACE_I18N_KEYS_TO_ADD,
  flatWorkspaceItems,
  resolveWorkspacePath,
  workspaceOfPath,
  workspacesForPermissions,
  workspacesForRole,
  type WorkspaceKey,
} from './workspace-navigation'
import { NAV_GROUPS, flatNavItems, type NavGroup } from './nav-config'
import {
  ALL_PERMISSION_NODE_IDS,
  DEFAULT_ROLE_NODES,
  ROLE_MEMBER,
  ROLE_PLATFORM_ADMIN,
} from '@/lib/roles'
import zh from '@/i18n/zh.json'

/**
 * FR-496 阶段 5 的工作区导航数据层测试。
 * 核心主张只有一条：**换结构不换权限语义**。因此每个断言都拿旧六域（nav-config.ts）
 * 当参照物比，而不是自证新数据「看起来对」。
 */

interface CollectedEntry {
  to: string
  labelKey: string
  icon?: LucideIcon
  perm?: string | string[]
}

/** 展开任意 NavGroup[]（含 children / sections / 叶子分组自身），产出全部入口。 */
function collectEntries(groups: NavGroup[]): CollectedEntry[] {
  const out: CollectedEntry[] = []
  for (const g of groups) {
    if (g.to) out.push({ to: g.to, labelKey: g.labelKey, icon: g.icon, perm: g.perm })
    for (const c of g.children ?? []) out.push({ to: c.to, labelKey: c.labelKey, icon: c.icon, perm: c.perm })
    for (const s of g.sections ?? []) {
      for (const c of s.children) out.push({ to: c.to, labelKey: c.labelKey, icon: c.icon, perm: c.perm })
    }
  }
  return out
}

const OLD_ENTRIES = collectEntries(NAV_GROUPS)
const OLD_ENTRY_BY_PATH = new Map(OLD_ENTRIES.map((e) => [e.to, e]))
/**
 * 旧六域**登记**的全部导航目的地：37 条。
 * 这里直接取登记项而不是 `flatNavItems(null, true)`——后者会走角色种子分支且**忽略 admin 参数**
 * （`null` 命中 `navGroupsForRole(null)`），只返回无 perm 要求的首页，不是「管理员全开」。
 * 管理员口径的等价物是 `flatNavItems(adminNodes, true)`，见下面各测试的交叉核对。
 */
const OLD_ALL_PATHS = OLD_ENTRIES.map((e) => e.to)
const NAV_PATH_SET = new Set(OLD_ALL_PATHS)

const WORKSPACE_GROUP_LIST = WORKSPACES.flatMap((w) => w.groups)
const NEW_ENTRIES = collectEntries(WORKSPACE_GROUP_LIST)
const NEW_ENTRY_BY_PATH = new Map(NEW_ENTRIES.map((e) => [e.to, e]))
/** 新工作区层（管理员全开）的全部 `to`：37 条导航目的地 + 5 条子路由深链。 */
const NEW_ALL_PATHS = flatWorkspaceItems(new Set<string>(), true).map((i) => i.to)

const SUB_ROUTE_SET = new Set<string>(SUB_ROUTE_PATTERNS)

/** 子路由 → 其入口页（CSV 归位）。子路由的 perm 继承该入口页的 perm。 */
const SUB_ROUTE_PARENT: Record<string, string> = {
  '/instances/new': '/instances',
  '/instances/:id': '/instances',
  '/instances/:id/files': '/instances',
  '/client-channels/:id/publish': '/client-channels',
  '/bots/sessions/:id': '/bots',
}

const adminNodes = new Set(ALL_PERMISSION_NODE_IDS)

/** 把新层全部 labelKey 收集成一个去重集合（工作区名 + 组名 + 页面名）。 */
function allUsedLabelKeys(): string[] {
  return [
    ...new Set([
      ...WORKSPACES.map((w) => w.labelKey),
      ...WORKSPACE_GROUP_LIST.map((g) => g.labelKey),
      ...NEW_ENTRIES.map((e) => e.labelKey),
    ]),
  ]
}

function flattenKeys(obj: Record<string, unknown>, prefix = ''): Set<string> {
  const out = new Set<string>()
  for (const [k, v] of Object.entries(obj)) {
    const path = prefix ? `${prefix}.${k}` : k
    if (v && typeof v === 'object') for (const p of flattenKeys(v as Record<string, unknown>, path)) out.add(p)
    else out.add(path)
  }
  return out
}

const ZH_KEYS = flattenKeys(zh)

/**
 * 等价性主断言（权限口径）：
 * 1) 旧可见的每条导航目的地，新层必须同样可见（零遗漏）；
 * 2) 新层里属于旧全集的条目集合，必须与旧结果**完全相等**（零越权：不能多给一条）；
 * 3) 多出来的只能是 CSV 登记过的子路由，且其入口页同样可见（子路由继承 perm，不是放宽）。
 */
function assertPermissionEquivalence(nodes: Set<string>, admin: boolean, label: string) {
  const oldPaths = flatNavItems(nodes, admin).map((i) => i.to)
  const newPaths = flatWorkspaceItems(nodes, admin).map((i) => i.to)
  const oldSet = new Set(oldPaths)
  const newSet = new Set(newPaths)

  for (const to of oldPaths) expect(newSet.has(to), `${label}：新层丢了旧导航目的地 ${to}`).toBe(true)

  const newNavOnly = newPaths.filter((to) => NAV_PATH_SET.has(to))
  expect(new Set(newNavOnly), `${label}：导航目的地的可见性集合不一致`).toEqual(oldSet)

  const extras = newPaths.filter((to) => !NAV_PATH_SET.has(to))

  for (const to of extras) {
    expect(SUB_ROUTE_SET.has(to), `${label}：出现未登记的新增项 ${to}`).toBe(true)
    const parent = SUB_ROUTE_PARENT[to]
    expect(parent, `${label}：${to} 缺入口页登记`).toBeDefined()
    expect(oldSet.has(parent), `${label}：入口页 ${parent} 不可见却漏出深链 ${to}`).toBe(true)
  }
}

describe('workspace-navigation：四工作区 × 组 × 页面（FR-496 阶段 5）', () => {
  it('工作区顺序、分组 key 与逐条页面归位对齐 CSV', () => {
    expect(WORKSPACES.map((w) => w.key)).toEqual(['ops', 'observability', 'operation', 'platform'])
    expect(WORKSPACES.map((w) => w.labelKey)).toEqual([
      'nav.workspaceOps',
      'nav.workspaceObservability',
      'nav.workspaceOperation',
      'nav.workspacePlatform',
    ])

    const structure = WORKSPACES.map((w) => ({
      key: w.key,
      groups: w.groups.map((g) => ({
        key: g.key,
        labelKey: g.labelKey,
        pages: (g.children ?? []).map((c) => c.to),
      })),
    }))

    expect(structure).toEqual([
      {
        key: 'ops',
        groups: [
          { key: 'opsResources', labelKey: 'nav.resources', pages: ['/', '/instances', '/nodes'] },
          {
            key: 'opsGroupsWorkbench',
            labelKey: 'nav.groupsWorkbench',
            pages: ['/networks/topology', '/networks', '/super', '/director'],
          },
          {
            key: 'opsInstances',
            labelKey: 'nav.instances',
            pages: ['/instances/new', '/instances/:id', '/instances/:id/files'],
          },
        ],
      },
      {
        key: 'observability',
        groups: [
          { key: 'obsMonitoring', labelKey: 'nav.observability', pages: ['/monitor', '/logs', '/statistics', '/alerts'] },
          {
            key: 'obsAutomation',
            labelKey: 'nav.automationGovernance',
            pages: ['/tasks', '/schedules', '/backups', '/config-baselines'],
          },
          { key: 'obsMessages', labelKey: 'nav.messages', pages: ['/notifications'] },
        ],
      },
      {
        key: 'operation',
        groups: [
          { key: 'opPlayersLoad', labelKey: 'nav.playersAndLoad', pages: ['/players', '/bots', '/bots/sessions/:id'] },
          {
            key: 'opClientDistribution',
            labelKey: 'nav.clientDistribution',
            pages: ['/client-channels', '/client-dist-ops', '/client-channels/:id/publish'],
          },
        ],
      },
      {
        key: 'platform',
        groups: [
          { key: 'platformIdentity', labelKey: 'nav.identityAccess', pages: ['/users', '/groups', '/permissions'] },
          {
            key: 'platformRuntimeContent',
            labelKey: 'nav.runtimeAndContent',
            pages: ['/runtime-assets', '/templates', '/artifact-versions'],
          },
          {
            key: 'platformStorageBackup',
            labelKey: 'nav.storageRuntime',
            pages: ['/storage', '/artifact-storages', '/backup-storages'],
          },
          {
            key: 'platformAgentAccess',
            labelKey: 'nav.agentAccess',
            pages: ['/agent-tokens', '/mcp-activity', '/agent-call-logs'],
          },
          {
            key: 'platformSystemMaintenance',
            labelKey: 'nav.systemMaintenance',
            pages: ['/audit', '/settings', '/database', '/system-update', '/licenses'],
          },
        ],
      },
    ])
  })

  it('登记条数对账 CSV：42 条进工作区 + 3 条认证页 = 45；三层结构无 sections', () => {
    // 三层结构：工作区 → 组 → 页面；组是容器（无自身 to、无二级 sections）
    expect(WORKSPACE_GROUP_LIST.every((g) => g.to === undefined)).toBe(true)
    expect(WORKSPACE_GROUP_LIST.every((g) => g.sections === undefined)).toBe(true)
    expect(WORKSPACE_GROUP_LIST.every((g) => (g.children?.length ?? 0) > 0)).toBe(true)

    // 工作区 → 组 → 页面数（对齐 CSV 的 3/3/3、3/3/1、2/1、5 分类）
    expect(
      WORKSPACES.map((w) => [w.key, w.groups.length, w.groups.reduce((n, g) => n + (g.children?.length ?? 0), 0)]),
    ).toEqual([
      ['ops', 3, 10],
      ['observability', 3, 9],
      ['operation', 2, 6],
      ['platform', 5, 17],
    ])

    const allPaths = NEW_ENTRIES.map((e) => e.to)
    expect(new Set(allPaths).size, '工作区层存在重复登记的路径').toBe(allPaths.length)

    const navPaths = allPaths.filter((p) => NAV_PATH_SET.has(p))
    const subPaths = allPaths.filter((p) => SUB_ROUTE_SET.has(p))
    expect(navPaths.length, '37 个导航目的地').toBe(37)
    expect(new Set(subPaths), '5 条子路由全部登记').toEqual(SUB_ROUTE_SET)
    expect(navPaths.length + subPaths.length).toBe(allPaths.length)

    // 45 = 37 导航 + 5 子路由 + 3 认证页
    expect(allPaths.length + AUTH_ROUTE_PATHS.length).toBe(45)

    // 组 key 全局唯一（折叠态 / 图标映射按 key 记账）
    const groupKeys = WORKSPACE_GROUP_LIST.map((g) => g.key)
    expect(new Set(groupKeys).size).toBe(groupKeys.length)

    // 每条子路由的入口页都在旧导航里登记过（perm 来源可追溯）
    for (const pattern of SUB_ROUTE_PATTERNS) {
      expect(NAV_PATH_SET.has(SUB_ROUTE_PARENT[pattern]), `${pattern} 的入口页不在旧导航中`).toBe(true)
    }
  })

  it('perm / labelKey / icon 与 NAV_GROUPS 同路径项逐条同值（零发明、零省略）', () => {
    expect(OLD_ALL_PATHS.length, '旧导航目的地应为 37 条').toBe(37)
    expect(OLD_ENTRY_BY_PATH.size).toBe(37)
    // 交叉核对参照物口径：管理员全开 = 登记全集（顺序也一致）
    expect(flatNavItems(adminNodes, true).map((i) => i.to)).toEqual(OLD_ALL_PATHS)

    for (const to of OLD_ALL_PATHS) {
      const oldEntry = OLD_ENTRY_BY_PATH.get(to)
      const newEntry = NEW_ENTRY_BY_PATH.get(to)
      expect(newEntry, `${to} 未在工作区层登记`).toBeDefined()
      expect(newEntry?.perm, `${to} 的 perm 与 NAV_GROUPS 不一致`).toEqual(oldEntry?.perm)
      expect(newEntry?.labelKey, `${to} 的 labelKey 与 NAV_GROUPS 不一致`).toBe(oldEntry?.labelKey)
      expect(newEntry?.icon, `${to} 的 icon 与 NAV_GROUPS 不一致`).toBe(oldEntry?.icon)
    }

    // 权限节点词汇表：新层用到的节点必须全部来自旧导航（不得凭空发明）
    const oldPerms = new Set<string>()
    for (const e of OLD_ENTRIES) {
      if (e.perm === undefined) continue
      for (const p of Array.isArray(e.perm) ? e.perm : [e.perm]) oldPerms.add(p)
    }
    for (const e of NEW_ENTRIES) {
      if (e.perm === undefined) continue
      for (const p of Array.isArray(e.perm) ? e.perm : [e.perm]) {
        expect(oldPerms.has(p), `凭空发明的权限节点 ${p}（${e.to}）`).toBe(true)
      }
    }
  })

  it('any-of 数组原样保留，子路由 perm 继承入口页', () => {
    // any-of 必须仍是数组形态，不能被压成单节点
    expect(NEW_ENTRY_BY_PATH.get('/alerts')?.perm).toEqual(['alert.read', 'alert.manage'])
    expect(NEW_ENTRY_BY_PATH.get('/config-baselines')?.perm).toEqual(['file.read', 'instance.read'])
    expect(NEW_ENTRY_BY_PATH.get('/artifact-versions')?.perm).toEqual(['system.update', 'node.manage'])

    for (const pattern of SUB_ROUTE_PATTERNS) {
      const parent = SUB_ROUTE_PARENT[pattern]
      expect(NEW_ENTRY_BY_PATH.get(pattern)?.perm, `${pattern} 应继承 ${parent} 的 perm`).toEqual(
        NEW_ENTRY_BY_PATH.get(parent)?.perm,
      )
    }
  })

  it('路由零遗漏：新层包含旧导航全部 37 条，新增恰为 5 条子路由', () => {
    // 管理员全开：旧层 37 条导航目的地，新层 37 + 5 条
    const oldAdminPaths = flatNavItems(adminNodes, true).map((i) => i.to)
    expect(oldAdminPaths, '旧层管理员口径应为 37 条').toEqual(OLD_ALL_PATHS)
    expect(flatWorkspaceItems(adminNodes, true).map((i) => i.to)).toEqual(NEW_ALL_PATHS)

    const newSet = new Set(NEW_ALL_PATHS)
    for (const to of OLD_ALL_PATHS) expect(newSet.has(to), `新层丢失了 ${to}`).toBe(true)

    expect(new Set(NEW_ALL_PATHS.filter((p) => !NAV_PATH_SET.has(p)))).toEqual(SUB_ROUTE_SET)
    expect([...new Set(NEW_ALL_PATHS.filter((p) => NAV_PATH_SET.has(p)))].sort()).toEqual([...OLD_ALL_PATHS].sort())
    expect(newSet.size).toBe(NEW_ALL_PATHS.length)

    // 角色包装下平台管理员同样是「37 + 5」；null 走角色种子（未知角色保守清零），
    // 与 flatNavItems(null, true) 的老行为一致——null 不等于「管理员全开」。
    expect(flatWorkspaceItems(null, true).map((i) => i.to)).toEqual(flatNavItems(null, true).map((i) => i.to))
    expect(flatWorkspaceItems(null, true).map((i) => i.to)).toEqual(['/'])
    expect(flatWorkspaceItems(ROLE_PLATFORM_ADMIN).map((i) => i.to)).toEqual(NEW_ALL_PATHS)
  })

  it('权限裁剪等价：空权限只留无 perm 入口', () => {
    expect(flatNavItems(new Set(), false).map((i) => i.to)).toEqual(['/'])
    expect(flatWorkspaceItems(new Set(), false).map((i) => i.to)).toEqual(['/'])
    expect(flatWorkspaceItems(null, false).map((i) => i.to)).toEqual(['/'])

    const empty = workspacesForPermissions(new Set<string>(), false)
    expect(empty.map((w) => w.key)).toEqual(['ops'])
    expect(empty[0].groups.map((g) => g.key)).toEqual(['opsResources'])
    // 空权限下不出现任何实例 / 发布 / 会话深链
    for (const pattern of SUB_ROUTE_PATTERNS) {
      expect(flatWorkspaceItems(new Set(), false).map((i) => i.to)).not.toContain(pattern)
    }
  })

  it('权限裁剪等价：{instance.read} 场景逐条对照', () => {
    const nodes = new Set(['instance.read'])
    expect(flatNavItems(nodes, false).map((i) => i.to)).toEqual(['/', '/instances', '/config-baselines'])
    expect(flatWorkspaceItems(nodes, false).map((i) => i.to)).toEqual([
      '/',
      '/instances',
      '/instances/new',
      '/instances/:id',
      '/instances/:id/files',
      '/config-baselines',
    ])
  })

  it('权限裁剪等价：多组权限集合下导航目的地可见性完全一致', () => {
    const scenarios: { label: string; nodes: Set<string>; admin?: boolean }[] = [
      { label: '空权限', nodes: new Set<string>() },
      { label: '仅 instance.read', nodes: new Set(['instance.read']) },
      { label: 'node.read + network.read', nodes: new Set(['node.read', 'network.read']) },
      { label: 'any-of 单边 alert.manage', nodes: new Set(['alert.manage']) },
      { label: 'any-of 单边 file.read', nodes: new Set(['file.read']) },
      {
        label: '观测只读',
        nodes: new Set(['monitor.read', 'log.read', 'stats.read', 'alert.read', 'notification.read']),
      },
      { label: '运营域', nodes: new Set(['player.read', 'bot.read', 'channel.read']) },
      { label: '平台域', nodes: new Set(['user.read', 'group.read', 'rbac.read', 'audit.read', 'settings.read']) },
      { label: '全部节点（非管理员）', nodes: adminNodes },
      { label: '管理员短路', nodes: new Set<string>(), admin: true },
      { label: '管理员短路 + 全量节点', nodes: adminNodes, admin: true },
    ]

    for (const { label, nodes, admin } of scenarios) {
      assertPermissionEquivalence(nodes, admin ?? false, label)
    }

    // 管理员短路与全量节点的可见面必须一致（admin 为 true 时忽略 nodes）
    expect(flatWorkspaceItems(adminNodes, true).map((i) => i.to)).toEqual(
      flatWorkspaceItems(new Set<string>(), true).map((i) => i.to),
    )
  })

  it('any-of 语义：/alerts 与 /config-baselines 单边权限都可见', () => {
    const cases: { nodes: string[]; alerts: boolean; baselines: boolean }[] = [
      { nodes: ['alert.read'], alerts: true, baselines: false },
      { nodes: ['alert.manage'], alerts: true, baselines: false },
      { nodes: ['file.read'], alerts: false, baselines: true },
      { nodes: ['instance.read'], alerts: false, baselines: true },
      { nodes: ['monitor.read'], alerts: false, baselines: false },
      { nodes: [], alerts: false, baselines: false },
    ]

    for (const { nodes, alerts, baselines } of cases) {
      const nodesSet = new Set(nodes)
      const oldPaths = flatNavItems(nodesSet, false).map((i) => i.to)
      const newPaths = flatWorkspaceItems(nodesSet, false).map((i) => i.to)

      for (const [label, paths] of [
        ['旧六域', oldPaths],
        ['新工作区', newPaths],
      ] as const) {
        expect(paths.includes('/alerts'), `${label} alerts（节点：${nodes.join(',') || '空'}）`).toBe(alerts)
        expect(paths.includes('/config-baselines'), `${label} config-baselines（节点：${nodes.join(',') || '空'}）`).toBe(
          baselines,
        )
      }
    }
  })

  it('工作区归属：37 条导航目的地逐条归位（CSV 对照）', () => {
    const table: [path: string, workspace: WorkspaceKey, groupKey: string][] = [
      ['/', 'ops', 'opsResources'],
      ['/instances', 'ops', 'opsResources'],
      ['/nodes', 'ops', 'opsResources'],
      ['/networks/topology', 'ops', 'opsGroupsWorkbench'],
      ['/networks', 'ops', 'opsGroupsWorkbench'],
      ['/super', 'ops', 'opsGroupsWorkbench'],
      ['/director', 'ops', 'opsGroupsWorkbench'],
      ['/monitor', 'observability', 'obsMonitoring'],
      ['/logs', 'observability', 'obsMonitoring'],
      ['/statistics', 'observability', 'obsMonitoring'],
      ['/alerts', 'observability', 'obsMonitoring'],
      ['/tasks', 'observability', 'obsAutomation'],
      ['/schedules', 'observability', 'obsAutomation'],
      ['/backups', 'observability', 'obsAutomation'],
      ['/config-baselines', 'observability', 'obsAutomation'],
      ['/notifications', 'observability', 'obsMessages'],
      ['/players', 'operation', 'opPlayersLoad'],
      ['/bots', 'operation', 'opPlayersLoad'],
      ['/client-channels', 'operation', 'opClientDistribution'],
      ['/client-dist-ops', 'operation', 'opClientDistribution'],
      ['/users', 'platform', 'platformIdentity'],
      ['/groups', 'platform', 'platformIdentity'],
      ['/permissions', 'platform', 'platformIdentity'],
      ['/runtime-assets', 'platform', 'platformRuntimeContent'],
      ['/templates', 'platform', 'platformRuntimeContent'],
      ['/artifact-versions', 'platform', 'platformRuntimeContent'],
      ['/storage', 'platform', 'platformStorageBackup'],
      ['/artifact-storages', 'platform', 'platformStorageBackup'],
      ['/backup-storages', 'platform', 'platformStorageBackup'],
      ['/agent-tokens', 'platform', 'platformAgentAccess'],
      ['/mcp-activity', 'platform', 'platformAgentAccess'],
      ['/agent-call-logs', 'platform', 'platformAgentAccess'],
      ['/audit', 'platform', 'platformSystemMaintenance'],
      ['/settings', 'platform', 'platformSystemMaintenance'],
      ['/database', 'platform', 'platformSystemMaintenance'],
      ['/system-update', 'platform', 'platformSystemMaintenance'],
      ['/licenses', 'platform', 'platformSystemMaintenance'],
    ]

    expect(table.map(([p]) => p).sort()).toEqual([...OLD_ALL_PATHS].sort())

    for (const [path, workspace, groupKey] of table) {
      expect(workspaceOfPath(path), `${path} 的工作区`).toBe(workspace)
      expect(resolveWorkspacePath(path)?.groupKey, `${path} 的分组`).toBe(groupKey)
      expect(resolveWorkspacePath(path)?.pattern, `${path} 命中自身`).toBe(path)
    }
  })

  it('子路由归属：深链归入正确工作区/分组，/instances/new 不被 :id 抢走', () => {
    const table: [path: string, workspace: WorkspaceKey, groupKey: string, pattern: string][] = [
      ['/instances/new', 'ops', 'opsInstances', '/instances/new'],
      ['/instances/srv-0001', 'ops', 'opsInstances', '/instances/:id'],
      ['/instances/srv-0001/files', 'ops', 'opsInstances', '/instances/:id/files'],
      ['/bots/sessions/run-9', 'operation', 'opPlayersLoad', '/bots/sessions/:id'],
      ['/client-channels/123/publish', 'operation', 'opClientDistribution', '/client-channels/:id/publish'],
    ]

    for (const [path, workspace, groupKey, pattern] of table) {
      const resolved = resolveWorkspacePath(path)
      expect(workspaceOfPath(path), `${path} 的工作区`).toBe(workspace)
      expect(resolved?.groupKey, `${path} 的分组`).toBe(groupKey)
      expect(resolved?.pattern, `${path} 命中的登记项`).toBe(pattern)
    }

    // 静态段优先于动态段：`new` 是指南页，不是 `:id` 控制台
    expect(resolveWorkspacePath('/instances/new')?.pattern).not.toBe('/instances/:id')
    // 长模式优先于短模式：文件深链不会被 `:id` 吃掉
    expect(resolveWorkspacePath('/instances/srv-0001/files')?.pattern).not.toBe('/instances/:id')
    // 列表页本身仍归「资源」，与实例深链分属不同组
    expect(resolveWorkspacePath('/instances')?.groupKey).toBe('opsResources')
    expect(resolveWorkspacePath('/networks/topology')?.groupKey).toBe('opsGroupsWorkbench')
    expect(resolveWorkspacePath('/networks')?.groupKey).toBe('opsGroupsWorkbench')
  })

  it('路径归一化：query/hash、尾斜杠、重复斜杠、缺前导斜杠', () => {
    expect(workspaceOfPath('/instances/')).toBe('ops')
    expect(workspaceOfPath('/monitor?scope=cluster')).toBe('observability')
    expect(workspaceOfPath('/monitor#trend')).toBe('observability')
    expect(workspaceOfPath('//logs')).toBe('observability')
    expect(workspaceOfPath('logs')).toBe('observability')
    expect(workspaceOfPath('/instances/srv-1/files?tab=edit')).toBe('ops')
    // 未知子路径回落到最近的段对齐祖先
    expect(workspaceOfPath('/monitor/history')).toBe('observability')
  })

  it('认证页独立：/login、/setup、/invite 不属于任何工作区且不参与匹配', () => {
    expect(AUTH_ROUTE_PATHS).toEqual(['/login', '/setup', '/invite'])

    const registered = new Set(NEW_ENTRIES.map((e) => e.to))
    for (const auth of AUTH_ROUTE_PATHS) {
      expect(registered.has(auth), `${auth} 被塞进了工作区`).toBe(false)
      expect(NAV_PATH_SET.has(auth), `${auth} 不应在旧导航目的地里`).toBe(false)
      for (const ws of WORKSPACES) {
        expect(ws.groups.some((g) => (g.children ?? []).some((c) => c.to === auth))).toBe(false)
      }
      expect(workspaceOfPath(auth), `${auth} 不应被认作某个工作区`).toBeNull()
    }
  })

  it('未命中路径返回 null（含段边界与未知路由）', () => {
    const misses = ['/unknown', '/databases', '/instance', '/client-dist', '/log', '/dashboard', '/login']
    for (const path of misses) expect(workspaceOfPath(path), `${path} 不应命中`).toBeNull()
    // 根路径只在精确命中时归位，不会吞掉所有路径
    expect(workspaceOfPath('/')).toBe('ops')
    expect(resolveWorkspacePath('/')?.pattern).toBe('/')
  })

  it('角色种子等价：新旧两条角色路径的可见面一致', () => {
    const roles: (number | null)[] = [
      ROLE_MEMBER,
      ...Object.keys(DEFAULT_ROLE_NODES).map(Number),
      ROLE_PLATFORM_ADMIN,
      -1,
      99,
      null,
    ]

    for (const role of roles) {
      const oldPaths = flatNavItems(role).map((i) => i.to)
      const newPaths = flatWorkspaceItems(role).map((i) => i.to)
      const oldSet = new Set(oldPaths)
      const newSet = new Set(newPaths)

      for (const to of oldPaths) expect(newSet.has(to), `role=${role}：新层丢了 ${to}`).toBe(true)
      expect(new Set(newPaths.filter((p) => NAV_PATH_SET.has(p))), `role=${role}：导航目的地集合不一致`).toEqual(oldSet)
      for (const to of newPaths.filter((p) => !NAV_PATH_SET.has(p))) {
        expect(SUB_ROUTE_SET.has(to), `role=${role}：未登记的新增项 ${to}`).toBe(true)
        expect(oldSet.has(SUB_ROUTE_PARENT[to]), `role=${role}：入口页不可见却漏出 ${to}`).toBe(true)
      }
    }

    // 未知角色 / null 保守清零；平台管理员全开
    for (const role of [-1, 99, null] as const) {
      expect(flatWorkspaceItems(role).map((i) => i.to), `role=${role} 应保守清零`).toEqual(['/'])
      expect(workspacesForRole(role).map((w) => w.key)).toEqual(['ops'])
    }
    expect(flatWorkspaceItems(ROLE_PLATFORM_ADMIN).map((i) => i.to)).toEqual(NEW_ALL_PATHS)
    expect(workspacesForRole(ROLE_PLATFORM_ADMIN).map((w) => w.key)).toEqual([
      'ops',
      'observability',
      'operation',
      'platform',
    ])
  })

  it('workspacesForRole / workspacesForPermissions 与扁平化出口一致', () => {
    for (const role of [ROLE_MEMBER, ROLE_PLATFORM_ADMIN, null] as const) {
      const flattened = workspacesForRole(role).flatMap((w) =>
        collectEntries(w.groups).map((e) => ({ to: e.to, labelKey: e.labelKey })),
      )
      expect(flattened).toEqual(flatWorkspaceItems(role))
    }
    for (const nodes of [new Set<string>(), new Set(['instance.read']), adminNodes]) {
      const flattened = workspacesForPermissions(nodes, false).flatMap((w) =>
        collectEntries(w.groups).map((e) => ({ to: e.to, labelKey: e.labelKey })),
      )
      expect(flattened).toEqual(flatWorkspaceItems(nodes, false))
    }
    // 裁剪后的工作区不保留空分组
    for (const nodes of [new Set<string>(), new Set(['node.read']), adminNodes]) {
      for (const ws of workspacesForPermissions(nodes, false)) {
        expect(ws.groups.length).toBeGreaterThan(0)
      }
    }
  })

  it('i18n 债务可追溯：新增键登记在册，语言包既有键直接复用', () => {
    const used = allUsedLabelKeys()
    const pending = new Set(WORKSPACE_I18N_KEYS_TO_ADD)

    // 登记清单不重复、不夹带未使用的键（清单只增不减时这条会红）
    expect(pending.size).toBe(WORKSPACE_I18N_KEYS_TO_ADD.length)
    for (const key of WORKSPACE_I18N_KEYS_TO_ADD) {
      expect(used, `${key} 已登记但本层并未使用`).toContain(key)
    }

    // 每个 labelKey 要么语言包已有，要么登记在待补清单——不允许「黑户」键
    for (const key of used) {
      expect(ZH_KEYS.has(key) || pending.has(key), `${key} 既不在 zh.json 也未登记待补`).toBe(true)
    }

    // 复用面：绝大多数 labelKey 直接沿用旧键，新增面只在工作区名/新组名/子路由名
    expect(used.filter((k) => ZH_KEYS.has(k)).length).toBeGreaterThanOrEqual(30)
  })
})
