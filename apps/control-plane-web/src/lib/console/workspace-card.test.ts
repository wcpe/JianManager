import { describe, expect, it } from 'vitest'
import { CARD_TYPES, CARD_TYPE_SET, cardTypeDef, isCardType } from './workspace-card'

/**
 * 卡片类型目录 · 纯逻辑测。
 *
 * 目录本身是数据，用例锁的是**数据自洽**：类型不重复、集合与目录同步、
 * 尺寸约束成立（min ≤ default）、i18n 键齐备。这些一旦破了，拖拽落位与
 * 「添加卡片」菜单会静默错位。
 */
describe('workspace-card 目录', () => {
  it('卡片类型不重复', () => {
    const types = CARD_TYPES.map((c) => c.type)
    expect(new Set(types).size).toBe(types.length)
  })

  it('CARD_TYPE_SET 与目录保持同步', () => {
    expect(CARD_TYPE_SET.size).toBe(CARD_TYPES.length)
    for (const c of CARD_TYPES) expect(CARD_TYPE_SET.has(c.type)).toBe(true)
  })

  it('每种卡都给了 i18n 键与合法的尺寸约束（min ≤ default）', () => {
    for (const c of CARD_TYPES) {
      expect(c.titleKey).not.toBe('')
      expect(c.descKey).not.toBe('')
      expect(c.minSize.w).toBeLessThanOrEqual(c.defaultSize.w)
      expect(c.minSize.h).toBeLessThanOrEqual(c.defaultSize.h)
      // 尺寸须落在 12 列网格内，否则拖拽落位会越界。
      expect(c.defaultSize.w).toBeLessThanOrEqual(12)
      expect(c.minSize.w).toBeGreaterThan(0)
    }
  })

  it('cardTypeDef 命中已知类型、未知类型返回 undefined', () => {
    expect(cardTypeDef('terminal')?.type).toBe('terminal')
    expect(cardTypeDef('nope')).toBeUndefined()
  })

  it('isCardType 只认目录内的类型', () => {
    expect(isCardType('bot')).toBe(true)
    expect(isCardType('nope')).toBe(false)
    // 非字符串输入不得抛错。
    expect(isCardType(undefined)).toBe(false)
    expect(isCardType(42)).toBe(false)
  })
})
