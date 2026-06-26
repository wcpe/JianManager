import { describe, it, expect } from 'vitest'
import {
  makeCard,
  defaultOpsPreset,
  builtinPresets,
  normalizePreset,
  serializePresets,
  deserializePresets,
  layoutToCards,
  cardsToLayout,
  type WorkspacePreset,
  type PlacedCard,
} from './workspace-preset'
import { GRID_COLS } from './workspace-card'

describe('makeCard', () => {
  it('用类型默认尺寸生成卡片，附唯一 id', () => {
    const c = makeCard('terminal', { x: 0, y: 0 })
    expect(c.type).toBe('terminal')
    expect(c.layout.x).toBe(0)
    expect(c.layout.y).toBe(0)
    expect(c.layout.w).toBe(7)
    expect(c.layout.h).toBe(11)
    expect(c.id).toMatch(/terminal/)
  })

  it('相邻两次生成的卡片 id 不同', () => {
    const a = makeCard('bot', { x: 0, y: 0 })
    const b = makeCard('bot', { x: 0, y: 0 })
    expect(a.id).not.toBe(b.id)
  })

  it('可覆写尺寸', () => {
    const c = makeCard('resource', { x: 2, y: 3, w: 4, h: 6 })
    expect(c.layout).toMatchObject({ x: 2, y: 3, w: 4, h: 6 })
  })
})

describe('defaultOpsPreset（运维台）', () => {
  it('默认布局含大终端 + 状态 + 资源三卡', () => {
    const p = defaultOpsPreset()
    const types = p.cards.map((c) => c.type).sort()
    expect(types).toEqual(['resource', 'serverstate', 'terminal'])
    expect(p.builtin).toBe(true)
    expect(p.id).toBe('ops')
  })

  it('终端卡占左侧主区（x=0 且最宽）', () => {
    const p = defaultOpsPreset()
    const term = p.cards.find((c) => c.type === 'terminal')!
    expect(term.layout.x).toBe(0)
    expect(term.layout.w).toBeGreaterThanOrEqual(6)
  })

  it('卡片之间不水平重叠超出网格列数', () => {
    const p = defaultOpsPreset()
    for (const c of p.cards) {
      expect(c.layout.x + c.layout.w).toBeLessThanOrEqual(GRID_COLS)
    }
  })
})

describe('builtinPresets', () => {
  it('内置预设含运维台 + 纯终端 + 资源', () => {
    const ids = builtinPresets().map((p) => p.id)
    expect(ids).toContain('ops')
    expect(ids).toContain('terminal')
    expect(ids).toContain('resource')
  })

  it('全部内置预设都标记 builtin', () => {
    expect(builtinPresets().every((p) => p.builtin)).toBe(true)
  })
})

describe('normalizePreset', () => {
  it('丢弃未知卡片类型', () => {
    const raw = {
      id: 'x',
      name: 'X',
      cards: [
        { id: 'a', type: 'terminal', layout: { x: 0, y: 0, w: 4, h: 4 } },
        { id: 'b', type: 'bogus', layout: { x: 0, y: 0, w: 4, h: 4 } },
      ],
    }
    const p = normalizePreset(raw)!
    expect(p.cards).toHaveLength(1)
    expect(p.cards[0].type).toBe('terminal')
  })

  it('把超出网格列数的卡片夹回网格内', () => {
    const raw = {
      id: 'x',
      name: 'X',
      cards: [{ id: 'a', type: 'terminal', layout: { x: 20, y: 0, w: 30, h: 4 } }],
    }
    const p = normalizePreset(raw)!
    const c = p.cards[0]
    expect(c.layout.x).toBeGreaterThanOrEqual(0)
    expect(c.layout.w).toBeLessThanOrEqual(GRID_COLS)
    expect(c.layout.x + c.layout.w).toBeLessThanOrEqual(GRID_COLS)
  })

  it('把小于最小尺寸的卡片提到最小尺寸', () => {
    const raw = {
      id: 'x',
      name: 'X',
      cards: [{ id: 'a', type: 'terminal', layout: { x: 0, y: 0, w: 1, h: 1 } }],
    }
    const c = normalizePreset(raw)!.cards[0]
    // terminal minSize = { w: 3, h: 5 }
    expect(c.layout.w).toBeGreaterThanOrEqual(3)
    expect(c.layout.h).toBeGreaterThanOrEqual(5)
  })

  it('缺 id/name/cards 的对象返回 null', () => {
    expect(normalizePreset(null)).toBeNull()
    expect(normalizePreset({})).toBeNull()
    expect(normalizePreset({ id: 'x' })).toBeNull()
    expect(normalizePreset({ id: 'x', name: 'X' })).toBeNull()
    expect(normalizePreset({ id: 'x', name: 'X', cards: 'nope' })).toBeNull()
  })

  it('非法 layout 字段的卡片被剔除（而非整体失败）', () => {
    const raw = {
      id: 'x',
      name: 'X',
      cards: [
        { id: 'a', type: 'terminal', layout: { x: 0, y: 0, w: 4, h: 4 } },
        { id: 'b', type: 'bot', layout: { x: 'NaN', y: 0, w: 4, h: 4 } },
        { id: 'c', type: 'bot' },
      ],
    }
    const p = normalizePreset(raw)!
    expect(p.cards).toHaveLength(1)
    expect(p.cards[0].id).toBe('a')
  })
})

