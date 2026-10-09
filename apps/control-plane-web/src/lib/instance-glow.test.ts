import { describe, it, expect } from 'vitest'

import { instanceStatusGlowClass } from './instance-glow'

describe('instanceStatusGlowClass (FR-315 状态光晕映射)', () => {
  it('RUNNING → 绿柔光 running', () => {
    const c = instanceStatusGlowClass('RUNNING')
    expect(c).toContain('jm-status-glow-running')
    expect(c).not.toContain('--soft')
  })
  it('STARTING/STOPPING → 琥珀呼吸 transitioning', () => {
    expect(instanceStatusGlowClass('STARTING')).toContain('jm-status-glow-transitioning')
    expect(instanceStatusGlowClass('STOPPING')).toContain('jm-status-glow-transitioning')
  })
  it('CRASHED → 红警示 crashed', () => {
    expect(instanceStatusGlowClass('CRASHED')).toContain('jm-status-glow-crashed')
  })
  it('STOPPED/未知 → 无光晕空串', () => {
    expect(instanceStatusGlowClass('STOPPED')).toBe('')
    expect(instanceStatusGlowClass('WHATEVER')).toBe('')
  })
  it('soft=true → 卡片弱化带 --soft', () => {
    expect(instanceStatusGlowClass('RUNNING', true)).toContain('jm-status-glow--soft')
  })
  it('soft 对无光晕态仍空串', () => {
    expect(instanceStatusGlowClass('STOPPED', true)).toBe('')
  })
})
