import { describe, expect, it } from 'vitest'
import { chartAccessibleName } from './chart-a11y'

describe('chartAccessibleName 图表替代文本', () => {
  it('显式 ariaLabel 优先于推导', () => {
    expect(chartAccessibleName([{ name: 'TPS' }], '近 24 小时 TPS 趋势', '暂无数据')).toBe(
      '近 24 小时 TPS 趋势',
    )
  })

  it('未显式给出时按序列名拼接', () => {
    expect(chartAccessibleName([{ name: 'TPS' }, { name: 'MSPT' }], undefined, undefined)).toBe(
      'TPS, MSPT',
    )
  })

  it('同名序列去重、空名与空白名滤掉', () => {
    expect(
      chartAccessibleName([{ name: '世界 A' }, { name: '世界 A' }, { name: '  ' }], undefined, undefined),
    ).toBe('世界 A')
  })

  it('全推不出时退到调用方给的本地化提示', () => {
    expect(chartAccessibleName([{ name: '' }], undefined, '暂无数据')).toBe('暂无数据')
  })

  it('既无序列名也无提示时返回 undefined（不挂空的 aria-label）', () => {
    expect(chartAccessibleName([], undefined, undefined)).toBeUndefined()
    expect(chartAccessibleName([], undefined, '   ')).toBeUndefined()
  })
})
