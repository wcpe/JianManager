import { describe, expect, it } from 'vitest'
import { buildFederationResult, mapFederationEnvelope, legacyDegradeEnvelope } from './logFederation'

describe('引擎未就绪 → 降级（生产修复验证）', () => {
  it('notes 含 ErrFederatedNotReady → federatedNotReady=true（可触发降级）', () => {
    const env = { notes: ['federated log query path not ready'] }
    expect(mapFederationEnvelope(env).federatedNotReady).toBe(true)
  })
  it('errorCode=ENGINE_NOT_READY → federatedNotReady=true', () => {
    expect(mapFederationEnvelope({ errorCode: 'ENGINE_NOT_READY' }).federatedNotReady).toBe(true)
  })
  it('federatedReady=false → federatedNotReady=true', () => {
    expect(mapFederationEnvelope({ federatedReady: false }).federatedNotReady).toBe(true)
  })
  it('降级信封 → degraded 且含 legacy 来源（平台日志可查）', () => {
    const r = buildFederationResult(legacyDegradeEnvelope(), { degraded: true })
    expect(r.degraded).toBe(true)
    expect(r.sourceTag).toBe('legacy')
  })
})
