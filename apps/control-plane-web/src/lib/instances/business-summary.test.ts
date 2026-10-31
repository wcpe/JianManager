import { describe, expect, it } from 'vitest'
import { sameBusinessStatus, summarizeBusiness } from './business-summary'
import type { BusinessStatus } from '@/api/businessStatus'

const st = (domains: Array<{ domain: string; available: boolean }>, stale = false): BusinessStatus =>
  ({ instanceId: 1, instanceName: 'i', probeConnected: true, fetchedAt: '', stale, domains } as BusinessStatus)

describe('summarizeBusiness', () => {
  it('无数据（undefined）→ unknown，不能显示成「未注册」', () => {
    expect(summarizeBusiness(undefined)).toEqual({ kind: 'unknown' })
  })

  it('0 个域 → none', () => {
    expect(summarizeBusiness(st([]))).toEqual({ kind: 'none' })
  })

  it('全部可用 → degraded=0', () => {
    const r = summarizeBusiness(st([
      { domain: 'a', available: true },
      { domain: 'b', available: true },
    ]))
    expect(r).toMatchObject({ kind: 'domains', ok: 2, total: 2, degraded: 0 })
  })

  it('部分降级 → degraded 计数正确（决定是否警示配色）', () => {
    const r = summarizeBusiness(st([
      { domain: 'a', available: true },
      { domain: 'b', available: false },
      { domain: 'c', available: false },
    ]))
    expect(r).toMatchObject({ kind: 'domains', ok: 1, total: 3, degraded: 2 })
  })

  it('stale 透出（用于 title 提示「探针不可达」）', () => {
    const r = summarizeBusiness(st([{ domain: 'a', available: true }], true))
    expect(r).toMatchObject({ stale: true })
  })
})

describe('sameBusinessStatus（memo 比较器）', () => {
  const a = st([{ domain: 'x', available: true }, { domain: 'y', available: false }])
  const same = st([{ domain: 'x', available: true }, { domain: 'y', available: false }])
  const diffOk = st([{ domain: 'x', available: false }, { domain: 'y', available: false }])
  const diffLen = st([{ domain: 'x', available: true }])

  it('引用相同 → 相等', () => {
    expect(sameBusinessStatus(a, a)).toBe(true)
  })

  it('内容相同但对象不同 → 相等（批量轮询每次都是新对象，否则比较器失效）', () => {
    expect(sameBusinessStatus(a, same)).toBe(true)
  })

  it('可用域数变了 → 不相等（这一格显示的就是它）', () => {
    expect(sameBusinessStatus(a, diffOk)).toBe(false)
  })

  it('域数量变了 → 不相等', () => {
    expect(sameBusinessStatus(a, diffLen)).toBe(false)
  })

  it('stale 变了 → 不相等', () => {
    expect(sameBusinessStatus(a, st([{ domain: 'x', available: true }, { domain: 'y', available: false }], true))).toBe(false)
  })

  it('callingCount 等不参与渲染的字段变化 → 仍相等（避免每次轮询重画整行）', () => {
    const withCounts = { ...a, domains: [
      { domain: 'x', available: true, dispatchCount: 999 },
      { domain: 'y', available: false, dispatchCount: 888 },
    ] } as BusinessStatus
    expect(sameBusinessStatus(a, withCounts)).toBe(true)
  })

  it('undefined 与非 undefined → 不相等', () => {
    expect(sameBusinessStatus(undefined, a)).toBe(false)
    expect(sameBusinessStatus(a, undefined)).toBe(false)
  })

  it('两边都 undefined → 相等', () => {
    expect(sameBusinessStatus(undefined, undefined)).toBe(true)
  })
})
