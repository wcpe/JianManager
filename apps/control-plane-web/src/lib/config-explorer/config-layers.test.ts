import { describe, expect, it } from 'vitest'
import type { ConfigLayerHead, TracedEntry } from '@/api/configFiles'
import {
  buildTracedRows,
  formatTracedValue,
  groupLayersByScope,
  hasInstanceLayer,
  scopeLevelLabelKey,
  shouldWarnInstancePrecedence,
} from './config-layers'

/** 造一条层 head（只填判定用到的字段）。 */
function head(scopeLevel: 'group' | 'instance', scopeRefId: number, versionNo = 1): ConfigLayerHead {
  return {
    scopeLevel,
    scopeRefId,
    scopeName: `${scopeLevel}-${scopeRefId}`,
    versionId: versionNo,
    versionNo,
    contentHash: 'h',
    remark: '',
    createdBy: 1,
    createdAt: 0,
    contentSize: 1,
  }
}

describe('groupLayersByScope：层按作用域分组', () => {
  it('分组层与实例层分开，且组内按 scopeRefId 升序', () => {
    const { groups, instances } = groupLayersByScope([
      head('instance', 7),
      head('group', 3),
      head('instance', 2),
      head('group', 1),
    ])
    expect(groups.map((l) => l.scopeRefId)).toEqual([1, 3])
    expect(instances.map((l) => l.scopeRefId)).toEqual([2, 7])
  })

  it('空/缺省输入返回两组空数组（不抛）', () => {
    expect(groupLayersByScope(undefined)).toEqual({ groups: [], instances: [] })
    expect(groupLayersByScope([])).toEqual({ groups: [], instances: [] })
  })
})

describe('shouldWarnInstancePrecedence：约束 2「实例层优先级最高」的显示条件', () => {
  it('同时有分组层与实例层，且目标实例自己有实例层 → 提示', () => {
    const layers = [head('group', 1), head('instance', 5)]
    expect(shouldWarnInstancePrecedence(layers, 5)).toBe(true)
  })

  it('只有实例层、没有分组层 → 不提示（没有「分组层覆盖不了它」的语境）', () => {
    expect(shouldWarnInstancePrecedence([head('instance', 5)], 5)).toBe(false)
  })

  it('只有分组层、没有实例层 → 不提示', () => {
    expect(shouldWarnInstancePrecedence([head('group', 1), head('group', 2)], 5)).toBe(false)
  })

  it('分组层 + 别的实例的实例层，但目标实例自己没有实例层 → 不提示', () => {
    // 这条是易错点：实例层存在 ≠ 与当前预览实例有关。
    expect(shouldWarnInstancePrecedence([head('group', 1), head('instance', 9)], 5)).toBe(false)
  })

  it('还没选实例 → 不提示', () => {
    expect(shouldWarnInstancePrecedence([head('group', 1), head('instance', 5)], null)).toBe(false)
  })

  it('没有任何层 → 不提示', () => {
    expect(shouldWarnInstancePrecedence([], 5)).toBe(false)
    expect(shouldWarnInstancePrecedence(undefined, 5)).toBe(false)
  })
})

describe('hasInstanceLayer：某实例是否已有实例层版本', () => {
  it('命中同 id 的实例层为真', () => {
    expect(hasInstanceLayer([head('group', 5), head('instance', 5)], 5)).toBe(true)
  })

  it('只有同 id 的分组层不算（分组与实例 id 空间正交）', () => {
    expect(hasInstanceLayer([head('group', 5)], 5)).toBe(false)
  })

  it('实例 id 为空返回假', () => {
    expect(hasInstanceLayer([head('instance', 5)], null)).toBe(false)
  })
})

describe('formatTracedValue：值 → 展示文本', () => {
  it('标量原样，字符串不加引号', () => {
    expect(formatTracedValue('abc')).toBe('abc')
    expect(formatTracedValue(25566)).toBe('25566')
    expect(formatTracedValue(false)).toBe('false')
    expect(formatTracedValue(null)).toBe('null')
  })

  it('对象与数组 JSON 化', () => {
    expect(formatTracedValue({ a: 1 })).toBe('{"a":1}')
    expect(formatTracedValue([1, 2])).toBe('[1,2]')
  })

  it('循环引用不抛（退回 String 兜底）', () => {
    const cyclic: Record<string, unknown> = {}
    cyclic.self = cyclic
    expect(() => formatTracedValue(cyclic)).not.toThrow()
  })
})

describe('buildTracedRows：溯源表行', () => {
  it('实例层排在分组层前，同层内按版本号降序，再按键名字典序', () => {
    const traced: Record<string, TracedEntry> = {
      'group.a': { value: 1, source: { level: 'group', id: 1, name: 'g1', versionNo: 1 } },
      'inst.z': { value: 2, source: { level: 'instance', id: 5, name: 'srv', versionNo: 3 } },
      'inst.a': { value: 3, source: { level: 'instance', id: 5, name: 'srv', versionNo: 3 } },
      'group.b': { value: 4, source: { level: 'group', id: 2, name: 'g2', versionNo: 9 } },
    }
    expect(buildTracedRows(traced).map((r) => r.key)).toEqual(['inst.a', 'inst.z', 'group.b', 'group.a'])
  })

  it('被 null 删除的键标记 deleted（值不参与展示）', () => {
    const rows = buildTracedRows({
      debug: { value: null, source: { level: 'group', id: 1, versionNo: 2 }, deleted: true },
    })
    expect(rows[0].deleted).toBe(true)
  })

  it('缺省 traced 返回空数组', () => {
    expect(buildTracedRows(undefined)).toEqual([])
    expect(buildTracedRows({})).toEqual([])
  })
})

describe('scopeLevelLabelKey：作用域级别 → i18n key', () => {
  it('instance/group 各自映射，未知级别回落到分组层', () => {
    expect(scopeLevelLabelKey('instance')).toBe('configFiles.scope.instance')
    expect(scopeLevelLabelKey('group')).toBe('configFiles.scope.group')
    expect(scopeLevelLabelKey('weird')).toBe('configFiles.scope.group')
  })
})
