import type { InstanceInfo } from '@/api/instances'
import type { NodeInfo } from '@/api/nodes'

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
