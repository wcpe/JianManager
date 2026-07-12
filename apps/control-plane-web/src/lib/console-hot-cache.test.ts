import { afterEach, describe, expect, it } from 'vitest'
import { pickEvictionTarget, promoteHotSet } from './console-hot-cache'
import { clearInstanceDrafts, hasInstanceDraft, reportInstanceDraft } from './console-draft-registry'

/** FR-296 跨服热缓存 LRU 纯逻辑 + 草稿注册表。 */
describe('promoteHotSet', () => {
  it('未命中插入队首', () => {
    expect(promoteHotSet([1, 2], 3)).toEqual([3, 1, 2])
  })

  it('命中即前移置顶（不重复）', () => {
    expect(promoteHotSet([1, 2, 3], 3)).toEqual([3, 1, 2])
  })

  it('已在队首返回原数组（引用不变，避免无谓重渲）', () => {
    const prev = [1, 2, 3]
    expect(promoteHotSet(prev, 1)).toBe(prev)
  })
})

describe('pickEvictionTarget', () => {
  const noDraft = () => false

  it('未超容不淘汰', () => {
    expect(pickEvictionTarget([1, 2, 3], noDraft)).toBeNull()
  })

  it('超容淘汰队尾（LRU，最久未用）', () => {
    expect(pickEvictionTarget([4, 3, 2, 1], noDraft)).toBe(1)
  })

  it('淘汰偏好：队尾带草稿时跳过，改淘汰更早的无草稿成员', () => {
    const hasDraft = (id: number) => id === 1
    expect(pickEvictionTarget([4, 3, 2, 1], hasDraft)).toBe(2)
  })

  it('候选全带草稿：被迫淘汰队尾（调用方 toast 警示）', () => {
    const hasDraft = () => true
    expect(pickEvictionTarget([4, 3, 2, 1], hasDraft)).toBe(1)
  })

  it('队首（当前活跃）永不被淘汰', () => {
    const hasDraft = (id: number) => id !== 4
    expect(pickEvictionTarget([4, 3, 2, 1], hasDraft)).not.toBe(4)
  })
})

describe('console-draft-registry', () => {
  afterEach(() => {
    clearInstanceDrafts(1)
    clearInstanceDrafts(2)
  })

  it('登记/撤销草稿脏态，任一编辑面脏即视为有草稿', () => {
    expect(hasInstanceDraft(1)).toBe(false)
    reportInstanceDraft(1, 'resource-file', true)
    reportInstanceDraft(1, 'resource-config', true)
    expect(hasInstanceDraft(1)).toBe(true)

    reportInstanceDraft(1, 'resource-file', false)
    expect(hasInstanceDraft(1)).toBe(true)
    reportInstanceDraft(1, 'resource-config', false)
    expect(hasInstanceDraft(1)).toBe(false)
  })

  it('实例间互不串扰，clearInstanceDrafts 只清目标实例', () => {
    reportInstanceDraft(1, 'resource-file', true)
    reportInstanceDraft(2, 'resource-file', true)
    clearInstanceDrafts(1)
    expect(hasInstanceDraft(1)).toBe(false)
    expect(hasInstanceDraft(2)).toBe(true)
  })
})
