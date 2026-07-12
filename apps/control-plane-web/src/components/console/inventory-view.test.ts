import { describe, it, expect } from 'vitest'
import {
  parseInventoryView,
  buildSlotGrid,
  INVENTORY_SLOTS,
  ENDER_CHEST_SLOTS,
  type RawItemSlot,
} from './inventory-view'

/** 构造一条原始物品槽（模拟探针 inventory.view 编码出的 encodeItem 结构）。 */
function item(slot: number, extra: Partial<RawItemSlot> = {}): RawItemSlot {
  return { slot, material: 'STONE', amount: 1, nbtBase64: `nbt-${slot}`, ...extra }
}

describe('parseInventoryView 解析 inventory.view 输出', () => {
  it('exists=false → 无数据视图（区分空背包）', () => {
    const v = parseInventoryView({ exists: false, player: 'uuid-x' })
    expect(v).not.toBeNull()
    expect(v!.exists).toBe(false)
    expect(v!.player).toBe('uuid-x')
    expect(v!.inventory).toEqual([])
  })

  it('exists=true → 解析背包/末影箱/在线态/基础属性', () => {
    const v = parseInventoryView({
      exists: true,
      player: 'uuid-1',
      online: true,
      dataVersion: 42,
      inventory: [item(0, { material: 'DIAMOND_SWORD', amount: 1, displayName: '锋利之刃' }), item(5, { amount: 64 })],
      enderChest: [item(1, { material: 'GOLD_INGOT', amount: 16 })],
      basicAttrs: { health: 20, foodLevel: 18, xpLevel: 30, xpProgress: 0.5, xpTotal: 1395, gameMode: 'SURVIVAL' },
    })
    expect(v!.exists).toBe(true)
    expect(v!.online).toBe(true)
    expect(v!.dataVersion).toBe(42)
    expect(v!.inventory).toHaveLength(2)
    expect(v!.inventory[0]).toMatchObject({ slot: 0, material: 'DIAMOND_SWORD', amount: 1, displayName: '锋利之刃' })
    expect(v!.enderChest).toHaveLength(1)
    expect(v!.basicAttrs).toMatchObject({ health: 20, gameMode: 'SURVIVAL' })
  })

  it('非对象 / 缺 exists → null（坏数据降级）', () => {
    expect(parseInventoryView(null)).toBeNull()
    expect(parseInventoryView('oops')).toBeNull()
    expect(parseInventoryView({})).toBeNull()
  })

  it('物品缺 nbtBase64 / material 仍可解析（amount 缺省为 1）', () => {
    const v = parseInventoryView({
      exists: true,
      player: 'p',
      inventory: [{ slot: 2, material: 'APPLE' }],
    })
    expect(v!.inventory[0]).toMatchObject({ slot: 2, material: 'APPLE', amount: 1, nbtBase64: '' })
  })

  it('坏物品项（非对象 / slot 非数）被丢弃', () => {
    const v = parseInventoryView({
      exists: true,
      player: 'p',
      inventory: ['bad', { material: 'X' }, item(3)],
    })
    expect(v!.inventory.map((i) => i.slot)).toEqual([3])
  })

  it('basicAttrs 缺失时为 null', () => {
    const v = parseInventoryView({ exists: true, player: 'p', inventory: [] })
    expect(v!.basicAttrs).toBeNull()
  })
})

describe('buildSlotGrid 槽位 → 定长格子', () => {
  it('背包默认 36 格，空槽补 null', () => {
    const grid = buildSlotGrid([item(0), item(8), item(35)], INVENTORY_SLOTS)
    expect(grid).toHaveLength(INVENTORY_SLOTS)
    expect(grid[0]).not.toBeNull()
    expect(grid[1]).toBeNull()
    expect(grid[8]).not.toBeNull()
    expect(grid[35]).not.toBeNull()
  })

  it('末影箱默认 27 格', () => {
    const grid = buildSlotGrid([item(0)], ENDER_CHEST_SLOTS)
    expect(grid).toHaveLength(ENDER_CHEST_SLOTS)
  })

  it('越界槽位（slot >= size）按需扩容到容纳该槽，对齐整行', () => {
    // 槽 40 超出常规 36；网格须能容纳，且补到 9 的整数倍行（45）。
    const grid = buildSlotGrid([item(40)], INVENTORY_SLOTS)
    expect(grid.length).toBeGreaterThanOrEqual(41)
    expect(grid.length % 9).toBe(0)
    expect(grid[40]).not.toBeNull()
  })

  it('同槽位重复以最后一个为准（防探针重发叠加）', () => {
    const grid = buildSlotGrid([item(3, { material: 'A' }), item(3, { material: 'B' })], INVENTORY_SLOTS)
    expect(grid[3]?.material).toBe('B')
  })

  it('空输入 → 全空定长网格', () => {
    const grid = buildSlotGrid([], INVENTORY_SLOTS)
    expect(grid).toHaveLength(INVENTORY_SLOTS)
    expect(grid.every((c) => c === null)).toBe(true)
  })
})
