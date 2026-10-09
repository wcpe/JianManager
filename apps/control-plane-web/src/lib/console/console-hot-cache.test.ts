import { afterEach, describe, expect, it } from 'vitest'
import { pickEvictionTarget, promoteHotSet } from './console-hot-cache'
import { clearInstanceDrafts, hasInstanceDraft, reportInstanceDraft } from './console-draft-registry'
import { HOT_SET_SIZE, HOT_SET_SIZE_MAX, resolveHotSetCapacity } from './terminal-session-manager'

/** FR-296 跨服热缓存 LRU 纯逻辑 + 草稿注册表；FR-414 容量解析。 */
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
  /**
   * FR-414 起 capacity 必传（不再默认取 HOT_SET_SIZE）。这些用例锁的是 **LRU 算法**
   * 而非容量数值，故显式传 3 让断言与原先逐字相同——容量本身的解析另有下方 describe 覆盖。
   */
  const CAP = 3

  it('未超容不淘汰', () => {
    expect(pickEvictionTarget([1, 2, 3], noDraft, CAP)).toBeNull()
  })

  it('超容淘汰队尾（LRU，最久未用）', () => {
    expect(pickEvictionTarget([4, 3, 2, 1], noDraft, CAP)).toBe(1)
  })

  it('淘汰偏好：队尾带草稿时跳过，改淘汰更早的无草稿成员', () => {
    const hasDraft = (id: number) => id === 1
    expect(pickEvictionTarget([4, 3, 2, 1], hasDraft, CAP)).toBe(2)
  })

  it('候选全带草稿：被迫淘汰队尾（调用方 toast 警示）', () => {
    const hasDraft = () => true
    expect(pickEvictionTarget([4, 3, 2, 1], hasDraft, CAP)).toBe(1)
  })

  it('队首（当前活跃）永不被淘汰', () => {
    const hasDraft = (id: number) => id !== 4
    expect(pickEvictionTarget([4, 3, 2, 1], hasDraft, CAP)).not.toBe(4)
  })

  it('容量随参数生效：同一热集在更大容量下不再淘汰（FR-414 动态提升的落点）', () => {
    expect(pickEvictionTarget([4, 3, 2, 1], noDraft, 3)).toBe(1)
    expect(pickEvictionTarget([4, 3, 2, 1], noDraft, 4)).toBeNull()
  })
})

describe('resolveHotSetCapacity（FR-414 容量治理）', () => {
  it('可见数不超基线时用基线', () => {
    expect(resolveHotSetCapacity(0)).toBe(HOT_SET_SIZE)
    expect(resolveHotSetCapacity(1)).toBe(HOT_SET_SIZE)
    expect(resolveHotSetCapacity(HOT_SET_SIZE)).toBe(HOT_SET_SIZE)
  })

  it('基线默认为 6（原写死的 3 已提高）', () => {
    expect(HOT_SET_SIZE).toBe(6)
  })

  it('可见数超基线时按可见数提升——淘汰一个正在被看的终端是纯 bug', () => {
    expect(resolveHotSetCapacity(HOT_SET_SIZE + 2)).toBe(HOT_SET_SIZE + 2)
  })

  it('提升被硬上限收口，闸不会被可见数一路顶穿', () => {
    expect(resolveHotSetCapacity(HOT_SET_SIZE_MAX + 50)).toBe(HOT_SET_SIZE_MAX)
    expect(HOT_SET_SIZE_MAX).toBeGreaterThan(HOT_SET_SIZE)
  })

  it('基线可覆盖（「可配」），越界与非数一律收口到 [1, MAX]', () => {
    expect(resolveHotSetCapacity(0, 4)).toBe(4)
    expect(resolveHotSetCapacity(0, 0)).toBe(1)
    expect(resolveHotSetCapacity(0, -3)).toBe(1)
    expect(resolveHotSetCapacity(0, HOT_SET_SIZE_MAX + 99)).toBe(HOT_SET_SIZE_MAX)
    expect(resolveHotSetCapacity(0, Number.NaN)).toBe(HOT_SET_SIZE)
  })

  it('小数被下取整（容量必须是整数坑位数）', () => {
    expect(resolveHotSetCapacity(0, 4.9)).toBe(4)
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
