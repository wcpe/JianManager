import { describe, it, expect } from 'vitest'
import { pickStatVisual, deltaTone } from './stat-card'

describe('pickStatVisual', () => {
  it('按指标性质混搭右侧视觉：占比→条 / 走势→趋势线 / 计数→双值 / 普通→无', () => {
    expect(pickStatVisual('ratio')).toBe('bar')
    expect(pickStatVisual('trend')).toBe('trend')
    expect(pickStatVisual('count')).toBe('dual')
    expect(pickStatVisual('plain')).toBe('none')
  })
})

describe('deltaTone', () => {
  it('零 / 非数值不出增量', () => {
    expect(deltaTone(0)).toBeNull()
    expect(deltaTone(NaN)).toBeNull()
  })
  it('默认「升为好」：正↑绿、负↓红', () => {
    expect(deltaTone(128)).toEqual({ arrow: '↑', level: 'success' })
    expect(deltaTone(-3)).toEqual({ arrow: '↓', level: 'danger' })
  })
  it('「降为好」（如内存/延迟）：正↑红、负↓绿', () => {
    expect(deltaTone(5, 'down')).toEqual({ arrow: '↑', level: 'danger' })
    expect(deltaTone(-5, 'down')).toEqual({ arrow: '↓', level: 'success' })
  })
})
