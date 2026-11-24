import { describe, expect, it } from 'vitest'
import type { LucideIcon } from 'lucide-react'

import { AUTH_ROUTE_PATHS, SUB_ROUTE_PATTERNS, WORKSPACES, WORKSPACE_I18N_KEYS_TO_ADD, flatWorkspaceItems, resolveWorkspacePath, workspaceOfPath, workspacesForPermissions, workspacesForRole } from '@/lib/console/workspace-navigation'
import type { WorkspaceKey } from '@/lib/console/workspace-navigation'
import type { NavGroup } from '@/lib/shared/navigation-types'
import { ALL_PERMISSION_NODE_IDS, DEFAULT_ROLE_NODES, ROLE_MEMBER, ROLE_PLATFORM_ADMIN } from '@/lib/shared/roles'
import zh from '@/i18n/zh.json'
import navSnapshot from './__fixtures_nav-snapshot.json'

/** 
 *阶段 5 的工作区导航数据层测试。
 * 核心主张只有一条：**换结构不换权限语义**。
 *
 * 【为何用快照而不是继续引用 nav-config.ts（阶段 6）】
 * 阶段 6 删掉了旧六域真源 `nav-config.ts`，但「新层与旧层逐条等价」这个主张必须继续被验证，
 * 否则下次有人改 WORKSPACES时，没有任何东西能拦住他悄悄删掉/改权限。
 * 因此把旧六域当年登记的全部 41 条**冻结成快照** `__fixtures_nav-snapshot.json`：
 * 它是历史事实，不随代码变动，继续当参照物。快照的生成口径见文件末尾注释。
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

const OLD_ENTRIES = navSnapshot as unknown as CollectedEntry[]
const OLD_ENTRY_BY_PATH = new Map(OLD_ENTRIES.map((e) => [e.to, e]))
/** 
 * 旧六域**登记**的全部导航目的地：41 条。
 * 数据源已从 `NAV_GROUPS` 改为冻结快照（阶段 6 删了旧真源），条数作为不变量继续断言：
 * 快照少一条就说明它被误改，而快照正是「不得漏掉旧入口」的唯一证据。
 */
const OLD_ALL_PATHS = OLD_ENTRIES.map((e) => e.to)
const NAV_PATH_SET = new Set(OLD_ALL_PATHS)

/** 
 * 快照冻结**之后**新增的导航目的地（必须逐条登记在这里）。
 *
 * 【为什么需要这份清单】`__fixtures_nav-snapshot.json` 是「旧六域当年登记了什么」的历史事实，
 * 不得手改；但产品会继续加页面。若不允许登记新入口，要么只能改快照（等于篡改历史、
 * 「零遗漏」这条主张随之失效），要么新页面永远进不了导航。所以新入口另立一份显式清单：
 * 加页面必须同时改这里，快照保持只读，等价性断言仍然只对旧 41 条生效。
 *
 * 登记项需自带 perm 与 labelKey，供下方「不得凭空发明权限节点」与「i18n 键必须存在」两条断言消费。
 */
const POST_FREEZE_NAV_ENTRIES: CollectedEntry[] = [
  // 探针托管：管的是探针进程自己的 config.yml（66 键定档表），
  // 与「配置文件在实例磁盘上的形态」是两套对象。
  //
  // 原先这里是两条独立入口（/managed-configs 配置、/managed-matrix 对账），
  // 一级路由此膨胀；现已收敛为单页 3 Tab 的两级路由 /probe/managed
  //（配置与下发 / 对账矩阵 / 下发历史）。旧路径保留为路由重定向，不再进导航。
  { to: '/probe/managed', labelKey: 'nav.probeManaged', perm: 'instance.read' },
]
/** 冻结后新增的导航目的地路径集合。 */
const POST_FREEZE_NAV_PATHS = new Set(POST_FREEZE_NAV_ENTRIES.map((e) => e.to))

const WORKSPACE_GROUP_LIST = WORKSPACES.flatMap((w) => w.groups)
const NEW_ENTRIES = collectEntries(WORKSPACE_GROUP_LIST)
const NEW_ENTRY_BY_PATH = new Map(NEW_ENTRIES.map((e) => [e.to, e]))
/** 新工作区层（管理员全开）的全部 `to`：41 条导航目的地 + 5 条子路由深链。 */
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

