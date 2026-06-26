import { describe, it, expect } from 'vitest'
import { groupPathOf } from './instance-group-path'
import type { InstanceGroupNode } from '@/api/instanceGroups'

function gnode(id: number, parentId: number | null, name = `grp-${id}`): InstanceGroupNode {
  return { id, uuid: `g-${id}`, name, parentId, sort: 0, instanceCount: 0 }
}

describe('groupPathOf', () => {
  // 1(亚洲) → 2(生存) → 3(主城), 1 → 4
  const flat = [
    gnode(1, null, '亚洲区'),
    gnode(2, 1, '生存'),
    gnode(3, 2, '主城'),
    gnode(4, 1, '创造'),
  ]

  it('returns root→...→node path for a deep node', () => {
    expect(groupPathOf(flat, 3).map((n) => n.name)).toEqual(['亚洲区', '生存', '主城'])
  })

  it('returns single segment for a root node', () => {
    expect(groupPathOf(flat, 1).map((n) => n.name)).toEqual(['亚洲区'])
  })

  it('returns the node alone path for a second-level branch', () => {
    expect(groupPathOf(flat, 4).map((n) => n.id)).toEqual([1, 4])
  })

  it('returns empty for unknown id', () => {
    expect(groupPathOf(flat, 999)).toEqual([])
  })

  it('does not infinite-loop on a parent cycle (defensive)', () => {
    // 2→1→2 脏环：path 构建必须终止
    const dirty = [gnode(1, 2, 'A'), gnode(2, 1, 'B')]
    const path = groupPathOf(dirty, 1)
    // 至少包含自身且长度有界（不死循环）
    expect(path.length).toBeGreaterThanOrEqual(1)
    expect(path.length).toBeLessThanOrEqual(2)
  })

  it('treats a missing parent as the path root (no crash)', () => {
    const flat2 = [gnode(5, 99, 'orphan')]
    expect(groupPathOf(flat2, 5).map((n) => n.id)).toEqual([5])
  })
})
