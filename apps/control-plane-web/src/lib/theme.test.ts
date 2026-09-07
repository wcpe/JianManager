import { describe, it, expect } from 'vitest'
import {
  COLOR_THEMES,
  COLOR_THEME_KEY,
  MODE_KEY,
  resolveColorTheme,
  resolveMode,
  nextMode,
  colorThemeAttr,
  cycleColorTheme,
} from './theme'

describe('resolveColorTheme', () => {
  it('合法 5 值原样返回', () => {
    expect(resolveColorTheme('indigo')).toBe('indigo')
    expect(resolveColorTheme('teal')).toBe('teal')
    expect(resolveColorTheme('ocean')).toBe('ocean')
    expect(resolveColorTheme('violet')).toBe('violet')
    expect(resolveColorTheme('sunset')).toBe('sunset')
  })

  it('缺省/未知/空 回退到 indigo（承 FR-163 默认；旧用户 teal 等存储值继续有效）', () => {
    expect(resolveColorTheme(null)).toBe('indigo')
    expect(resolveColorTheme(undefined)).toBe('indigo')
    expect(resolveColorTheme('')).toBe('indigo')
    expect(resolveColorTheme('purple')).toBe('indigo')
    expect(resolveColorTheme('INDIGO')).toBe('indigo')
  })
})

describe('COLOR_THEMES', () => {
  it('5 值全集，顺序即 UI 展示顺序', () => {
    expect(COLOR_THEMES).toEqual(['indigo', 'teal', 'ocean', 'violet', 'sunset'])
  })
})

describe('colorThemeAttr', () => {
  it('indigo → null（移除 data-theme，回落根变量）', () => {
    expect(colorThemeAttr('indigo')).toBeNull()
  })

  it('其余 4 主题 → 自身字符串（命中对应覆盖组）', () => {
    expect(colorThemeAttr('teal')).toBe('teal')
    expect(colorThemeAttr('ocean')).toBe('ocean')
    expect(colorThemeAttr('violet')).toBe('violet')
    expect(colorThemeAttr('sunset')).toBe('sunset')
  })
})

describe('cycleColorTheme', () => {
  it('按 COLOR_THEMES 顺序逐个循环', () => {
    expect(cycleColorTheme('indigo')).toBe('teal')
    expect(cycleColorTheme('teal')).toBe('ocean')
    expect(cycleColorTheme('ocean')).toBe('violet')
    expect(cycleColorTheme('violet')).toBe('sunset')
    expect(cycleColorTheme('sunset')).toBe('indigo')
  })

  it('循环完整性：从任意主题出发连走 5 步回到起点', () => {
    for (const start of COLOR_THEMES) {
      let current = start
      for (let i = 0; i < COLOR_THEMES.length; i += 1) current = cycleColorTheme(current)
      expect(current).toBe(start)
    }
  })

  it('5 步内遍历全部主题（无重复、无遗漏）', () => {
    const visited = new Set<string>()
    let current: (typeof COLOR_THEMES)[number] = 'indigo'
    for (let i = 0; i < COLOR_THEMES.length; i += 1) {
      visited.add(current)
      current = cycleColorTheme(current)
    }
    expect(visited.size).toBe(COLOR_THEMES.length)
  })
})

describe('resolveMode', () => {
  it('已知值原样返回', () => {
    expect(resolveMode('light')).toBe('light')
    expect(resolveMode('dark')).toBe('dark')
    expect(resolveMode('system')).toBe('system')
  })

  it('缺省/未知 回退到 system', () => {
    expect(resolveMode(null)).toBe('system')
    expect(resolveMode(undefined)).toBe('system')
    expect(resolveMode('')).toBe('system')
    expect(resolveMode('sepia')).toBe('system')
  })
})

describe('nextMode', () => {
  it('三态循环 light → dark → system → light', () => {
    expect(nextMode('light')).toBe('dark')
    expect(nextMode('dark')).toBe('system')
    expect(nextMode('system')).toBe('light')
  })
})

describe('持久键常量', () => {
  it('与既有约定一致', () => {
    expect(MODE_KEY).toBe('theme')
    expect(COLOR_THEME_KEY).toBe('colorTheme')
  })
})
