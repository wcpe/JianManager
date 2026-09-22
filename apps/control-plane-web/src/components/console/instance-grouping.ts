import type { InstanceInfo } from '@/api/instances'
import type { InstanceGroupNode } from '@/api/instanceGroups'
import { buildGroupTree, type GroupTreeNode } from './instance-group-tree'
import { resolveCapabilities } from '@/lib/capabilities'

/** 环境维度复用 Tags 字段的约定前缀（FR-047），与后端 model.EnvTagPrefix 一致。 */
export const ENV_TAG_PREFIX = 'env:'

/**
 * 把实例 `tags` 字段规范化为字符串数组。
 * 后端将 JSON 列原样以字符串返回（空标签为 ""，有标签为 `'["env:prod"]'`，清空为 "null"），
 * 与 `envVars`/`launchSpec` 一致；前端在此统一解析，容错任意非数组输入，避免直接 `.filter` 崩溃。
 */
export function parseTags(raw: unknown): string[] {
  if (Array.isArray(raw)) {
    return raw.filter((t): t is string => typeof t === 'string')
  }
  if (typeof raw === 'string' && raw.trim() !== '') {
    try {
      const parsed = JSON.parse(raw)
      if (Array.isArray(parsed)) {
        return parsed.filter((t): t is string => typeof t === 'string')
      }
    } catch {
      // 非 JSON（理论上不会出现），按无标签处理
    }
  }
  return []
}

/**
 * 从实例标签中提取环境维度值（去掉 `env:` 前缀），取首个 env 标签。
 * 无 env 标签时返回空串，表示「未分环境」。
 */
export function envOf(inst: Pick<InstanceInfo, 'tags'>): string {
  for (const tag of parseTags(inst.tags)) {
    if (tag.startsWith(ENV_TAG_PREFIX)) {
      return tag.slice(ENV_TAG_PREFIX.length).trim()
    }
  }
  return ''
}

/** 实例的自由标签（剔除 env: 前缀的环境标签）。 */
export function freeTagsOf(inst: Pick<InstanceInfo, 'tags'>): string[] {
  return parseTags(inst.tags).filter((t) => !t.startsWith(ENV_TAG_PREFIX))
}

/**
 * 收集实例集合中出现过的环境值（去重、字典序），供筛选下拉用。
 * 仅含至少一个实例标注的环境，避免下拉出现空选项。
 */
export function collectEnvs(instances: InstanceInfo[]): string[] {
  const set = new Set<string>()
  for (const inst of instances) {
    const env = envOf(inst)
    if (env) set.add(env)
  }
  return [...set].sort()
}

/** 收集实例集合中出现过的自由标签（去重、字典序），供筛选下拉用。 */
export function collectTags(instances: InstanceInfo[]): string[] {
  const set = new Set<string>()
  for (const inst of instances) {
    for (const tag of freeTagsOf(inst)) set.add(tag)
  }
  return [...set].sort()
}

/** 分组维度。 */
export type GroupDimension =
  | 'none'
  | 'node'
  | 'env'
  | 'status'
  /** 大区/小区两级树（FR-452）：先按 region: 标签，再按 zone: 标签。 */
  | 'region'
  /** 仅按 zone: 标签单级分组。 */
  | 'zone'
  /** 实例组织分组树（FR-165），需外部「实例→组」映射（多组落首个）。 */
  | 'groupTree'
  /** Network 软标签成员归属（FR-335），需外部「实例→群组」映射（多归属落首个）。 */
  | 'network'
  /** 能力画像角色（FR-445）：proxy/backend/universal/beacon。 */
  | 'role'
  /** 实例类型：minecraft_java/generic。 */
  | 'type'

/** 分组维度下拉可选项（顺序即 UI 顺序；region 为默认）。 */
export const GROUP_DIMENSIONS: GroupDimension[] = [
  'region',
  'zone',
  'groupTree',
  'network',
  'role',
  'type',
  'env',
  'node',
  'status',
  'none',
]

