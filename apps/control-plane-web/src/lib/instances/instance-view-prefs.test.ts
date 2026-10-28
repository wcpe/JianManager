import { afterAll, beforeAll, beforeEach, describe, expect, it } from 'vitest'

import {
  applySelectionClick,
  computeVisibleColumns,
  DEFAULT_COLUMN_ORDER,
  invertSelection,
  INSTANCE_COLUMN_KEYS,
  PINNED_COLUMNS,
  moveColumn,
  readStoredColumnOrder,
  writeStoredColumnOrder,
  type InstanceColumnKey,
} from '@/lib/instances/instance-view-prefs'

/**
 * 表头拖拽换位的核心逻辑（用户提出的需求）。
 *
 * 这里测的是「列顺序」这一模型的边界行为，不是 DOM 拖拽事件本身——
 * 拖拽手势由 @testing-library 的 fireEvent 在页面级用例里覆盖，而顺序计算的正确性
 * （钉住列不动、越界收敛、存储损坏自愈）更适合在这一层穷举。
 */
const STORAGE_KEY = 'jm.instances.columnOrder'

/** 全部可选列（身份三项 + 健康度三项 + 业务）。 */
const ALL_OPTIONAL = ['type', 'role', 'tags', 'metrics', 'active', 'heartbeat', 'business'] as const

/** 去掉指定列后的顺序（用于断言「其余列相对顺序不变」）。 */
function without(order: readonly InstanceColumnKey[], drop: readonly InstanceColumnKey[]): InstanceColumnKey[] {
  return order.filter((k) => !drop.includes(k))
}

/*
  本文件落在 node 项目（无 DOM），但持久化用例需要 localStorage。
  这里装一个最小内存实现：只覆盖本模块用到的 getItem/setItem/clear。
  不用 jsdom 是为了让「纯逻辑」测试保持轻量——拖拽换位的顺序计算不该为了跑通而拉一整个 DOM。
*/
const store = new Map<string, string>()
beforeAll(() => {
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    value: {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => void store.set(k, String(v)),
      removeItem: (k: string) => void store.delete(k),
      clear: () => store.clear(),
    },
  })
})
afterAll(() => {
  Reflect.deleteProperty(globalThis, 'localStorage')
})

describe('列顺序：moveColumn', () => {
  /*
    【为什么断言相对位置而不是写死整条数组】列模型会随产品迭代增列（例如后来加入的
    `business`）。写死整条期望会让「加了一列」误报成「换位算错了」——测试该守的是
    「换位语义」，不是「当前恰好有几列」。
  */
  it('向前拖（源在目标左侧）：落到目标原来那一格，其余相对顺序不变', () => {
    // 「区域」拖到「名称」上 → 名称前移到区域原位，区域紧随其后。
    const next = moveColumn(DEFAULT_COLUMN_ORDER, 'region', 'name')
    expect(next.indexOf('name')).toBe(DEFAULT_COLUMN_ORDER.indexOf('region'))
    expect(next.indexOf('region')).toBe(DEFAULT_COLUMN_ORDER.indexOf('name'))
    // 除这两列外，其余列的相对顺序不变
    expect(without(next, ['region', 'name'])).toEqual(without(DEFAULT_COLUMN_ORDER, ['region', 'name']))
  })

  it('向后拖（源在目标右侧）：落到目标原来那一格', () => {
    // 「状态」拖到「区域」上 → 状态占据区域原位。
    const next = moveColumn(DEFAULT_COLUMN_ORDER, 'status', 'region')
    expect(next.indexOf('status')).toBe(DEFAULT_COLUMN_ORDER.indexOf('region'))
    expect(next.indexOf('region')).toBe(DEFAULT_COLUMN_ORDER.indexOf('region') + 1)
    expect(without(next, ['status', 'region'])).toEqual(without(DEFAULT_COLUMN_ORDER, ['status', 'region']))
  })

  it('跨多列拖动时中间列整体平移一位（不丢列、不重复）', () => {
    const next = moveColumn(DEFAULT_COLUMN_ORDER, 'region', 'status')
    // 区域落到状态的原位；长度守恒，且没有重复列。
    expect(next.indexOf('region')).toBe(DEFAULT_COLUMN_ORDER.indexOf('status'))
    expect(next).toHaveLength(DEFAULT_COLUMN_ORDER.length)
    expect(new Set(next).size).toBe(DEFAULT_COLUMN_ORDER.length)
    // 所有已知列都还在
    for (const k of INSTANCE_COLUMN_KEYS) expect(next).toContain(k)
  })

  it('拖到自己身上是空操作（返回等价副本，不抖动）', () => {
    const next = moveColumn(DEFAULT_COLUMN_ORDER, 'name', 'name')
    expect(next).toEqual([...DEFAULT_COLUMN_ORDER])
  })

  it('钉住的列拖不动：select / actions 既不移动也不被挤开', () => {
    for (const pinned of PINNED_COLUMNS) {
      // 拖钉住列 → 原样返回
      expect(moveColumn(DEFAULT_COLUMN_ORDER, pinned, 'name')).toEqual([...DEFAULT_COLUMN_ORDER])
      // 把别的列拖到钉住列上 → 也原样返回（钉住列位置不能被占）
      expect(moveColumn(DEFAULT_COLUMN_ORDER, 'name', pinned)).toEqual([...DEFAULT_COLUMN_ORDER])
    }
  })

  it('未知目标列时原样返回（不产生丢列的结果）', () => {
    const next = moveColumn(DEFAULT_COLUMN_ORDER, 'name', 'nonexistent' as InstanceColumnKey)
    expect(next).toEqual([...DEFAULT_COLUMN_ORDER])
  })

  it('不修改入参（纯函数）', () => {
    const input = [...DEFAULT_COLUMN_ORDER]
    moveColumn(input, 'status', 'region')
    expect(input).toEqual([...DEFAULT_COLUMN_ORDER])
  })
})

