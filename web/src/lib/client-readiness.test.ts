import { describe, it, expect } from 'vitest'
import {
  deriveReadiness,
  readinessCompletedCount,
  isChannelReady,
  READINESS_ORDER,
} from './client-readiness'

/**
 * 客户端分发频道就绪度推导（FR-187）。
 * 工作台顶部步骤器据此渲染：完成步骤折叠 ✓、第一个未完成步骤高亮引导。
 */
describe('deriveReadiness', () => {
  it('刚建频道（无密钥、未发版）：channel 完成，keys 为当前引导步骤', () => {
    const steps = deriveReadiness({ keyCount: 0, currentVersion: 0 })
    expect(steps.map((s) => s.id)).toEqual(READINESS_ORDER)
    expect(steps.find((s) => s.id === 'channel')).toMatchObject({ done: true, current: false })
    expect(steps.find((s) => s.id === 'keys')).toMatchObject({ done: false, current: true })
    expect(steps.find((s) => s.id === 'version')).toMatchObject({ done: false, current: false })
    expect(steps.find((s) => s.id === 'integrate')).toMatchObject({ done: false, current: false })
  })

  it('已建密钥但未发版：version 为当前引导步骤', () => {
    const steps = deriveReadiness({ keyCount: 2, currentVersion: 0 })
    expect(steps.find((s) => s.id === 'keys')).toMatchObject({ done: true, current: false })
    expect(steps.find((s) => s.id === 'version')).toMatchObject({ done: false, current: true })
    expect(steps.find((s) => s.id === 'integrate')).toMatchObject({ done: false, current: false })
  })

  it('已发版但无密钥：keys 仍是第一个未完成（顺序优先），integrate 未就绪', () => {
    const steps = deriveReadiness({ keyCount: 0, currentVersion: 3 })
    expect(steps.find((s) => s.id === 'keys')).toMatchObject({ done: false, current: true })
    expect(steps.find((s) => s.id === 'version')).toMatchObject({ done: true, current: false })
    expect(steps.find((s) => s.id === 'integrate')).toMatchObject({ done: false, current: false })
  })

  it('密钥+版本齐备：integrate 完成，全部就绪，无当前步骤', () => {
    const steps = deriveReadiness({ keyCount: 1, currentVersion: 5 })
    expect(steps.every((s) => s.done)).toBe(true)
    expect(steps.some((s) => s.current)).toBe(false)
    expect(isChannelReady(steps)).toBe(true)
  })
})

describe('readinessCompletedCount', () => {
  it('计已完成步骤数', () => {
    expect(readinessCompletedCount(deriveReadiness({ keyCount: 0, currentVersion: 0 }))).toBe(1)
    expect(readinessCompletedCount(deriveReadiness({ keyCount: 2, currentVersion: 0 }))).toBe(2)
    expect(readinessCompletedCount(deriveReadiness({ keyCount: 2, currentVersion: 4 }))).toBe(4)
  })
})

describe('isChannelReady', () => {
  it('未全就绪时为 false', () => {
    expect(isChannelReady(deriveReadiness({ keyCount: 0, currentVersion: 0 }))).toBe(false)
    expect(isChannelReady(deriveReadiness({ keyCount: 1, currentVersion: 0 }))).toBe(false)
  })
})