/** 一个分组：key 为分组值（空串表示「未分组」），instances 为成员。 */
export interface InstanceGroup {
  key: string
  /**
   * 成员集合：单级维度为全部成员；region 为「跨子分组扁平」的全量成员（供组头计数/健康色带）；
   * groupTree 为**子树（含后代分组）**成员去重后的并集。
   */
  instances: InstanceInfo[]
  /** 两级维度（region）下的子分组（zone）；groupTree 维度为下级分组（任意层级）。 */
  children?: InstanceGroup[]
  /**
   * **直接**成员（仅 groupTree 多级维度设置）：渲染层先画本组成员的成员行，再递归子分组。
   * 未设置时（region/单级维度）渲染层回落 `instances`。
   */
  direct?: InstanceInfo[]
}

/** region/zone 标签前缀（与后端 model/beacon_sync.go 的 region:/zone: 一致）。 */
export const REGION_TAG_PREFIX = 'region:'
export const ZONE_TAG_PREFIX = 'zone:'

/** 取首个指定前缀标签的值，无则空串。 */
function tagValueOf(inst: Pick<InstanceInfo, 'tags'>, prefix: string): string {
  for (const tag of parseTags(inst.tags)) {
    if (tag.startsWith(prefix)) return tag.slice(prefix.length).trim()
  }
  return ''
}

/** 从实例标签提取大区（region:），无标签返回空串（落「未分组」）。 */
export function regionOf(inst: Pick<InstanceInfo, 'tags'>): string {
  return tagValueOf(inst, REGION_TAG_PREFIX)
}

/** 从实例标签提取小区（zone:），无标签返回空串（落「未分组」）。 */
export function zoneOf(inst: Pick<InstanceInfo, 'tags'>): string {
  return tagValueOf(inst, ZONE_TAG_PREFIX)
}

/**
 * 外部维度所需的「实例 → 分组键」映射（network / groupTree 维度）。
 * 值为分组键（network 为群组名；groupTree 为 `g:<组 id>`，**不含层级**，层级由
 * `GroupTreeSource`/`groupInstancesByGroupTree` 的 children 承载）；
 * 一个实例可命中多个分组，但**落首个**（口径同 lib/topology.ts:groupTopology）。
 */
export type InstanceGroupKeyMap = Map<number, string[]>

/**
 * 组织分组树（FR-165/FR-452）维度的组键前缀：`g:<id>`。
 * 键用**组 id**而非组名——组名无唯一约束，同名组会互相塌缩且丢层级。
 */
export const GROUP_TREE_KEY_PREFIX = 'g:'

/** 由组 id 生成该维度的分组键。 */
export function groupTreeKey(groupId: number): string {
  return `${GROUP_TREE_KEY_PREFIX}${groupId}`
}

/** groupTree 组键的展示元数据（供列表组头与拓扑分带共用层级口径）。 */
export interface GroupKeyLabel {
  /** 组名。 */
  name: string
  /** 祖先路径（`祖先 / … / 父`），根分组为空。 */
  parent?: string
  /** 树前序序号（层序遍历前序），越小越靠前；供拓扑带排序复用分组树顺序。 */
  order: number
  /** 层级深度（根 0）。 */
  depth: number
}

/** 组键 → 展示元数据。 */
export type GroupKeyLabels = Map<string, GroupKeyLabel>

/**
 * groupTree 维度取数结果：实例→组键映射 + 组键展示元数据。
 * - `keys`：**直接**成员归属（多组归属保留全部，聚合时落首个）。
 * - `labels`：组键 → 组名/祖先路径/树序，供列表组头命名与拓扑分带。
 */
export interface GroupTreeSource {
  keys: InstanceGroupKeyMap
  labels: GroupKeyLabels
}

/**
 * 由 `/instance-groups` 的扁平节点（parentId 邻接表）重建分组树，产出 groupTree 维度入参：
 * 键用组 id（同名不同组不合并），并携带层级（祖先路径 + 树序），供列表与拓扑共用。
 * 复用 `instance-group-tree.ts` 的 `buildGroupTree`（同 (sort,id) 排序、孤儿/脏数据健壮）。
 */
