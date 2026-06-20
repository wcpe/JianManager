import type { InstanceInfo } from '@/api/instances'

/** 环境维度复用 Tags 字段的约定前缀（FR-047），与后端 model.EnvTagPrefix 一致。 */
export const ENV_TAG_PREFIX = 'env:'

/**
 * 从实例标签中提取环境维度值（去掉 `env:` 前缀），取首个 env 标签。
 * 无 env 标签时返回空串，表示「未分环境」。
 */
export function envOf(inst: Pick<InstanceInfo, 'tags'>): string {
  const tags = inst.tags ?? []
  for (const tag of tags) {
    if (tag.startsWith(ENV_TAG_PREFIX)) {
      return tag.slice(ENV_TAG_PREFIX.length).trim()
    }
  }
  return ''
}

/** 实例的自由标签（剔除 env: 前缀的环境标签）。 */
export function freeTagsOf(inst: Pick<InstanceInfo, 'tags'>): string[] {
  return (inst.tags ?? []).filter((t) => !t.startsWith(ENV_TAG_PREFIX))
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
export type GroupDimension = 'none' | 'node' | 'env' | 'status'

/** 一个分组：key 为分组值（空串表示「未分组」），instances 为成员。 */
export interface InstanceGroup {
  key: string
  instances: InstanceInfo[]
}

/**
 * 按指定维度把实例聚合为分组（FR-047 分组视图）。
 * - none：单一分组（key=''）含全部，调用方据此走平铺。
 * - node：按 nodeId 分组（key 为 nodeId 字符串）。
 * - env：按环境值分组，未分环境归入 key=''。
 * - status：按状态分组。
 * 分组按 key 字典序排序，空 key（未分组）恒排末尾，避免「未分组」插在中间。
 */
export function groupInstances(
  instances: InstanceInfo[],
  dim: GroupDimension,
): InstanceGroup[] {
  if (dim === 'none') {
    return [{ key: '', instances }]
  }
  const keyOf = (inst: InstanceInfo): string => {
    switch (dim) {
      case 'node':
        return String(inst.nodeId)
      case 'env':
        return envOf(inst)
      case 'status':
        return inst.status
    }
  }
  const map = new Map<string, InstanceInfo[]>()
  for (const inst of instances) {
    const k = keyOf(inst)
    const list = map.get(k)
    if (list) list.push(inst)
    else map.set(k, [inst])
  }
  return [...map.entries()]
    .map(([key, list]) => ({ key, instances: list }))
    .sort((a, b) => {
      if (a.key === '') return 1
      if (b.key === '') return -1
      return a.key.localeCompare(b.key)
    })
}
