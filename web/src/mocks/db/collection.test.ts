import { describe, it, expect } from 'vitest'
import { createCollection } from './collection'

interface Row {
  id: number
  name: string
}

describe('createCollection（FR-197 内存假后端集合）', () => {
  it('insert 自增 id，可 list/get', () => {
    const c = createCollection<Row>(() => [])
    const a = c.insert({ name: 'a' })
    const b = c.insert({ name: 'b' })
    expect(a.id).toBe(1)
    expect(b.id).toBe(2)
    expect(c.list()).toHaveLength(2)
    expect(c.get(1)?.name).toBe('a')
  })

  it('update 局部改、remove 删除', () => {
    const c = createCollection<Row>(() => [{ id: 1, name: 'a' }])
    c.update(1, { name: 'z' })
    expect(c.get(1)?.name).toBe('z')
    c.remove(1)
    expect(c.get(1)).toBeUndefined()
  })

  it('reset 回到种子初值，不被先前改动污染（用例隔离）', () => {
    const c = createCollection<Row>(() => [{ id: 1, name: 'seed' }])
    c.update(1, { name: 'changed' })
    c.insert({ name: 'extra' })
    c.reset()
    expect(c.list()).toEqual([{ id: 1, name: 'seed' }])
  })
})