export function buildGroupTreeSource(nodes: InstanceGroupNode[]): GroupTreeSource {
  const tree = buildGroupTree(nodes)
  const keys: InstanceGroupKeyMap = new Map()
  const labels: GroupKeyLabels = new Map()
  let seq = 0
  const walk = (list: GroupTreeNode[], ancestors: string[]) => {
    for (const node of list) {
      const key = groupTreeKey(node.id)
      labels.set(key, {
        name: node.name,
        parent: ancestors.length > 0 ? ancestors.join(' / ') : undefined,
        order: seq++,
        depth: node.depth,
      })
      for (const iid of node.memberInstanceIds ?? []) {
        const arr = keys.get(iid)
        if (arr) arr.push(key)
        else keys.set(iid, [key])
      }
      walk(node.children, [...ancestors, node.name])
    }
  }
  walk(tree, [])
  return { keys, labels }
}

/**
 * groupTree 维度层级聚合（FR-452）：按分组树产出**多级** InstanceGroup（父组头 → 子组头 → 成员）。
 * - 键为 `g:<组 id>`（同名不同组各自成组）。
 * - `direct` 为该组**直接**成员；`instances` 为子树（含后代）成员去重并集（供组头计数/健康色带）。
 * - 无成员的组不出现在结果中（空组头无意义）；未归属任何组的实例落末尾 key='' 分组。
 * 纯函数，便于单测校验层级/同名不合并。
 */
export function groupInstancesByGroupTree(
  instances: InstanceInfo[],
  nodes: InstanceGroupNode[],
): InstanceGroup[] {
  if (nodes.length === 0) {
    return instances.length > 0 ? [{ key: '', instances, direct: instances }] : []
  }
  const byId = new Map(instances.map((i) => [i.id, i]))
  const assigned = new Set<number>()
  const direct = new Map<number, InstanceInfo[]>()
  for (const node of nodes) {
    const list: InstanceInfo[] = []
    for (const iid of node.memberInstanceIds ?? []) {
      const inst = byId.get(iid)
      if (!inst || assigned.has(iid)) continue // 多组归属落首个（按节点序）
      assigned.add(iid)
      list.push(inst)
    }
    direct.set(node.id, list)
  }

  const build = (node: GroupTreeNode): InstanceGroup | null => {
    const children = node.children
      .map(build)
      .filter((g): g is InstanceGroup => g !== null)
    const own = direct.get(node.id) ?? []
    const union: InstanceInfo[] = [...own]
    const seen = new Set(own.map((i) => i.id))
    for (const child of children) {
      for (const inst of child.instances) {
        if (seen.has(inst.id)) continue
        seen.add(inst.id)
        union.push(inst)
      }
    }
    if (union.length === 0) return null
    return {
      key: groupTreeKey(node.id),
      instances: union,
      direct: own,
      children: children.length > 0 ? children : undefined,
    }
  }

  const groups = buildGroupTree(nodes)
    .map(build)
    .filter((g): g is InstanceGroup => g !== null)

  const ungrouped = instances.filter((i) => !assigned.has(i.id))
  if (ungrouped.length > 0) groups.push({ key: '', instances: ungrouped, direct: ungrouped })
  return groups
}

/** 参与维度取值的实例最小结构（列表行 / 拓扑投影均可满足）。 */
export type GroupingSubject = Pick<
  InstanceInfo,
  'id' | 'tags' | 'nodeId' | 'status' | 'role' | 'type'
> & {
  /** 能力画像（FR-445）：role/type 维度优先取画像，缺失回退实例字面字段。 */
  capabilities?: InstanceInfo['capabilities']
}

/**
 * 取实例在某分组维度下的关键值（空串=未分组）。
 * 列表页按 `groupInstances` 聚合、拓扑页按 `groupTopologyByDimension` 分带共用同一取值口径，
 * 保证「列表切维度即时重排」与「拓扑层级随维度变化」两处一致。
 */
export function dimensionValueOf(
  inst: GroupingSubject,
  dim: GroupDimension,
  extra?: InstanceGroupKeyMap,
): string {
  return keyOfFor(dim, extra)(inst)
}

/**
 * 由「分组键 → 成员实例 ID」列表构建实例→分组键映射（FR-452 network/groupTree 维度入参）。
 * 一个实例可命中多个分组（软标签/多组归属），全部保留；「落首个」在聚合时按分组顺序决定。
 */