describe('列顺序：持久化', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('写入后可原样读回', () => {
    // 构造一个与默认不同的合法顺序：把 name 提到最前（select 之后）。
    const custom = moveColumn(DEFAULT_COLUMN_ORDER, 'name', 'region')
    writeStoredColumnOrder(custom)
    expect(readStoredColumnOrder()).toEqual(custom)
    // 与默认确实不同，否则这条用例无意义
    expect(custom).not.toEqual([...DEFAULT_COLUMN_ORDER])
  })

  it('无存储时回落默认顺序', () => {
    expect(readStoredColumnOrder()).toEqual([...DEFAULT_COLUMN_ORDER])
  })

  it('存储损坏（非 JSON）时回落默认，不抛错', () => {
    localStorage.setItem(STORAGE_KEY, 'not-json{{{')
    expect(readStoredColumnOrder()).toEqual([...DEFAULT_COLUMN_ORDER])
  })

  it('存储含未知列时只保留已知列，并把缺失列按默认顺序补回', () => {
    // 模拟旧版本存档里多了一个已删除的列，且少了 actions。
    localStorage.setItem(STORAGE_KEY, JSON.stringify(['select', 'ghostColumn', 'name', 'region']))
    const order = readStoredColumnOrder()
    // 已知列被保留且保持原相对次序
    expect(order.slice(0, 3)).toEqual(['select', 'name', 'region'])
    // 缺失的已知列全部补齐，不丢列
    for (const key of INSTANCE_COLUMN_KEYS) expect(order).toContain(key)
    expect(order).toHaveLength(INSTANCE_COLUMN_KEYS.length)
  })

  it('存储含重复列时去重（避免 React key 冲突与列重复渲染）', () => {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(['select', 'name', 'name', 'region']))
    const order = readStoredColumnOrder()
    expect(order.filter((k) => k === 'name')).toHaveLength(1)
    expect(order).toHaveLength(INSTANCE_COLUMN_KEYS.length)
  })

  it('存储为非数组时回落默认', () => {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ order: ['name'] }))
    expect(readStoredColumnOrder()).toEqual([...DEFAULT_COLUMN_ORDER])
  })
})

