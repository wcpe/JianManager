import { describe, it, expect } from 'vitest'
import { buildBusinessPayload, coerceBusinessArg, isWriteAction } from './business-actions'
import type { BusinessAction } from '@/api/business'

function action(name: string, readOnly?: boolean): BusinessAction {
  return { action: name, readOnly }
}

describe('isWriteAction（FR-121 写动作判定）', () => {
  it('readOnly=true 为读动作', () => {
    expect(isWriteAction(action('balance', true))).toBe(false)
  })

  it('readOnly=false 为写动作', () => {
    expect(isWriteAction(action('deposit', false))).toBe(true)
  })

  it('readOnly 缺省从严视为写动作', () => {
    expect(isWriteAction(action('deposit'))).toBe(true)
  })
})

describe('coerceBusinessArg（FR-119 泛化台入参取值）', () => {
  it('对象型文本还原为真嵌套对象（getObject 才拿得到）', () => {
    const parsed = coerceBusinessArg('{"dataVersion":42,"basicAttrs":{"health":18}}')
    expect(parsed).toEqual({ dataVersion: 42, basicAttrs: { health: 18 } })
  })

  it('前后空白不影响对象解析', () => {
    expect(coerceBusinessArg('  {"a":1}  ')).toEqual({ a: 1 })
  })

  it('数组型文本还原为数组', () => {
    expect(coerceBusinessArg('["a","b"]')).toEqual(['a', 'b'])
  })

  it('标量字符串保持原样（economy 的 player/currency 不受影响）', () => {
    expect(coerceBusinessArg('Steve')).toBe('Steve')
    expect(coerceBusinessArg('coin')).toBe('coin')
  })

  it('数值型标量保持字符串（amount="100" 不被误转成数字而破坏既有下发）', () => {
    expect(coerceBusinessArg('100')).toBe('100')
    expect(coerceBusinessArg('-3.5')).toBe('-3.5')
  })

  it('以 { 起头但非法 JSON 保持原字符串（不吞错，交由探针报错）', () => {
    expect(coerceBusinessArg('{not json}')).toBe('{not json}')
  })

  it('空串保持空串', () => {
    expect(coerceBusinessArg('')).toBe('')
  })
})

describe('buildBusinessPayload（FR-119 混合标量/对象入参下发）', () => {
  it('对象入参嵌套、标量入参保持字符串', () => {
    const json = buildBusinessPayload({
      player: 'Steve',
      base: '{"dataVersion":42,"basicAttrs":{"health":20}}',
      edited: '{"dataVersion":42,"basicAttrs":{"health":18}}',
    })
    expect(JSON.parse(json)).toEqual({
      player: 'Steve',
      base: { dataVersion: 42, basicAttrs: { health: 20 } },
      edited: { dataVersion: 42, basicAttrs: { health: 18 } },
    })
  })

  it('纯标量入参（economy 域）逐字段仍为字符串', () => {
    const json = buildBusinessPayload({ player: 'Steve', currency: 'coin', amount: '100' })
    expect(JSON.parse(json)).toEqual({ player: 'Steve', currency: 'coin', amount: '100' })
  })
})