describe('serialize / deserialize round-trip', () => {
  it('序列化后反序列化等价（仅用户预设入库）', () => {
    const presets: WorkspacePreset[] = [
      {
        id: 'u1',
        name: '我的布局',
        cards: [makeCard('terminal', { x: 0, y: 0 }), makeCard('metrics', { x: 7, y: 0 })],
      },
    ]
    const json = serializePresets(presets)
    const back = deserializePresets(json)
    expect(back).toHaveLength(1)
    expect(back[0].name).toBe('我的布局')
    expect(back[0].cards.map((c) => c.type).sort()).toEqual(['metrics', 'terminal'])
  })

  it('损坏 JSON 反序列化回空数组', () => {
    expect(deserializePresets('{not json')).toEqual([])
    expect(deserializePresets('')).toEqual([])
    expect(deserializePresets('null')).toEqual([])
    expect(deserializePresets('{"presets": 123}')).toEqual([])
  })

  it('反序列化过滤掉无法规整的预设', () => {
    const json = JSON.stringify({
      presets: [
        { id: 'ok', name: 'OK', cards: [{ id: 'a', type: 'terminal', layout: { x: 0, y: 0, w: 4, h: 6 } }] },
        { garbage: true },
        { id: 'empty', name: 'E', cards: [{ id: 'z', type: 'bogus', layout: { x: 0, y: 0, w: 4, h: 6 } }] },
      ],
    })
    const back = deserializePresets(json)
    // 'empty' 卡全为非法类型 → cards 空，但预设结构合法仍保留（允许空画布预设）
    expect(back.map((p) => p.id).sort()).toEqual(['empty', 'ok'])
    expect(back.find((p) => p.id === 'empty')!.cards).toHaveLength(0)
  })

  it('内置预设不入库（serializePresets 仅写传入的用户预设）', () => {
    const json = serializePresets([])
    expect(deserializePresets(json)).toEqual([])
  })
})

describe('instanceId 携带（FR-167 跨实例）', () => {
  it('makeCard 可携带 instanceId', () => {
    const c = makeCard('terminal', { x: 0, y: 0 }, 42)
    expect(c.instanceId).toBe(42)
  })

  it('不传 instanceId 时为 undefined（单实例画布无需）', () => {
    const c = makeCard('terminal', { x: 0, y: 0 })
    expect(c.instanceId).toBeUndefined()
  })

  it('序列化/反序列化保留 instanceId', () => {
    const presets: WorkspacePreset[] = [
      {
        id: 's1',
        name: '监看墙',
        cards: [makeCard('terminal', { x: 0, y: 0 }, 1), makeCard('terminal', { x: 6, y: 0 }, 2)],
      },
    ]
    const back = deserializePresets(serializePresets(presets))
    expect(back[0].cards.map((c) => c.instanceId).sort()).toEqual([1, 2])
  })

  it('向后兼容：旧预设（卡无 instanceId）反序列化为 undefined，不报错', () => {
    const json = JSON.stringify({
      presets: [{ id: 'old', name: '旧', cards: [{ id: 'a', type: 'terminal', layout: { x: 0, y: 0, w: 4, h: 6 } }] }],
    })
    const back = deserializePresets(json)
    expect(back).toHaveLength(1)
    expect(back[0].cards[0].instanceId).toBeUndefined()
  })

  it('normalizePreset 丢弃非有限 instanceId（容错为无主卡而非整体失败）', () => {
    const raw = {
      id: 'x',
      name: 'X',
      cards: [{ id: 'a', type: 'terminal', instanceId: 'NaN', layout: { x: 0, y: 0, w: 4, h: 6 } }],
    }
    const p = normalizePreset(raw)!
    expect(p.cards).toHaveLength(1)
    expect(p.cards[0].instanceId).toBeUndefined()
  })
})

describe('layoutToCards / cardsToLayout', () => {
  const cards: PlacedCard[] = [
    makeCard('terminal', { x: 0, y: 0, w: 6, h: 10 }),
    makeCard('serverstate', { x: 6, y: 0, w: 4, h: 8 }),
  ]

  it('cardsToLayout 输出 react-grid-layout 布局项（含最小尺寸约束）', () => {
    const layout = cardsToLayout(cards)
    expect(layout).toHaveLength(2)
    const term = layout.find((l) => l.i === cards[0].id)!
    expect(term).toMatchObject({ x: 0, y: 0, w: 6, h: 10 })
    expect(term.minW).toBe(3)
    expect(term.minH).toBe(5)
  })

  it('layoutToCards 把 RGL 回调的新坐标写回卡片，未知 i 忽略', () => {
    const next = layoutToCards(cards, [
      { i: cards[0].id, x: 2, y: 1, w: 8, h: 12 },
      { i: cards[1].id, x: 0, y: 12, w: 5, h: 6 },
      { i: 'ghost', x: 0, y: 0, w: 1, h: 1 },
    ])
    expect(next).toHaveLength(2)
    expect(next[0].layout).toMatchObject({ x: 2, y: 1, w: 8, h: 12 })
    expect(next[1].layout).toMatchObject({ x: 0, y: 12, w: 5, h: 6 })
  })

  it('layoutToCards 保留卡片 type/id 不变', () => {
    const next = layoutToCards(cards, cardsToLayout(cards))
    expect(next.map((c) => c.type)).toEqual(['terminal', 'serverstate'])
    expect(next.map((c) => c.id)).toEqual(cards.map((c) => c.id))
  })
})