/** 
 * 上游工作区层定稿之后**新增**的导航目的地（不属于旧六域 41 条快照）：
 * 它们是本层自己的登记项，与子路由深链分开记账——子路由必须能追到旧入口页 inherit perm，
 * 而新增目的地自带 perm 语义（`/my-tokens` 无 perm，所有登录用户可见）。
 */
const ADDED_NAV_PATHS = new Set<string>(['/my-tokens'])

/** 
 * 新层里「非旧快照、也非子路由」的合法新增项。
 *
 * 由两份显式清单合成：
 *   1. {@link POST_FREEZE_NAV_ENTRIES}——声明了 perm 的新入口（/probe/managed）；
 *   2. {@link ADDED_NAV_PATHS}——无 perm 的自助入口（/my-tokens）。
 * 两者都不是子路由，也都不参与「继承旧入口页 perm」那套判定，故只在
 * 「可见性校验」这一处合并消费；permission 语义仍各按各的清单判
 *（/probe/managed 需要 instance.read，/my-tokens 无 perm 对所有人开放）。
 */
const DECLARED_NEW_NAV_SET = new Set([...POST_FREEZE_NAV_PATHS, ...ADDED_NAV_PATHS])

/**
 * lucide 上游改名兼容表：同一图标组件在新版本换了 displayName（旧名保留为别名导出）。
 * 快照存的是改名前的 displayName，对比前把新名归一回历史名，避免依赖升级造成假阳性。
 * 新增条目时写明「新 displayName → 快照里的旧名」。
 */
const LUCIDE_RENAMED_DISPLAY_NAMES: Record<string, string> = {
  // lucide ≥1.x 把 triangle-alert 改名为 triangle-exclamation-point，
  // TriangleAlert 仍作为同一组件的别名导出，displayName 随之变为新名。
  TriangleExclamationPoint: 'TriangleAlert',
}

/**
 * icon 归一：新层是 lucide 组件引用，快照里存的是 `displayName` 字符串。
 * 两个方向都要处理（字符串直回，组件取 displayName），对比前归一到同一字符串。
 */
function iconName(icon: CollectedEntry['icon']): string | undefined {
  if (icon === undefined || icon === null) return undefined
  if (typeof icon === 'string') return icon
  const c = icon as unknown as { displayName?: string; name?: string }
  const name = c.displayName ?? c.name
  return name === undefined ? undefined : (LUCIDE_RENAMED_DISPLAY_NAMES[name] ?? name)
}

/** 
 * perm 归一：快照是 JSON，`undefined` 会被序列化成 `null`；而新层里「无 perm」是 `undefined`。
 * 两者语义相同（无权限要求），对比前必须归一到同一形态，否则会报假阳性。
 */
function permOf(entry: CollectedEntry | undefined): string | string[] | undefined {
  const p = entry?.perm
  return p === null ? undefined : p
}

const adminNodes = new Set(ALL_PERMISSION_NODE_IDS)

/** 
 * 旧六域时代的权限裁剪语义（从快照重算，不依赖已删除的 nav-config.ts）。
 *
 * 这就是当初 `flatNavItems` 的行为：无 perm 入口恒可见；单节点看是否命中；数组是 any-of。
 * 管理员短路全开。保留它才能继续证明「换结构不换权限语义」——不会变成拿新层自证新层。
 */
function oldFlatPaths(nodesOrRole: Set<string> | number | null, admin = false): string[] {
  // 角色包装分支（与旧 navGroupsForRole 同语义）：管理员全开，未知/空种子保守清零。
  if (typeof nodesOrRole === 'number' || nodesOrRole === null) {
    if (isPlatformAdminRole(nodesOrRole)) return OLD_ALL_PATHS.slice()
    const seed = DEFAULT_ROLE_NODES[nodesOrRole ?? -1] ?? []
    return oldFlatPaths(new Set(seed), false)
  }

  const granted = nodesOrRole
  const permitted = (perm: CollectedEntry['perm']): boolean => {
    if (admin) return true
    if (perm === undefined || perm === null) return true
    const list = Array.isArray(perm) ? perm : [perm]
    return list.some((p) => granted.has(p))
  }

  return OLD_ENTRIES.filter((e) => permitted(e.perm)).map((e) => e.to)
}

