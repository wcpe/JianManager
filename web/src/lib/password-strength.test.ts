import { describe, it, expect } from 'vitest'
import { passwordStrength } from './password-strength'

describe('passwordStrength', () => {
  it('空密码 score 0、无档位标签、所有规则未命中', () => {
    const r = passwordStrength('')
    expect(r.score).toBe(0)
    expect(r.labelKey).toBeUndefined()
    expect(r.rules.every((x) => !x.met)).toBe(true)
  })

  it('未达 8 位最多算弱', () => {
    const r = passwordStrength('Ab1!')
    expect(r.score).toBe(1)
    expect(r.labelKey).toBe('setup.pwWeak')
    expect(r.rules.find((x) => x.key === 'setup.pwRuleLength')?.met).toBe(false)
  })

  it('达标长度 + 单一字符类为中', () => {
    expect(passwordStrength('abcdefgh').score).toBe(2)
  })

  it('达标长度 + 三类字符为强', () => {
    expect(passwordStrength('Abcdefgh').score).toBe(3)
  })

  it('达标长度 + 四类及以上为很强', () => {
    expect(passwordStrength('Abcdef1!').score).toBe(4)
    expect(passwordStrength('Abcdefg1').labelKey).toBe('setup.pwStrong')
  })
})
