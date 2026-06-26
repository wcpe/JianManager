import { describe, it, expect } from 'vitest'
import {
  encodeDragPayload,
  parseDragPayload,
  dragPayloadToCards,
  dedupeCards,
  type DragPayload,
} from './instance-library'
import { makeCard, type PlacedCard } from './workspace-preset'

describe('encode / parse drag payload round-trip', () => {
  it('实例 payload 往返等价', () => {
    const p: DragPayload = { kind: 'instance', instanceId: 7 }
    expect(parseDragPayload(encodeDragPayload(p))).toEqual(p)
  })

  it('单功能 payload 往返等价', () => {
    const p: DragPayload = { kind: 'card', instanceId: 3, cardType: 'terminal' }
    expect(parseDragPayload(encodeDragPayload(p))).toEqual(p)
  })

  it('多实例批量 payload 往返等价', () => {
    const p: DragPayload = { kind: 'instances', instanceIds: [1, 2, 4] }
    expect(parseDragPayload(encodeDragPayload(p))).toEqual(p)
  })
})

describe('parseDragPayload 容错', () => {
  it('损坏 JSON / 空串返回 null', () => {
    expect(parseDragPayload('{not json')).toBeNull()
    expect(parseDragPayload('')).toBeNull()
    expect(parseDragPayload('null')).toBeNull()
  })

  it('缺字段 / 非法类型返回 null', () => {
    expect(parseDragPayload(JSON.stringify({ kind: 'instance' }))).toBeNull()
    expect(parseDragPayload(JSON.stringify({ kind: 'card', instanceId: 1 }))).toBeNull()
    expect(parseDragPayload(JSON.stringify({ kind: 'card', instanceId: 1, cardType: 'bogus' }))).toBeNull()
    expect(parseDragPayload(JSON.stringify({ kind: 'instances', instanceIds: 'no' }))).toBeNull()
    expect(parseDragPayload(JSON.stringify({ kind: 'unknown', instanceId: 1 }))).toBeNull()
  })

  it('instanceId 非有限数返回 null', () => {
    expect(parseDragPayload(JSON.stringify({ kind: 'instance', instanceId: 'x' }))).toBeNull()
    expect(parseDragPayload(JSON.stringify({ kind: 'instances', instanceIds: [1, 'x'] }))).toBeNull()
  })
})

describe('dragPayloadToCards', () => {
  it('拖单功能 → 一张该实例该类型卡', () => {
    const cards = dragPayloadToCards({ kind: 'card', instanceId: 5, cardType: 'metrics' }, 0)
    expect(cards).toHaveLength(1)
    expect(cards[0].type).toBe('metrics')
    expect(cards[0].instanceId).toBe(5)
  })

  it('拖实例 → 默认卡组（运维台三卡），均带该实例 id', () => {
    const cards = dragPayloadToCards({ kind: 'instance', instanceId: 9 }, 0)
    expect(cards.length).toBeGreaterThanOrEqual(3)
    expect(cards.every((c) => c.instanceId === 9)).toBe(true)
    expect(cards.map((c) => c.type)).toContain('terminal')
  })

  it('多实例批量 → 每个实例一张终端卡（监看墙），各带自己的 id', () => {
    const cards = dragPayloadToCards({ kind: 'instances', instanceIds: [1, 2, 3] }, 0)
    const termByInst = cards.filter((c) => c.type === 'terminal')
    expect(termByInst.map((c) => c.instanceId).sort()).toEqual([1, 2, 3])
  })

  it('新卡落在给定底行之下，不与既有卡纵向重叠', () => {
    const cards = dragPayloadToCards({ kind: 'card', instanceId: 1, cardType: 'terminal' }, 20)
    expect(cards[0].layout.y).toBeGreaterThanOrEqual(20)
  })
})

describe('dedupeCards（跨实例卡去重）', () => {
  const a = makeCard('terminal', { x: 0, y: 0 }, 1)
  const b = makeCard('terminal', { x: 0, y: 0 }, 2)

  it('同实例同类型视为重复，保留先到者', () => {
    const dup = makeCard('terminal', { x: 5, y: 5 }, 1)
    const out = dedupeCards([a, dup])
    expect(out).toHaveLength(1)
    expect(out[0].id).toBe(a.id)
  })

  it('同类型但不同实例不算重复（监看墙允许多实例同功能）', () => {
    const out = dedupeCards([a, b])
    expect(out).toHaveLength(2)
  })

  it('无 instanceId 的卡（单实例预设遗留）按类型去重，互不误伤带 id 的卡', () => {
    const legacy: PlacedCard = { id: 'x', type: 'terminal', layout: { x: 0, y: 0, w: 4, h: 5 } }
    const out = dedupeCards([legacy, a])
    expect(out).toHaveLength(2)
  })
})
