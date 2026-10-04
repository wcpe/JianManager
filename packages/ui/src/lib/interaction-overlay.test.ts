import { describe, expect, it } from 'vitest'

import {
  disabledState,
  hoverOverlay,
  interactionOverlay,
  interactionTransition,
  pressOverlay,
  pressOverlayClass,
  shadowTransition,
  type PressTone,
} from './interaction-overlay'

/**
 * 交互覆盖层常量测试（FR-496 阶段 6）。
 *
 * 守的是「交互反馈词汇表」的性质，而不是某个组件的某串类：
 * ① 悬停与按压必须在同一份词汇表里成套出现——「有 hover 无 active」是本库最常见的历史缺陷；
 * ② 时长/缓动一律绑 motion token，禁止回到定值（否则脱离全局节奏，改 token 时它不跟着动）；
 * ③ 涂层取语义 token，不出现硬编码颜色。
 */

const HARDCODED_COLOR = /#[0-9a-fA-F]{3,8}\b|\brgba?\(|\bhsla?\(|\boklch\(/
/** 时长类名必须是 motion token 取值；`duration-150` 一类定值即为回退。 */
const MOTION_DURATION_PREFIX = 'duration-[var(--motion-duration-'

/** 取出类名串里不合规的时长类（空数组 = 全部走 token）。 */
function offTokenDurations(value: string): string[] {
  return value
    .split(/\s+/)
    .filter((token) => token.startsWith('duration-') && !token.startsWith(MOTION_DURATION_PREFIX))
}

const ALL_OVERLAYS: Array<[string, string]> = [
  ['hoverOverlay', hoverOverlay],
  ['pressOverlay', pressOverlay],
  ['interactionOverlay', interactionOverlay],
]

describe('悬停 / 按压涂层', () => {
  it('中性悬停涂层取 accent 底色与前景色', () => {
    expect(hoverOverlay).toContain('hover:bg-accent')
    expect(hoverOverlay).toContain('hover:text-accent-foreground')
  })

  it('按压涂层与悬停可区分（按住时必须有可见变化）', () => {
    expect(pressOverlay).toContain('active:bg-accent/70')
    // 深浅主题各有一档：暗色 accent 已是深底，按压需要另一档不透明度才能看出差别
    expect(pressOverlay).toContain('dark:active:bg-accent/60')
    expect(pressOverlay).not.toContain('active:bg-accent ')
  })

  it('interactionOverlay 成套提供悬停与按压（不留「有 hover 无 active」）', () => {
    expect(interactionOverlay).toContain('hover:bg-accent')
    expect(interactionOverlay).toContain('active:bg-accent/70')
  })

  it.each(ALL_OVERLAYS)('%s 不含硬编码颜色', (_name, value) => {
    expect(value).not.toMatch(HARDCODED_COLOR)
  })
})

describe('过渡与禁用态', () => {
  it('交互过渡绑 motion token，且不写定值时长', () => {
    expect(interactionTransition).toContain('duration-[var(--motion-duration-fast)]')
    expect(interactionTransition).toContain('ease-ios')
    expect(offTokenDurations(interactionTransition)).toEqual([])
  })

  it('阴影过渡取 slow 档（卡片悬停抬升的时长比按钮反馈长）', () => {
    expect(shadowTransition).toContain('duration-[var(--motion-duration-slow)]')
    expect(shadowTransition).toContain('ease-ios')
    expect(offTokenDurations(shadowTransition)).toEqual([])
  })

  it('禁用态同时屏蔽指针事件与降不透明度', () => {
    expect(disabledState).toContain('disabled:pointer-events-none')
    expect(disabledState).toContain('disabled:opacity-50')
  })
})

describe('pressOverlayClass 色调分派', () => {
  const tones: PressTone[] = ['primary', 'destructive', 'secondary', 'neutral']

  it.each(tones)('色调 %s 返回按压类且落在自身色系', (tone) => {
    const value = pressOverlayClass(tone)
    expect(value).toContain('active:')
    expect(value).not.toMatch(HARDCODED_COLOR)
  })

  it('四种色调互不相同（避免某个变体静默复用别人的按压色）', () => {
    const values = tones.map(pressOverlayClass)
    expect(new Set(values).size).toBe(tones.length)
  })

  it('neutral 与 hoverOverlay 同源，可直接与中性悬停成套使用', () => {
    expect(pressOverlayClass('neutral')).toBe(pressOverlay)
  })
})