/** 旧 `isPlatformAdmin` 的角色值判定（快照测试不 import 业务函数以免假阳性）。 */
function isPlatformAdminRole(role: number | null): boolean {
  return role === ROLE_PLATFORM_ADMIN
}

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
 * 3) 多出来的只能是「登记过的子路由」或「冻结后登记的新导航目的地」（两者都显式清单化）。
 *
 * 第 3 条的口径变化：旧版本只允许子路由，因为那时导航表完全等于 CSV 的 41 条。
 * 冻结后新增页面（探针托管配置 / 对账矩阵）走 {@link POST_FREEZE_NAV_ENTRIES} 显式登记，
 * 快照本身保持只读——否则「零遗漏」的参照物就被改动了。
 */
function assertPermissionEquivalence(nodes: Set<string>, admin: boolean, label: string) {
  const oldPaths = oldFlatPaths(nodes, admin)
  const newPaths = flatWorkspaceItems(nodes, admin).map((i) => i.to)
  const oldSet = new Set(oldPaths)
  const newSet = new Set(newPaths)

  for (const to of oldPaths) expect(newSet.has(to), `${label}：新层丢了旧导航目的地 ${to}`).toBe(true)

  const newNavOnly = newPaths.filter((to) => NAV_PATH_SET.has(to))
  expect(new Set(newNavOnly), `${label}：导航目的地的可见性集合不一致`).toEqual(oldSet)

  const extras = newPaths.filter((to) => !NAV_PATH_SET.has(to))

  for (const to of extras) {
    // 冻结后新增的页面分两类记账：
    //   1. 「无 perm 自助入口」（如 /my-tokens）——所有登录用户可见，不参与权限判定；
    //   2. 「声明了 perm 的新入口」（如 /probe/managed）——可见性必须跟随其声明的 perm。
    // 两类都在 DECLARED_NEW_NAV_SET 里，故统一走这里的 perm 校验：
    // perm 为 undefined 时上式直接为真，等价于第 1 类。
    if (DECLARED_NEW_NAV_SET.has(to)) {
      const declared = POST_FREEZE_NAV_ENTRIES.find((e) => e.to === to)
      if (declared) {
        const perm = declared.perm
        const permitted =
          admin || perm === undefined || (Array.isArray(perm) ? perm : [perm]).some((p) => nodes.has(p))
        // 断言方向取「可见 ⇒ 有权限」：不可见可能是其它原因（如该工作区被整体裁掉），不算违规。
        expect(permitted || !newSet.has(to), `${label}：${to} 无对应权限却可见`).toBe(true)
      }
      // 无 perm 的自助入口：与角色无关，可见性不该因权限集合变化。
      continue
    }
    expect(SUB_ROUTE_SET.has(to), `${label}：出现未登记的新增项 ${to}`).toBe(true)
    const parent = SUB_ROUTE_PARENT[to]
    expect(parent, `${label}：${to} 缺入口页登记`).toBeDefined()
    expect(oldSet.has(parent), `${label}：入口页 ${parent} 不可见却漏出深链 ${to}`).toBe(true)
  }
}

