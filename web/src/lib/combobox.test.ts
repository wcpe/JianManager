import { describe, it, expect } from 'vitest'
import {
  optionLabel,
  filterOptions,
  isKnownValue,
  shouldOfferCustom,
  toOptions,
} from './combobox'

const opts = [
  { value: 'paper', label: 'Paper' },
  { value: 'velocity', label: 'Velocity (modern)' },
  { value: 'bungeecord', label: 'BungeeCord' },
]

describe('optionLabel', () => {
  it('label 优先，缺省回退 value', () => {
    expect(optionLabel({ value: 'x', label: 'X' })).toBe('X')
    expect(optionLabel({ value: 'x' })).toBe('x')
  })
})

describe('filterOptions', () => {
  it('空输入返回全部', () => {
    expect(filterOptions(opts, '')).toHaveLength(3)
    expect(filterOptions(opts, '   ')).toHaveLength(3)
  })
  it('大小写不敏感匹配 value 或 label 子串', () => {
    expect(filterOptions(opts, 'PAP').map((o) => o.value)).toEqual(['paper'])
    expect(filterOptions(opts, 'modern').map((o) => o.value)).toEqual(['velocity'])
    expect(filterOptions(opts, 'cord').map((o) => o.value)).toEqual(['bungeecord'])
  })
  it('无命中返回空', () => {
    expect(filterOptions(opts, 'zzz')).toHaveLength(0)
  })
})

describe('isKnownValue', () => {
  it('完全相等才算已知', () => {
    expect(isKnownValue(opts, 'paper')).toBe(true)
    expect(isKnownValue(opts, 'pap')).toBe(false)
    expect(isKnownValue(opts, '')).toBe(false)
  })
})

describe('shouldOfferCustom', () => {
  it('非空且非已知值时提供自定义入口', () => {
    expect(shouldOfferCustom(opts, 'purpur')).toBe(true)
    expect(shouldOfferCustom(opts, 'paper')).toBe(false)
    expect(shouldOfferCustom(opts, '')).toBe(false)
    expect(shouldOfferCustom(opts, '  ')).toBe(false)
  })
})

describe('toOptions', () => {
  it('字符串数组归一为选项', () => {
    expect(toOptions(['a', 'b'])).toEqual([{ value: 'a' }, { value: 'b' }])
  })
})
