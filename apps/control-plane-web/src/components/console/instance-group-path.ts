import type { InstanceGroupNode } from '@/api/instanceGroups'

/**
 * 返回某分组从根到自身的路径（面包屑）（FR-165，design §4.4）。
 * 顺序为 [根, …, 目标]；id 未知返回空数组。
 * 对脏数据健壮：visited 去重防环；父指向不存在节点时以当前节点为路径根。
 * 纯函数，便于表驱动单测。
 */
export function groupPathOf(flat: InstanceGroupNode[], id: number): InstanceGroupNode[] {
  const byId = new Map<number, InstanceGroupNode>()
  for (const n of flat) byId.set(n.id, n)
  if (!byId.has(id)) return []

  const path: InstanceGroupNode[] = []
  const visited = new Set<number>()
  let cur: InstanceGroupNode | undefined = byId.get(id)
  while (cur && !visited.has(cur.id)) {
    visited.add(cur.id)
    path.unshift(cur)
    cur = cur.parentId != null ? byId.get(cur.parentId) : undefined
  }
  return path
}