describe('workspace-navigation：四工作区 × 组 × 页面（阶段 5）', () => {
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
            // 末尾两项是冻结后新增（见 POST_FREEZE_NAV_ENTRIES）：探针托管配置与对账矩阵
            // 与既有配置页同组——运维找「配置」时不必跨工作区。
            pages: [
              '/tasks',
              '/schedules',
              '/backups',
              '/config-baselines',
              '/config-files',
              '/config-adopt',
              '/config-deliveries',
              '/probe/managed',
            ],
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
          { key: 'platformIdentity', labelKey: 'nav.identityAccess', pages: ['/users', '/groups', '/permissions', '/my-tokens'] },
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
            pages: ['/approvals', '/audit', '/settings', '/database', '/system-update', '/licenses'],
          },
        ],
      },
    ])
  })

  it('登记条数对账 CSV：46 条进工作区 + 3 条认证页 = 49；三层结构无 sections', () => {
    // 三层结构：工作区 → 组 → 页面；组是容器（无自身 to、无二级 sections）
    expect(WORKSPACE_GROUP_LIST.every((g) => g.to === undefined)).toBe(true)
    expect(WORKSPACE_GROUP_LIST.every((g) => g.sections === undefined)).toBe(true)
    expect(WORKSPACE_GROUP_LIST.every((g) => (g.children?.length ?? 0) > 0)).toBe(true)

    // 工作区 → 组 → 页面数（对齐 CSV 的 3/3/3、3/3/1、2/1、5 分类；
    // observability 的 13 里含冻结后新增的 1 条（探针托管总控），见 POST_FREEZE_NAV_ENTRIES）
    expect(
      WORKSPACES.map((w) => [w.key, w.groups.length, w.groups.reduce((n, g) => n + (g.children?.length ?? 0), 0)]),
    ).toEqual([
      ['ops', 3, 10],
      ['observability', 3, 13],
      ['operation', 2, 6],
      ['platform', 5, 19],
    ])

    const allPaths = NEW_ENTRIES.map((e) => e.to)
    expect(new Set(allPaths).size, '工作区层存在重复登记的路径').toBe(allPaths.length)

    const carriedNavPaths = allPaths.filter((p) => NAV_PATH_SET.has(p))
    const addedNavPaths = allPaths.filter((p) => ADDED_NAV_PATHS.has(p))
    const postFreezePaths = allPaths.filter((p) => POST_FREEZE_NAV_PATHS.has(p))
    const subPaths = allPaths.filter((p) => SUB_ROUTE_SET.has(p))
    expect(carriedNavPaths.length, '41 个旧导航目的地').toBe(41)
    expect(new Set(addedNavPaths), '无 perm 自助入口全部登记').toEqual(ADDED_NAV_PATHS)
    expect(new Set(postFreezePaths), '冻结后新增项全部登记').toEqual(POST_FREEZE_NAV_PATHS)
    expect(new Set(subPaths), '5 条子路由全部登记').toEqual(SUB_ROUTE_SET)
    expect(
      carriedNavPaths.length + addedNavPaths.length + postFreezePaths.length + subPaths.length,
      '四类路径互不相交且覆盖全部登记项',
    ).toBe(allPaths.length)

    // 51 = 43 导航（41 旧 + 1 自助入口 + 1 冻结后新增） + 5 子路由 + 3 认证页
    expect(allPaths.length + AUTH_ROUTE_PATHS.length).toBe(51)

    // 组 key 全局唯一（折叠态 / 图标映射按 key 记账）
    const groupKeys = WORKSPACE_GROUP_LIST.map((g) => g.key)
    expect(new Set(groupKeys).size).toBe(groupKeys.length)

    // 每条子路由的入口页都在旧导航里登记过（perm 来源可追溯）
    for (const pattern of SUB_ROUTE_PATTERNS) {
      expect(NAV_PATH_SET.has(SUB_ROUTE_PARENT[pattern]), `${pattern} 的入口页不在旧导航中`).toBe(true)
    }
    // 新增目的地不属于旧快照，必须显式登记而不是混进子路由集合
    for (const to of ADDED_NAV_PATHS) {
      expect(addedNavPaths.includes(to), `${to} 应在导航目的地中`).toBe(true)
      expect(SUB_ROUTE_SET.has(to), `${to} 不应被当作子路由`).toBe(false)
    }
  })

  it('perm / labelKey / icon 与旧六域快照同路径项逐条同值（零发明、零省略）', () => {
    expect(OLD_ALL_PATHS.length, '旧导航目的地应为 41 条').toBe(41)
    expect(OLD_ENTRY_BY_PATH.size).toBe(41)
    // 交叉核对参照物口径：管理员全开 = 登记全集（顺序也一致）
    expect(oldFlatPaths(adminNodes, true)).toEqual(OLD_ALL_PATHS)

    for (const to of OLD_ALL_PATHS) {
      const oldEntry = OLD_ENTRY_BY_PATH.get(to)
      const newEntry = NEW_ENTRY_BY_PATH.get(to)
      expect(newEntry, `${to} 未在工作区层登记`).toBeDefined()
      expect(permOf(newEntry), `${to} 的 perm 与旧六域不一致`).toEqual(permOf(oldEntry))
      expect(newEntry?.labelKey, `${to} 的 labelKey 与旧六域不一致`).toBe(oldEntry?.labelKey)
      expect(iconName(newEntry?.icon), `${to} 的 icon 与旧六域不一致`).toBe(iconName(oldEntry?.icon))
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

  it('路由零遗漏：新层包含旧导航全部 41 条，其余恰为 5 条子路由 + 2 条新增导航', () => {
    // 管理员全开：旧层 41 条导航目的地；新层 = 41 + 5 子路由 + 2 新增导航
    //（/my-tokens 无 perm 自助入口 + /probe/managed 冻结后新增）
    const oldAdminPaths = oldFlatPaths(adminNodes, true)
    expect(oldAdminPaths, '旧层管理员口径应为 41 条').toEqual(OLD_ALL_PATHS)
    expect(flatWorkspaceItems(adminNodes, true).map((i) => i.to)).toEqual(NEW_ALL_PATHS)

    const newSet = new Set(NEW_ALL_PATHS)
    for (const to of OLD_ALL_PATHS) expect(newSet.has(to), `新层丢失了 ${to}`).toBe(true)

    // 「非旧快照」的部分 = 子路由 ∪ 无 perm 自助入口 ∪ 冻结后新增
    //（三份显式清单，不得有第四种来路）。
    expect(new Set(NEW_ALL_PATHS.filter((p) => !NAV_PATH_SET.has(p)))).toEqual(
      new Set([...SUB_ROUTE_SET, ...ADDED_NAV_PATHS, ...POST_FREEZE_NAV_PATHS]),
    )
    // 旧导航目的地必须一条不落地被新层承接
    expect([...new Set(NEW_ALL_PATHS.filter((p) => NAV_PATH_SET.has(p)))].sort()).toEqual([...OLD_ALL_PATHS].sort())
    // 新增目的地确实落在新层里（两类分别断言，避免一类掩盖另一类）
    for (const to of ADDED_NAV_PATHS) expect(newSet.has(to), `新层缺自助入口 ${to}`).toBe(true)
    for (const to of POST_FREEZE_NAV_PATHS) expect(newSet.has(to), `新层缺冻结后新增 ${to}`).toBe(true)
    expect(newSet.size).toBe(NEW_ALL_PATHS.length)

    // 角色包装下平台管理员是全开（43 导航 + 5 子路由）；null 走角色种子（未知角色保守清零），
    // 与旧 `flatNavItems(null, true)` 的老行为一致——null 不等于「管理员全开」。
    // 旧层 null 只剩首页；新层另有与角色无关的自助入口
    expect(oldFlatPaths(null, true)).toEqual(['/'])
    expect(flatWorkspaceItems(null, true).map((i) => i.to)).toEqual(['/', ...ADDED_NAV_PATHS])
    expect(flatWorkspaceItems(ROLE_PLATFORM_ADMIN).map((i) => i.to)).toEqual(NEW_ALL_PATHS)
  })

  it('权限裁剪等价：空权限只留无 perm 入口', () => {
    // 旧层唯一无 perm 入口是首页；新层另有自助令牌页（个人凭证与角色无关）
    expect(oldFlatPaths(new Set(), false)).toEqual(['/'])
    const permFree = ['/', ...ADDED_NAV_PATHS]
    expect(flatWorkspaceItems(new Set(), false).map((i) => i.to)).toEqual(permFree)
    expect(flatWorkspaceItems(null, false).map((i) => i.to)).toEqual(permFree)

    const empty = workspacesForPermissions(new Set<string>(), false)
    expect(empty.map((w) => w.key)).toEqual(['ops', 'platform'])
    expect(empty[0].groups.map((g) => g.key)).toEqual(['opsResources'])
    // 空权限下不出现任何实例 / 发布 / 会话深链
    for (const pattern of SUB_ROUTE_PATTERNS) {
      expect(flatWorkspaceItems(new Set(), false).map((i) => i.to)).not.toContain(pattern)
    }
  })

  it('权限裁剪等价：{instance.read} 场景逐条对照', () => {
    const nodes = new Set(['instance.read'])
    // 配置三项（config-files / config-adopt / config-deliveries）的 perm 都是
    // any-of `['file.read','instance.read']`，故仅持 instance.read 也应可见。
    expect(oldFlatPaths(nodes, false)).toEqual([
      '/',
      '/instances',
      '/config-baselines',
      '/config-files',
      '/config-adopt',
      '/config-deliveries',
    ])
    expect(flatWorkspaceItems(nodes, false).map((i) => i.to)).toEqual([
      '/',
      '/instances',
      '/instances/new',
      '/instances/:id',
      '/instances/:id/files',
      '/config-baselines',
      '/config-files',
      '/config-adopt',
      '/config-deliveries',
      // 冻结后新增：单页 3 Tab 的两级入口，只要求 instance.read，故可见。
      '/probe/managed',
      // 无 perm 的自助入口对所有登录用户可见，不受上面这组权限影响。
      // 顺序按工作区排列：operation 工作区在 platform 之前。
      '/my-tokens',
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
      const oldPaths = oldFlatPaths(nodesSet, false)
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

  it('工作区归属：41 条导航目的地逐条归位（CSV 对照）', () => {
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
      ['/config-files', 'observability', 'obsAutomation'],
      ['/config-adopt', 'observability', 'obsAutomation'],
      ['/config-deliveries', 'observability', 'obsAutomation'],
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
      ['/approvals', 'platform', 'platformSystemMaintenance'],
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

    // 冻结后新增的两页同样必须归位（否则侧栏高亮与命令面板会指向别的工作区）。
    for (const entry of POST_FREEZE_NAV_ENTRIES) {
      expect(workspaceOfPath(entry.to), `${entry.to} 的工作区`).toBe('observability')
      expect(resolveWorkspacePath(entry.to)?.groupKey, `${entry.to} 的分组`).toBe('obsAutomation')
      expect(resolveWorkspacePath(entry.to)?.pattern, `${entry.to} 命中自身`).toBe(entry.to)
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
      const oldPaths = oldFlatPaths(role)
      const newPaths = flatWorkspaceItems(role).map((i) => i.to)
      const oldSet = new Set(oldPaths)
      const newSet = new Set(newPaths)

      for (const to of oldPaths) expect(newSet.has(to), `role=${role}：新层丢了 ${to}`).toBe(true)
      // 对账口径只含旧六域登记过的目的地：新增目的地无旧层对应物，单独断言不参与等价比较
      const carriedNew = newPaths.filter((p) => NAV_PATH_SET.has(p))
      expect(new Set(carriedNew), `role=${role}：导航目的地集合不一致`).toEqual(oldSet)
      for (const to of newPaths.filter((p) => !NAV_PATH_SET.has(p))) {
        // 冻结后新增的导航目的地不属于子路由体系，不参与「入口页可见」判定：
        //   - 无 perm 自助入口（/my-tokens）对所有登录用户开放，旧层无对应入口可继承；
        //   - 声明了 perm 的新入口（/probe/managed）自带 perm，同样没有旧入口页。
        if (ADDED_NAV_PATHS.has(to) || POST_FREEZE_NAV_PATHS.has(to)) continue
        expect(SUB_ROUTE_SET.has(to), `role=${role}：未登记的新增项 ${to}`).toBe(true)
        expect(oldSet.has(SUB_ROUTE_PARENT[to]), `role=${role}：入口页不可见却漏出 ${to}`).toBe(true)
      }
    }

    // 未知角色 / null 保守清零；平台管理员全开。
    // 例外：无 perm 的新增目的地（`/my-tokens`）本就对所有登录用户开放，
    // 清零只作用于「按权限裁剪」的入口，不应把自助入口一并抹掉。
    const permFreeAdded = [...ADDED_NAV_PATHS]
    for (const role of [-1, 99, null] as const) {
      expect(flatWorkspaceItems(role).map((i) => i.to), `role=${role} 应保守清零`).toEqual(['/', ...permFreeAdded])
      expect(workspacesForRole(role).map((w) => w.key)).toEqual(['ops', 'platform'])
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

/** 
 * 【快照生成口径】`__fixtures_nav-snapshot.json` 里的 41 条，是在删除 `nav-config.ts`
 * 之前用下述代码从 `NAV_GROUPS` 原样导出的（每一组、每个 children / sections 叶子均不遗漏）：
 *
 * ```ts
 * for (const g of NAV_GROUPS) {
 *   if (g.to) push({ to: g.to, labelKey: g.labelKey, icon: g.icon.displayName, perm: g.perm ?? null })
 *   for (const c of g.children ?? []) push({ ...c })
 *   for (const s of g.sections ?? []) for (const c of s.children) push({ ...c })
 * }
 * ```
 *
 * `icon` 存的是组件 `displayName`（快照是 JSON，不能存组件引用），因此下面 icon 对比时
 * 两边都归一到字符串。快照只能由「删旧真源前的 HEAD」重生成，**不得手改**。
 */