export function buildKeyMap(groups: { key: string; instanceIds: number[] }[]): InstanceGroupKeyMap {
  const map: InstanceGroupKeyMap = new Map()
  for (const g of groups) {
    for (const id of g.instanceIds) {
      const arr = map.get(id)
      if (arr) arr.push(g.key)
      else map.set(id, [g.key])
    }
  }
  return map
}

/** 分组键取值器：由维度决定如何从实例（或外部映射）取关键值。 */
function keyOfFor(dim: GroupDimension, extra?: InstanceGroupKeyMap) {
  return (inst: GroupingSubject): string => {
    switch (dim) {
      case 'node':
        return String(inst.nodeId)
      case 'env':
        return envOf(inst)
      case 'status':
        return inst.status
      case 'region':
      case 'zone':
        return dim === 'region' ? regionOf(inst) : zoneOf(inst)
      case 'role':
        // 优先能力画像（FR-445）的角色；画像缺失回退实例字面 role。
        return resolveCapabilities(inst).role || inst.role || ''
      case 'type':
        return resolveCapabilities(inst).type || inst.type || ''
      case 'network':
      case 'groupTree': {
        const keys = extra?.get(inst.id)
        return keys && keys.length > 0 ? keys[0] : ''
      }
      default:
        return ''
    }
  }
}

/** 分组按 key 字典序排序，空 key（未分组）恒排末尾。 */
function sortGroups(groups: InstanceGroup[]): InstanceGroup[] {
  return groups.sort((a, b) => {
    if (a.key === '') return 1
    if (b.key === '') return -1
    return a.key.localeCompare(b.key)
  })
}

/**
 * 按指定维度把实例聚合为分组（FR-047 / FR-452 分组视图）。
 * - none：单一分组（key=''）含全部，调用方据此走平铺。
 * - node / env / status / zone / role / type：单级分组（key 为该维度值，空=未分组）。
 * - region：**两级树**——外层 region，`children` 为 zone 子分组（无 region 标签落 key=''）。
 * - network / groupTree：按外部映射（extra）的分组键单级分组；未命中落 key=''。
 * 分组按 key 字典序排序，空 key（未分组）恒排末尾（两级同样适用）。
 */
export function groupInstances(
  instances: InstanceInfo[],
  dim: GroupDimension,
  extra?: InstanceGroupKeyMap,
): InstanceGroup[] {
  if (dim === 'none') {
    return [{ key: '', instances }]
  }
  if (dim === 'region') {
    return groupByRegion(instances)
  }
  const keyOf = keyOfFor(dim, extra)
  const map = new Map<string, InstanceInfo[]>()
  for (const inst of instances) {
    const k = keyOf(inst)
    const list = map.get(k)
    if (list) list.push(inst)
    else map.set(k, [inst])
  }
  return sortGroups([...map.entries()].map(([key, list]) => ({ key, instances: list })))
}

/**
 * region 维度两级聚合：外层按 `region:` 标签，内层按 `zone:` 标签。
 * 无 region 标签的实例落外层 key=''（未分组，排末尾）；无 zone 标签落该 region 下 key='' 子组。
 * 只有一层的 region（成员全无 zone）仍保留单元素 children，调用方可扁平渲染。
 */
function groupByRegion(instances: InstanceInfo[]): InstanceGroup[] {
  const outer = new Map<string, Map<string, InstanceInfo[]>>()
  for (const inst of instances) {
    const r = regionOf(inst)
    const z = zoneOf(inst)
    let zones = outer.get(r)
    if (!zones) {
      zones = new Map<string, InstanceInfo[]>()
      outer.set(r, zones)
    }
    const list = zones.get(z)
    if (list) list.push(inst)
    else zones.set(z, [inst])
  }
  return sortGroups(
    [...outer.entries()].map(([region, zones]) => ({
      key: region,
      instances: [...zones.values()].flat(),
      children: sortGroups([...zones.entries()].map(([key, list]) => ({ key, instances: list }))),
    })),
  )
}

/** 判断某维度是否需要外部「实例→分组键」映射。 */
export function dimensionNeedsKeyMap(dim: GroupDimension): boolean {
  return dim === 'network' || dim === 'groupTree'
}
