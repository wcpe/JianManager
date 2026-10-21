/**
 * @file 侧栏资源面板的纯分组逻辑（FR-509/510）。
 *
 * 从 `SidebarResourcePanel.tsx` 抽出：该文件只应导出组件（`react-refresh/only-export-components`），
 * 而 `buildSections` 是需要被测试直接调用的纯函数，留在组件文件里会让 Fast Refresh 失效。
 *
 * 本地自定义分组（原 `custom` 模式）已删除：换机即失且与后端分组树（`instance_group_nodes`，
 * ADR-033）语义重复。此处的分组归属**只读不写**，一切组织归以分组树为准。
 */
import { envOf, freeTagsOf } from '@/lib/instances/instance-grouping'
import { statusDotKind } from '@/lib/instances/instance-status'
import type { InstanceGroupNode } from '@/lib/instances/instance-group'
import type { InstanceInfo } from '@/lib/instances/instance-types'
import type { StorableInstance, StoredInstance } from '@/lib/console/server-selection'

/** 行模型：已加载实例（全量信息）或收藏快照（未加载时的兜底）。 */
export type PanelRow = { kind: 'instance'; inst: InstanceInfo } | { kind: 'snapshot'; snap: StoredInstance }

/**
 * 分组维度（下拉项，持久化到 `jm.sidebar.groupMode`）。
 *
 * 【为什么没有 `pinned` / `name` / `created` 了】这三项本质是**排序**不是分组：
 * - `pinned` 已提升为列表上方**独立的置顶区**（可拖拽放入），不再是一种分组方式；
 * - `name` / `created` 是把全部行排个序，分组结果都只有一段——挪进 {@link SortMode}。
 * 剩下三项才是真正「把服务器切成几段」的维度。
 */
export type GroupMode = 'node' | 'groupTree' | 'network' | 'tag'

export const GROUP_MODES: GroupMode[] = ['node', 'groupTree', 'network', 'tag']

/** 默认分组方式：按节点（运维看机器归属最常用）。 */
export const DEFAULT_GROUP_MODE: GroupMode = 'node'

/**
 * 排序维度（与分组正交，持久化到 `jm.sidebar.sortMode`）。
 *
 * 与分组分开之后，「按节点分组 + 按名称排序」这类组合才表达得出来——此前它们挤在同一个
 * 下拉里，选了按名称就等于放弃分组。
 */
export type SortMode = 'name' | 'created' | 'status'

export const SORT_MODES: SortMode[] = ['name', 'created', 'status']

/** 默认排序：名称升序（与既有行为一致）。 */
export const DEFAULT_SORT_MODE: SortMode = 'name'

/** 段内行比较器：分组决定「分到哪段」，排序决定「段内怎么排」。 */
function comparatorOf(sort: SortMode): (a: PanelRow, b: PanelRow) => number {
  if (sort === 'created') {
    // 新→旧：创建时间可能缺失（快照行），缺的排最后。
    return (a, b) => {
      const av = a.kind === 'instance' ? a.inst.createdAt : ''
      const bv = b.kind === 'instance' ? b.inst.createdAt : ''
      if (av === bv) return byName(a, b)
      if (!av) return 1
      if (!bv) return -1
      return av < bv ? 1 : -1
    }
  }
  if (sort === 'status') {
    // 异常优先：运维扫侧栏时想先看到出问题的（与状态筛选的三分类同源判定）。
    const weight = (row: PanelRow): number => {
      const kind = statusDotKind(rowStatus(row))
      if (kind === 'crashed') return 0
      if (kind === 'transitioning') return 1
      if (kind === 'running') return 2
      return 3
    }
    return (a, b) => weight(a) - weight(b) || byName(a, b)
  }
  return byName
}

/** 列表的一段（组头 + 行）。 */
export interface PanelSection {
  key: string
  label: string
  rows: PanelRow[]
}

export const rowId = (row: PanelRow): number => (row.kind === 'instance' ? row.inst.id : row.snap.id)
export const rowName = (row: PanelRow): string => (row.kind === 'instance' ? row.inst.name : row.snap.name)
export const rowStatus = (row: PanelRow): string => (row.kind === 'instance' ? row.inst.status : row.snap.status)
export const rowNodeId = (row: PanelRow): number => (row.kind === 'instance' ? row.inst.nodeId : row.snap.nodeId)
export const toStorable = (row: PanelRow): StorableInstance => (row.kind === 'instance' ? row.inst : row.snap)

/** 一行名称比较（分组内排序统一走它；中文环境按本地排序规则）。 */
function byName(a: PanelRow, b: PanelRow): number {
  return rowName(a).localeCompare(rowName(b))
}

