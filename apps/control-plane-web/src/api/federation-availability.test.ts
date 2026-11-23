import { describe, expect, it } from 'vitest'
import { isFederationUnavailable } from './logFederation'

describe('isFederationUnavailable', () => {
  const mk = (status?: number) => (status === undefined ? new Error('ECONNREFUSED') : { response: { status } })
  it('404/401/403 → 不可用（降级）', () => {
    for (const s of [404, 401, 403]) expect(isFederationUnavailable(mk(s))).toBe(true)
  })
  it('代理层 5xx → 不可用（CI 无后端时 vite 代理返回 500）', () => {
    for (const s of [500, 502, 503, 504]) expect(isFederationUnavailable(mk(s))).toBe(true)
  })
  it('无响应 → 不可用', () => { expect(isFederationUnavailable(mk())).toBe(true) })
  it('422 等业务错误 → 不降级（联邦可用但请求有误）', () => {
    for (const s of [400, 422, 429]) expect(isFederationUnavailable(mk(s))).toBe(false)
  })
})
