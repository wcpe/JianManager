import { describe, it, expect } from 'vitest'
import { toneChipClass } from './tone'

describe('toneChipClass', () => {
  it('主色走淡染 accent + 主色前景', () => {
    expect(toneChipClass('primary')).toContain('text-primary')
    expect(toneChipClass('primary')).toContain('bg-accent')
  })
  it('状态色映射对应状态类', () => {
    expect(toneChipClass('success')).toContain('text-status-success')
    expect(toneChipClass('warning')).toContain('text-status-warning')
    expect(toneChipClass('danger')).toContain('text-status-danger')
    expect(toneChipClass('info')).toContain('text-status-info')
  })
  it('中性归弱前景', () => {
    expect(toneChipClass('neutral')).toContain('text-muted-foreground')
  })
})
