import { describe, it, expect } from 'vitest'
import { resourceLevel, tpsLevel, instanceStatusLevel, statusColorVar } from './threshold'

describe('resourceLevel', () => {
  it('按阈值分级 <50 / 50-80 / >80', () => {
    expect(resourceLevel(0)).toBe('success')
    expect(resourceLevel(49.9)).toBe('success')
    expect(resourceLevel(50)).toBe('warning')
    expect(resourceLevel(80)).toBe('warning')
    expect(resourceLevel(80.1)).toBe('danger')
    expect(resourceLevel(100)).toBe('danger')
  })
  it('非数值归中性', () => {
    expect(resourceLevel(NaN)).toBe('neutral')
  })
})

describe('tpsLevel', () => {
  it('按阈值分级 >=18 / 15-18 / <15', () => {
    expect(tpsLevel(20)).toBe('success')
    expect(tpsLevel(18)).toBe('success')
    expect(tpsLevel(17.9)).toBe('warning')
    expect(tpsLevel(15)).toBe('warning')
    expect(tpsLevel(14.9)).toBe('danger')
  })
  it('负值/非数值归中性（探针不可用）', () => {
    expect(tpsLevel(-1)).toBe('neutral')
    expect(tpsLevel(NaN)).toBe('neutral')
  })
})

describe('instanceStatusLevel', () => {
  it('状态映射等级', () => {
    expect(instanceStatusLevel('RUNNING')).toBe('success')
    expect(instanceStatusLevel('STARTING')).toBe('warning')
    expect(instanceStatusLevel('STOPPING')).toBe('warning')
    expect(instanceStatusLevel('CRASHED')).toBe('danger')
    expect(instanceStatusLevel('STOPPED')).toBe('neutral')
    expect(instanceStatusLevel('UNKNOWN')).toBe('neutral')
  })
})

describe('statusColorVar', () => {
  it('等级映射到 CSS 变量', () => {
    expect(statusColorVar('success')).toBe('var(--status-success)')
    expect(statusColorVar('danger')).toBe('var(--status-danger)')
    expect(statusColorVar('neutral')).toBe('var(--muted-foreground)')
  })
})
