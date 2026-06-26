import type { InstanceGroupNode } from '@/api/instanceGroups'

/**
 * 实例组织分组树（FR-165 / ADR-033）的纯逻辑：把后端扁平节点（parentId 邻接表）重建为
 * 嵌套树、计算子树集合、按折叠态扁平化为可见行。无 React 依赖，便于表驱动单测。
 */

/** 嵌套后的分组树节点：原节点 + 层级深度 + 子节点。 */
export interface GroupTreeNode extends InstanceGroupNode {
  /** 根为 0，逐级 +1，用于左树缩进。 */
  depth: number
  children: GroupTreeNode[]
}

/**
 * 把扁平节点列表重建为嵌套树。
 * - 同级按 (sort, id) 升序。
 * - parentId 指向不存在节点的节点按根处理（不丢失孤儿）。
 * - 对脏数据（自引用/不可达环）健壮：仅挂接从根可达的节点，绝不死循环。
 */
export function buildGroupTree(flat: InstanceGroupNode[]): GroupTreeNode[] {
  const byId = new Map<number, GroupTreeNode>()
  for (const n of flat) {
    byId.set(n.id, { ...n, depth: 0, children: [] })
  }

  const roots: GroupTreeNode[] = []
  for (const node of byId.values()) {
    const parent =
      node.parentId != null && byId.has(node.parentId) ? byId.get(node.parentId) : undefined
    if (parent && parent.id !== node.id) {
      parent.children.push(node)
    } else {
      roots.push(node)
    }
  }

  const cmp = (a: GroupTreeNode, b: GroupTreeNode) =>
    a.sort !== b.sort ? a.sort - b.sort : a.id - b.id

  // 从根逐层设置 depth 并排序；用显式栈，环节点不可达故自然被排除、不会死循环。
  const visited = new Set<number>()
  const assign = (nodes: GroupTreeNode[], depth: number) => {
    nodes.sort(cmp)
    for (const node of nodes) {
      if (visited.has(node.id)) {
        node.children = []
        continue
      }
      visited.add(node.id)
      node.depth = depth
      assign(node.children, depth + 1)
    }
  }
  roots.sort(cmp)
  assign(roots, 0)
  return roots
}

/**
 * 左树某分支的折叠记忆键（写入 console store collapsedGroups）。
 * 命名空间前缀 `igroup:` 与 `tree:`（侧栏实例树 FR-069）、导航组 key 隔离，互不污染折叠态。
 */
export function groupBranchKey(groupId: number): string {
  return `igroup:${groupId}`
}

/**
 * 返回某分组「子树（含自身及所有后代）」的全部分组 ID。
 * 用于「按组（含子树）筛选」时确定参与的分组集合；id 未知返回空数组。
 * 对脏数据健壮：visited 去重防环。
 */
export function subtreeGroupIds(flat: InstanceGroupNode[], rootId: number): number[] {
  const childrenOf = new Map<number, number[]>()
  const ids = new Set<number>()
  for (const n of flat) {
    ids.add(n.id)
    if (n.parentId != null) {
      const arr = childrenOf.get(n.parentId)
      if (arr) arr.push(n.id)
      else childrenOf.set(n.parentId, [n.id])
    }
  }
  if (!ids.has(rootId)) return []

  const out: number[] = []
  const visited = new Set<number>()
  const stack = [rootId]
  while (stack.length > 0) {
    const cur = stack.pop()!
    if (visited.has(cur)) continue
    visited.add(cur)
    out.push(cur)
    for (const child of childrenOf.get(cur) ?? []) {
      if (!visited.has(child)) stack.push(child)
    }
  }
  return out
}

/** 扁平化后的一行可见分组：节点 + 是否有子节点（决定是否显示折叠箭头）。 */
export interface VisibleGroupRow extends GroupTreeNode {
  hasChildren: boolean
}

/**
 * 按折叠态把嵌套树前序扁平化为可见行列表（折叠优先）。
 * collapsed[groupBranchKey(id)]===true 的节点：保留该节点本身，但其后代不渲染。
 * 这样 1000+ 实例、深嵌套下折叠分支不铺开、不卡（验收 §5）。
 */
export function flattenVisibleGroups(
  tree: GroupTreeNode[],
  collapsed: Record<string, boolean>,
): VisibleGroupRow[] {
  const rows: VisibleGroupRow[] = []
  const walk = (nodes: GroupTreeNode[]) => {
    for (const node of nodes) {
      rows.push({ ...node, hasChildren: node.children.length > 0 })
      if (node.children.length > 0 && !collapsed[groupBranchKey(node.id)]) {
        walk(node.children)
      }
    }
  }
  walk(tree)
  return rows
}