/**
 * 按分组方式切段（纯函数，便于针对性测试）：
 * - pinned：收藏顺序的单段（未加载的收藏回落快照行）；
 * - node / network / tag：按对应键分组，未命中归「未分组」（置尾）。
 *
 * 「段内怎么排」由 `sort`（{@link SortMode}）单独决定，与分组正交；
 * 「置顶」不再是分组维度，而是列表上方独立的一块区（见 `SidebarResourcePanel`）。
 */
export function buildSections(args: {
  rows: PanelRow[]
  mode: GroupMode
  nodeLabelOf: Map<number, string>
  nodeOrder: Map<number, number>
  /** 实例 id → 群组名数组：一台实例可命中多个群组，侧栏一行一台故取首个键。 */
  networkKeyOf: Map<number, string[]>
  /** 段内排序（与分组正交）。缺省按名称，与拆分前的行为一致。 */
  sort?: SortMode
  labels: { ungrouped: string }
}): PanelSection[] {
  const { rows, mode, nodeLabelOf, nodeOrder, networkKeyOf, labels, sort = DEFAULT_SORT_MODE } = args
  const compare = comparatorOf(sort)

  // node / network / tag：单级分组，未命中归「未分组」并置尾。
  const keyOf = (row: PanelRow): string => {
    if (mode === 'node') return nodeLabelOf.get(rowNodeId(row)) ?? `#${rowNodeId(row)}`
    const inst = row.kind === 'instance' ? row.inst : undefined
    if (!inst) return labels.ungrouped
    // 多群组归属取首个键（侧栏一行一台，不合并成一串）；无归属归「未分组」。
    if (mode === 'network') return networkKeyOf.get(inst.id)?.[0] ?? labels.ungrouped
    return envOf(inst) || freeTagsOf(inst)[0] || labels.ungrouped
  }

  const buckets = new Map<string, PanelRow[]>()
  for (const row of rows) {
    const key = keyOf(row)
    const list = buckets.get(key)
    if (list) list.push(row)
    else buckets.set(key, [row])
  }

  const entries = [...buckets.entries()]
  if (mode === 'node') {
    // 节点段按节点列表顺序（离线节点也有稳定位置），未知节点按 id 置尾。
    entries.sort((a, b) => {
      const ia = nodeOrder.get(Number(a[0].replace(/^#/, ''))) ?? Number.MAX_SAFE_INTEGER
      const ib = nodeOrder.get(Number(b[0].replace(/^#/, ''))) ?? Number.MAX_SAFE_INTEGER
      return ia - ib
    })
  } else {
    entries.sort((a, b) => (a[0] === labels.ungrouped ? 1 : b[0] === labels.ungrouped ? -1 : a[0].localeCompare(b[0])))
  }
  return entries.map(([key, list]) => ({ key: `${mode}:${key}`, label: key, rows: list.sort(compare) }))
}

/** 分组树的一行：组头（含层级与计数）或组内实例。 */
export type GroupTreeLine =
  | { kind: 'group'; key: string; node: InstanceGroupNode; depth: number; count: number }
  | { kind: 'instance'; key: string; node: InstanceGroupNode; row: PanelRow }

/**
 * 「按分组」维度：把窗口实例按后端分组树组织成**文件树**（FR-509 修订）。
 *
 * 【为什么是树而不是段】分组是树形归类（集群 → 大区 → 服组），平铺成同级段会把层级压平、
 * 读起来像一堆并列标签。这里输出带 `depth` 的行序列，由视图渲染缩进与展开箭头。
 *
 * 【M:N】同一实例可属多个组，因此会在每个所属组下**各出现一次**——这是分组语义不是重复。
 * 子组只展示自己的直接成员（成员归属不向上重复展示），避免同一实例在整条祖先链上刷屏；
 * 组头计数用子树去重数，与 `instanceCount` 口径一致。
 */
export function buildGroupTreeLines(args: {
  rows: PanelRow[]
  groupTree: InstanceGroupNode[]
}): GroupTreeLine[] {
  const { rows, groupTree } = args
  if (groupTree.length === 0) return []

  const childrenOf = new Map<number | null, InstanceGroupNode[]>()
  for (const node of groupTree) {
    const list = childrenOf.get(node.parentId)
    if (list) list.push(node)
    else childrenOf.set(node.parentId, [node])
  }
  for (const list of childrenOf.values()) list.sort((a, b) => a.sort - b.sort || a.id - b.id)

  const rowByInstanceId = new Map<number, PanelRow>()
  for (const row of rows) rowByInstanceId.set(rowId(row), row)

  /** 子树去重实例数（只算落在当前窗口里的，避免出现「计数 30 但一行都没有」）。 */
  const subtreeCount = (node: InstanceGroupNode): number => {
    const ids = new Set<number>()
    const stack: InstanceGroupNode[] = [node]
    const seen = new Set<number>()
    while (stack.length > 0) {
      const current = stack.pop() as InstanceGroupNode
      if (seen.has(current.id)) continue
      seen.add(current.id)
      for (const id of current.memberInstanceIds ?? []) if (rowByInstanceId.has(id)) ids.add(id)
      stack.push(...(childrenOf.get(current.id) ?? []))
    }
    return ids.size
  }

  const out: GroupTreeLine[] = []
  const walk = (parentId: number | null, depth: number) => {
    for (const node of childrenOf.get(parentId) ?? []) {
      const count = subtreeCount(node)
      // 空组整枝不渲染：没有成员的组头只是噪音（用户可去分组管理里看全量结构）。
      const hasChildWithRows = (childrenOf.get(node.id) ?? []).some((child) => subtreeCount(child) > 0)
      if (count === 0 && !hasChildWithRows) continue
      out.push({ kind: 'group', key: `g:${node.id}`, node, depth, count })
      // 直接成员按名称序；子组只渲染自己的直接成员，不重复祖先的。
      for (const id of (node.memberInstanceIds ?? []).slice().sort((a, b) => a - b)) {
        const row = rowByInstanceId.get(id)
        if (row) out.push({ kind: 'instance', key: `gi:${node.id}:${id}`, node, row })
      }
      walk(node.id, depth + 1)
    }
  }
  walk(null, 0)
  return out
}

/**
 * 侧栏列表的**虚拟化行高**（px）。必须是真实渲染高度的**严格倍数**：
 * - 服务器行 32（两行：名称 14px + 备注 12px + 上下内边距）；
 * - 组头 26（单行小字）。
 *
 * 【为什么写在这里而不是组件里】行高一旦和 CSS 不一致，窗口算出来的位置就会与实际渲染
 * 逐行累积漂移（千行时可达数千像素，滚动到底部会看到空白）。护栏要求「itemSize 与真实
 * 渲染高度严格一致」，因此把常量与消费它的 CSS 值放在同一个可测真源里，
 * `sidebar-sections` 的单测会断言 `ROW_HEIGHT` 与 `sidebarRowHeight()` 一一对应。
 */
export const SERVER_ROW_HEIGHT = 32
export const GROUP_HEAD_HEIGHT = 26

/**
 * 把「分段（sections）」或「文件树（tree）」两种渲染形态铺平成**一条可虚拟化的行序列**。
 *
 * 两种形态此前各写一遍 `map`，虚拟化后如果继续分叉就会有两套窗口计算——合并成扁平行模型后，
 * 窗口只算一次，渲染时按 `kind` 分派即可（组头/服务器行高度不同，走变高模式 `virtualWindowVaried`）。
 *
 * `mode === 'groupTree'` 走文件树分支，其余走分段分支；两者互斥（`treeLines` 在其它维度为空）。
 */
export type SidebarFlatLine =
  | { kind: 'section-head'; key: string; label: string; count: number }
  | { kind: 'group-branch'; key: string; label: string; depth: number; count: number }
  | { kind: 'server'; key: string; row: PanelRow; indent: number }

export function flattenSidebarLines(args: {
  mode: GroupMode
  sections: PanelSection[]
  treeLines: GroupTreeLine[]
}): SidebarFlatLine[] {
  const { mode, sections, treeLines } = args
  if (mode === 'groupTree') {
    return treeLines.map((line): SidebarFlatLine =>
      line.kind === 'group'
        ? { kind: 'group-branch', key: line.key, label: line.node.name, depth: line.depth, count: line.count }
        // 树里的实例缩进两层：层号 ∝ 节点深度。根组下的实例缩进 1，子组下缩进 2，
        // 与组头缩进（depth 直接取）形成稳定阶梯。
        : { kind: 'server', key: line.key, row: line.row, indent: line.node.parentId === null ? 1 : 2 },
    )
  }
  const out: SidebarFlatLine[] = []
  for (const section of sections) {
    if (section.label !== '') {
      out.push({ kind: 'section-head', key: `${section.key}:head`, label: section.label, count: section.rows.length })
    }
    for (const row of section.rows) {
      out.push({ kind: 'server', key: `${section.key}:${row.kind}:${rowId(row)}`, row, indent: 0 })
    }
  }
  return out
}
