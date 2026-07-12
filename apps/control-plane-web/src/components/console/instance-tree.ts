import type { InstanceInfo } from '@/api/instances'
import type { NodeInfo } from '@/api/nodes'
import type { GroupDimension } from './instance-grouping'

/** 实例树中一个节点分组：节点信息 + 其下实例列表。 */
export interface NodeGroup {
  nodeId: number
  /** 节点名；节点已不存在时为 undefined（回退到「节点 #id」展示） */
  nodeName?: string
  instances: InstanceInfo[]
}

/**
 * 按节点把实例分组，分组顺序与 `nodes` 一致。
 * 不属于任何已知节点的实例（孤儿）汇总到末尾一个 nodeId<0 的分组，
 * 避免「全部节点」视图下丢失实例。空分组（节点无实例）仍保留，便于显示节点占位。
 */
export function groupInstancesByNode(
  instances: InstanceInfo[],
  nodes: NodeInfo[],
): NodeGroup[] {
  const byNode = new Map<number, InstanceInfo[]>()
  for (const inst of instances) {
    const list = byNode.get(inst.nodeId)
    if (list) list.push(inst)
    else byNode.set(inst.nodeId, [inst])
  }

  const groups: NodeGroup[] = nodes.map((node) => ({
    nodeId: node.id,
    nodeName: node.name,
    instances: byNode.get(node.id) ?? [],
  }))

  const knownIds = new Set(nodes.map((n) => n.id))
  const orphans = instances.filter((inst) => !knownIds.has(inst.nodeId))
  if (orphans.length > 0) {
    groups.push({ nodeId: -1, nodeName: undefined, instances: orphans })
  }

  return groups
}

/**
 * 侧栏实例树某一分组分支的折叠记忆键（FR-069）。
 * 命名空间前缀 `tree:` 与 `<dim>` 隔离，避免与多级侧栏导航组 key（如 `instances`/`monitor`）
 * 在 console store 的 `collapsedGroups` 中相撞；空 groupKey（未分组/孤儿）用 `__none__` 占位。
 * 同一 dim 下同一 groupKey 必稳定，切换 dim 时键自然不同，互不污染折叠态。
 */
export function treeBranchKey(dim: GroupDimension, groupKey: string): string {
  return `tree:${dim}:${groupKey === '' ? '__none__' : groupKey}`
}

/** 一棵侧栏实例树的分支：分组键 + 折叠记忆键 + 该组成员。 */
export interface TreeBranch {
  /** 原始分组键（nodeId 字符串 / env 值 / status 值 / 空串=未分组）。 */
  key: string
  /** 折叠记忆键（写入 console store collapsedGroups）。 */
  branchKey: string
  instances: InstanceInfo[]
}

/**
 * 把已分好的「分组键→成员」列表统一包装为带折叠键的树分支（FR-069）。
 * 不改变入参顺序（分组排序由上游 groupInstances/groupInstancesByNode 决定），
 * 仅为每个分组补出稳定的 branchKey，供组件做「折叠优先」渲染与折叠记忆。
 */
export function toTreeBranches(
  dim: GroupDimension,
  groups: { key: string; instances: InstanceInfo[] }[],
): TreeBranch[] {
  return groups.map((g) => ({
    key: g.key,
    branchKey: treeBranchKey(dim, g.key),
    instances: g.instances,
  }))
}

/**
 * 实例状态点的视觉分类。
 * RUNNING=绿，STARTING/STOPPING=琥珀，CRASHED=红，其余（STOPPED 等）=空心灰。
 */
export type StatusDotKind = 'running' | 'transitioning' | 'crashed' | 'stopped'

export function statusDotKind(status: string): StatusDotKind {
  switch (status) {
    case 'RUNNING':
      return 'running'
    case 'STARTING':
    case 'STOPPING':
      return 'transitioning'
    case 'CRASHED':
      return 'crashed'
    default:
      return 'stopped'
  }
}
