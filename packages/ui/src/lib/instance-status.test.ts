import { describe, expect, it } from 'vitest'
import { statusDotKind } from './instance-status'

/**
 * 实例状态归并 · 纯逻辑测。
 *
 * 这份判断原本散在应用侧 `instance-tree.ts`，随状态点组件一并归包；用例锁定四态映射，
 * 尤其是「未知状态按已停止处理」——它决定了前端不会把没见过的状态显示成健康。
 */
describe('statusDotKind', () => {
  it('RUNNING 归为运行中', () => {
    expect(statusDotKind('RUNNING')).toBe('running')
  })

  it('STARTING / STOPPING 归为过渡中', () => {
    expect(statusDotKind('STARTING')).toBe('transitioning')
    expect(statusDotKind('STOPPING')).toBe('transitioning')
  })

  it('CRASHED / DAMAGED 归为崩溃', () => {
    expect(statusDotKind('CRASHED')).toBe('crashed')
    expect(statusDotKind('DAMAGED')).toBe('crashed')
  })

  it('STOPPED 与未知状态都归为已停止（不把没见过的状态显示成健康）', () => {
    expect(statusDotKind('STOPPED')).toBe('stopped')
    expect(statusDotKind('WHATEVER')).toBe('stopped')
    expect(statusDotKind('')).toBe('stopped')
  })
})
