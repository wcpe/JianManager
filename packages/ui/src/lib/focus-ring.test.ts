import { describe, expect, it } from 'vitest'

import {
  focusRing,
  focusRingClass,
  focusRingDanger,
  focusRingInset,
  invalidFieldState,
} from './focus-ring'

/**
 * 焦点环常量测试（FR-496 阶段 6）。
 *
 * 这些断言守的不是「某个组件画了什么」，而是**焦点环词汇表本身的性质**：
 * ① FR-176 的收敛决定（细环，不得回退成 3px/50% 的粗环）；
 * ② 环必须同时有边线与环底（明暗主题都看得见）；
 * ③ 全部取语义 token，不出现硬编码颜色（否则换主题时环不跟着变）。
 *
 * 组件的视觉细节会随批次调整，这三条属于跨组件的可访问性底线，调整时必须显式改这里。
 */

/** 受 FR-176 禁止的粗焦点环写法（出现在常量里即为回退）。 */
const FORBIDDEN_COARSE_RING = ['focus-visible:ring-[3px]', 'focus-visible:ring-ring/50']

/** 允许出现在类名里的颜色取值方式：语义 token 或 Tailwind 调色板别名，禁止硬编码色值。 */
const HARDCODED_COLOR = /#[0-9a-fA-F]{3,8}\b|\brgba?\(|\bhsla?\(|\boklch\(/

describe('焦点环常量', () => {
  it('标准环是 2px + 40% 的细环（FR-176 收敛值）', () => {
    expect(focusRing).toContain('focus-visible:ring-2')
    expect(focusRing).toContain('focus-visible:ring-ring/40')
  })

  it.each(FORBIDDEN_COARSE_RING)('不回退到过粗/过浓的写法：%s', (coarse) => {
    expect(focusRing).not.toContain(coarse)
  })

  it('环与边线并存，避免只靠一层表达焦点', () => {
    // 浅底上单靠 40% 环对比不足，深底上单靠边线不够醒目 —— 两者缺一都会在某个主题下失效
    expect(focusRing).toContain('focus-visible:border-ring')
  })

  it('内嵌变体 = 标准环 + ring-inset（其余取值与标准环完全一致）', () => {
    expect(focusRingInset).toBe(`${focusRing} focus-visible:ring-inset`)
  })

  it('危险色环用 destructive，且宽度与标准环同档', () => {
    expect(focusRingDanger).toContain('focus-visible:ring-2')
    expect(focusRingDanger).toContain('focus-visible:border-destructive')
    expect(focusRingDanger).toContain('focus-visible:ring-destructive/20')
    expect(focusRingDanger).toContain('dark:focus-visible:ring-destructive/40')
    // 危险环不得混入主色底，否则与 destructive 边线叠色
    expect(focusRingDanger).not.toContain('ring-ring/')
  })

  it('aria-invalid 状态描红与危险色环同源（同一套 destructive 取值）', () => {
    expect(invalidFieldState).toContain('aria-invalid:border-destructive')
    expect(invalidFieldState).toContain('aria-invalid:ring-destructive/20')
    expect(invalidFieldState).toContain('dark:aria-invalid:ring-destructive/40')
    // 状态描红与焦点态是两条正交通道：不能依赖 :focus-visible，否则失焦的无效框看起来是正常的
    expect(invalidFieldState).not.toContain('focus-visible:')
  })

  it('所有取值都是语义 token，不含硬编码颜色', () => {
    for (const value of [focusRing, focusRingInset, focusRingDanger, invalidFieldState]) {
      expect(value).not.toMatch(HARDCODED_COLOR)
    }
  })
})

describe('focusRingClass 色调/位置选择', () => {
  it('默认取标准环', () => {
    expect(focusRingClass()).toBe(focusRing)
    expect(focusRingClass({})).toBe(focusRing)
  })

  it('tone="destructive" 换成危险色环（整串替换，不叠加主色环）', () => {
    const value = focusRingClass({ tone: 'destructive' })
    expect(value).toBe(focusRingDanger)
    expect(value).not.toContain('ring-ring/')
  })

  it('inset 与 tone 可组合，且只出现一次 ring-inset', () => {
    const value = focusRingClass({ tone: 'destructive', inset: true })
    expect(value).toContain('focus-visible:ring-inset')
    expect(value).toContain('focus-visible:border-destructive')
    expect(value.split('focus-visible:ring-inset')).toHaveLength(2)
  })
})