/**
 * `computeVisibleColumns` 是表头 / 行 / colgroup 三处共用的唯一来源。
 *
 * 【为什么要专门测它】这一层出错的代价不是「算错一点」，而是**整表列错位**，
 * 而且历史上同一类 bug 连续出现过三次（表头与行不同步、健康度格没跟随列设置、
 * colgroup 漏加新增的 business 列）。所以这里把「可选列组合 → 实际列」的映射穷举一遍：
 * 只要这条映射是对的，三处共用它就必然一致。
 */
describe('列计算：computeVisibleColumns', () => {
  it('默认全开时，含身份 / 健康度 / 业务列，且顺序与 columnOrder 一致', () => {
    const cols = computeVisibleColumns(DEFAULT_COLUMN_ORDER, new Set(ALL_OPTIONAL))
    expect(cols).toContain('identity')
    expect(cols).toContain('health')
    expect(cols).toContain('business')
    // 固定列永远在
    for (const k of ['select', 'region', 'name', 'nodePort', 'status', 'actions']) expect(cols).toContain(k)
    expect(cols).toEqual(DEFAULT_COLUMN_ORDER.filter((k) => cols.includes(k)))
  })

  it('身份列：type / role / tags 任一可见即渲染合并格；全关则整格消失', () => {
    expect(computeVisibleColumns(DEFAULT_COLUMN_ORDER, new Set(['type']))).toContain('identity')
    expect(computeVisibleColumns(DEFAULT_COLUMN_ORDER, new Set(['role']))).toContain('identity')
    expect(computeVisibleColumns(DEFAULT_COLUMN_ORDER, new Set(['tags']))).toContain('identity')
    expect(computeVisibleColumns(DEFAULT_COLUMN_ORDER, new Set())).not.toContain('identity')
  })

  it('健康度列：metrics / active / heartbeat 任一可见即渲染合并格；全关则整格消失', () => {
    for (const one of ['metrics', 'active', 'heartbeat']) {
      expect(computeVisibleColumns(DEFAULT_COLUMN_ORDER, new Set([one]))).toContain('health')
    }
    expect(computeVisibleColumns(DEFAULT_COLUMN_ORDER, new Set())).not.toContain('health')
  })

  it('业务列：单一开关控制，且未开启时不出现在结果里', () => {
    expect(computeVisibleColumns(DEFAULT_COLUMN_ORDER, new Set(['business']))).toContain('business')
    expect(computeVisibleColumns(DEFAULT_COLUMN_ORDER, new Set(['type']))).not.toContain('business')
  })

  it('columnOrder 为 undefined 时回落默认顺序（不返回空数组）', () => {
    const cols = computeVisibleColumns(undefined, new Set(ALL_OPTIONAL))
    expect(cols.length).toBeGreaterThan(0)
    expect(cols).toEqual(computeVisibleColumns(DEFAULT_COLUMN_ORDER, new Set(ALL_OPTIONAL)))
  })

  it('顺序数组缺失某已知列时自动补在末尾（旧存储 / 新增列自愈）', () => {
    // 模拟旧存储：只有 select 与 name，其余都没记。
    const cols = computeVisibleColumns(['select', 'name'] as InstanceColumnKey[], new Set(ALL_OPTIONAL))
    expect(cols).toContain('select')
    expect(cols).toContain('name')
    // 其余已知列被补齐，一个都不少
    for (const k of ['region', 'nodePort', 'status', 'actions']) expect(cols).toContain(k)
    expect(cols).toHaveLength(new Set(cols).size) // 无重复
  })

  it('顺序数组含未知 key 时忽略它（不渲染幽灵列）', () => {
    const cols = computeVisibleColumns(
      ['select', 'ghost' as InstanceColumnKey, 'name'] as InstanceColumnKey[],
      new Set(ALL_OPTIONAL),
    )
    expect(cols).not.toContain('ghost')
    expect(cols).toContain('name')
  })

  it('钉住列永远存在，即使 order 里没写它们', () => {
    const cols = computeVisibleColumns(['name'] as InstanceColumnKey[], new Set(ALL_OPTIONAL))
    expect(cols).toContain('select')
    expect(cols).toContain('actions')
  })
})

/**
 * 多选交互（用户提出：Shift 连选 / Ctrl 点选 / 反选）。
 *
 * 这些是纯函数，所以能把修饰键组合穷举一遍——写在组件里就只能靠 DOM 事件测，
 * 边界（无锚点、锚点不在顺序表、跨页）很难覆盖全。
 */
