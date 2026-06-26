import { describe, it, expect } from 'vitest'
import {
  buildGroupTree,
  groupBranchKey,
  subtreeGroupIds,
  flattenVisibleGroups,
  type GroupTreeNode,
} from './instance-group-tree'
import type { InstanceGroupNode } from '@/api/instanceGroups'

function gnode(id: number, parentId: number | null, sort = 0): InstanceGroupNode {
  return { id, uuid: `g-${id}`, name: `grp-${id}`, parentId, sort, instanceCount: 0 }
}

describe('buildGroupTree', () => {
  it('builds nested tree from flat nodes, roots first', () => {
    // 1 → 2 → 3, 1 → 4, 5 (root)
    const flat = [gnode(1, null), gnode(2, 1), gnode(3, 2), gnode(4, 1), gnode(5, null)]
    const tree = buildGroupTree(flat)

    expect(tree.map((n) => n.id)).toEqual([1, 5])
    const n1 = tree[0]
    expect(n1.children.map((c) => c.id)).toEqual([2, 4])
    expect(n1.children[0].children.map((c) => c.id)).toEqual([3])
    expect(n1.depth).toBe(0)
    expect(n1.children[0].depth).toBe(1)
    expect(n1.children[0].children[0].depth).toBe(2)
  })

  it('orders siblings by sort then id', () => {
    const flat = [gnode(10, null, 2), gnode(11, null, 1), gnode(12, null, 1)]
    const tree = buildGroupTree(flat)
    // sort=1 先（id 11 < 12），sort=2 后（id 10）
    expect(tree.map((n) => n.id)).toEqual([11, 12, 10])
  })

  it('treats a node whose parent is missing as a root (no orphan loss)', () => {
    const flat = [gnode(2, 99)]
    const tree = buildGroupTree(flat)
    expect(tree.map((n) => n.id)).toEqual([2])
    expect(tree[0].depth).toBe(0)
  })

  it('does not infinite-loop on a self-referential cycle (defensive)', () => {
    // 后端防环，但前端仍需对脏数据健壮：2→2 自环不应进入死循环
    const flat = [gnode(1, null), { ...gnode(2, 2) }]
    const tree = buildGroupTree(flat)
    // 自环节点不挂到任何可达根下，至少不崩溃；根 1 正常
    expect(tree.some((n) => n.id === 1)).toBe(true)
  })

  it('returns empty array for empty input', () => {
    expect(buildGroupTree([])).toEqual([])
  })
})

describe('groupBranchKey', () => {
  it('namespaces by igroup: prefix so it never collides with node/nav keys', () => {
    expect(groupBranchKey(1)).toBe('igroup:1')
    expect(groupBranchKey(42)).toBe('igroup:42')
  })
})

describe('subtreeGroupIds', () => {
  const flat = [gnode(1, null), gnode(2, 1), gnode(3, 2), gnode(4, 1), gnode(5, null)]

  it('returns the node itself plus all descendants', () => {
    expect(subtreeGroupIds(flat, 1).sort((a, b) => a - b)).toEqual([1, 2, 3, 4])
    expect(subtreeGroupIds(flat, 2).sort((a, b) => a - b)).toEqual([2, 3])
    expect(subtreeGroupIds(flat, 3)).toEqual([3])
    expect(subtreeGroupIds(flat, 5)).toEqual([5])
  })

  it('returns just the id when it has no children', () => {
    expect(subtreeGroupIds(flat, 4)).toEqual([4])
  })

  it('returns empty when the id is unknown', () => {
    expect(subtreeGroupIds(flat, 999)).toEqual([])
  })
})

describe('flattenVisibleGroups', () => {
  const flat = [gnode(1, null), gnode(2, 1), gnode(3, 2), gnode(4, 1), gnode(5, null)]

  it('flattens all nodes when nothing is collapsed (pre-order)', () => {
    const tree = buildGroupTree(flat)
    const rows = flattenVisibleGroups(tree, {})
    expect(rows.map((r) => r.id)).toEqual([1, 2, 3, 4, 5])
  })

  it('hides descendants of a collapsed node (折叠优先：折叠分支不铺开)', () => {
    const tree = buildGroupTree(flat)
    // 折叠节点 1 → 其子孙 2,3,4 全部不渲染；根 5 仍可见
    const rows = flattenVisibleGroups(tree, { 'igroup:1': true })
    expect(rows.map((r) => r.id)).toEqual([1, 5])
  })

  it('collapsing a mid node hides only its own subtree', () => {
    const tree = buildGroupTree(flat)
    const rows = flattenVisibleGroups(tree, { 'igroup:2': true })
    // 2 折叠 → 隐藏 3；其余可见
    expect(rows.map((r) => r.id)).toEqual([1, 2, 4, 5])
  })

  it('marks hasChildren correctly', () => {
    const tree = buildGroupTree(flat)
    const rows = flattenVisibleGroups(tree, {})
    const byId = new Map(rows.map((r) => [r.id, r]))
    expect(byId.get(1)!.hasChildren).toBe(true)
    expect(byId.get(3)!.hasChildren).toBe(false)
    expect(byId.get(5)!.hasChildren).toBe(false)
  })
})

describe('GroupTreeNode type shape', () => {
  it('exposes depth and children', () => {
    const tree = buildGroupTree([gnode(1, null)])
    const n: GroupTreeNode = tree[0]
    expect(n.depth).toBe(0)
    expect(Array.isArray(n.children)).toBe(true)
  })
})
