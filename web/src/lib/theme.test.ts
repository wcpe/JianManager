import { describe, it, expect } from 'vitest'
import {
  COLOR_THEME_KEY,
  MODE_KEY,
  resolveColorTheme,
  resolveMode,
  nextMode,
  colorThemeAttr,
  cycleColorTheme,
} from './theme'

describe('resolveColorTheme', () => {
  it('已知值原样返回', () => {
    expect(resolveColorTheme('indigo')).toBe('indigo')
    expect(resolveColorTheme('teal')).toBe('teal')
  })

  it('缺省/未知/空 回退到 indigo（承 FR-163 默认）', () => {
    expect(resolveColorTheme(null)).toBe('indigo')
    expect(resolveColorTheme(undefined)).toBe('indigo')
    expect(resolveColorTheme('')).toBe('indigo')
    expect(resolveColorTheme('purple')).toBe('indigo')
  })
})

describe('colorThemeAttr', () => {
  it('indigo → null（移除 data-theme，回落根变量）', () => {
    expect(colorThemeAttr('indigo')).toBeNull()
  })

  it('teal → "teal"（设 data-theme）', () => {
    expect(colorThemeAttr('teal')).toBe('teal')
  })
})

describe('cycleColorTheme', () => {
  it('在 indigo↔teal 间循环', () => {
    expect(cycleColorTheme('indigo')).toBe('teal')
    expect(cycleColorTheme('teal')).toBe('indigo')
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