describe('多选：applySelectionClick', () => {
  const ORDER = [1, 2, 3, 4, 5]
  const sel = (...ids: number[]) => new Set(ids)

  it('无修饰键：替换为单项（不是追加）', () => {
    const next = applySelectionClick({ orderedIds: ORDER, selected: sel(1, 2, 3), targetId: 4 })
    expect([...next]).toEqual([4])
  })

  it('Ctrl 点选：切换单项，保留其它已选', () => {
    const added = applySelectionClick({ orderedIds: ORDER, selected: sel(1), targetId: 3, ctrlKey: true })
    expect([...added].sort()).toEqual([1, 3])
    const removed = applySelectionClick({ orderedIds: ORDER, selected: sel(1, 3), targetId: 3, ctrlKey: true })
    expect([...removed]).toEqual([1])
  })

  it('Cmd 点选与 Ctrl 等价（mac 习惯）', () => {
    const next = applySelectionClick({ orderedIds: ORDER, selected: sel(1), targetId: 2, metaKey: true })
    expect([...next].sort()).toEqual([1, 2])
  })

  it('Shift 连选：从锚点向后覆盖区间', () => {
    // 已选 2 作锚点，Shift 点 4 → 选中 2,3,4（2 已在，补 3,4）
    const next = applySelectionClick({ orderedIds: ORDER, selected: sel(2), targetId: 4, shiftKey: true })
    expect([...next].sort()).toEqual([2, 3, 4])
  })

  it('Shift 连选：从锚点向前覆盖区间', () => {
    const next = applySelectionClick({ orderedIds: ORDER, selected: sel(4), targetId: 2, shiftKey: true })
    expect([...next].sort()).toEqual([2, 3, 4])
  })

  it('Shift 连选：区间跨整个列表也正确', () => {
    const next = applySelectionClick({ orderedIds: ORDER, selected: sel(1), targetId: 5, shiftKey: true })
    expect([...next].sort()).toEqual([1, 2, 3, 4, 5])
  })

  it('Shift 连选：无锚点时退化为单选（不是「从开头选到这里」）', () => {
    const next = applySelectionClick({ orderedIds: ORDER, selected: sel(), targetId: 3, shiftKey: true })
    expect([...next]).toEqual([3])
  })

  it('Shift 连选：锚点不在当前顺序表里时退化为单选', () => {
    // 已选 99（不在本页），Shift 点 3：不应把 1..3 全选上
    const next = applySelectionClick({ orderedIds: ORDER, selected: sel(99), targetId: 3, shiftKey: true })
    expect([...next].sort()).toEqual([3, 99])
  })

  it('Shift 连选：不修改传入的集合（纯函数）', () => {
    const original = sel(2)
    applySelectionClick({ orderedIds: ORDER, selected: original, targetId: 4, shiftKey: true })
    expect([...original]).toEqual([2])
  })

  it('目标不在顺序表里时原样返回（不产生幽灵选中）', () => {
    const next = applySelectionClick({ orderedIds: ORDER, selected: sel(1), targetId: 42, shiftKey: true })
    expect([...next]).toEqual([1])
  })
})

describe('多选：invertSelection', () => {
  it('在可见集合内翻转：已选去掉、未选加上', () => {
    const next = invertSelection([1, 2, 3], new Set([1, 3]))
    expect([...next].sort()).toEqual([2])
  })

  it('全选状态下反选 = 全不选', () => {
    const next = invertSelection([1, 2, 3], new Set([1, 2, 3]))
    expect(next.size).toBe(0)
  })

  it('空选状态下反选 = 全选可见集合', () => {
    const next = invertSelection([1, 2, 3], new Set())
    expect([...next].sort()).toEqual([1, 2, 3])
  })

  it('不同页/不在可见集合里的已选项保持不动（不能静默丢掉）', () => {
    const next = invertSelection([1, 2], new Set([1, 99]))
    // 1 在可见集合里被去掉，2 被加上，99（别的页选的）原样保留
    expect([...next].sort()).toEqual([2, 99])
  })
})
