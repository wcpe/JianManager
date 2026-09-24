/**
 * api/logFederation 纯映射单测（FR-481）。
 * 覆盖 envelope→foundation 归一、ErrFederatedNotReady、404 降级、导出门禁结果。
 */
import { describe, expect, it } from 'vitest'
import {
  FEDERATED_NOT_READY_MESSAGE,
  buildFederationResult,
  isFederatedNotReady,
  isHttp404,
  legacyDegradeEnvelope,
  mapFederationEnvelope,
  normalizeFederationCoverage,
  transportFailureEnvelope,
} from './logFederation'
import { PARTIAL_REASONS } from '@/lib/logs-federation'

describe('logFederation client mapping（FR-481）', () => {
  it('isHttp404 / isFederatedNotReady', () => {
    expect(isHttp404({ response: { status: 404 } })).toBe(true)
    expect(isHttp404({ response: { status: 500 } })).toBe(false)
    expect(isHttp404(new Error('network'))).toBe(false)
    expect(isFederatedNotReady([FEDERATED_NOT_READY_MESSAGE])).toBe(true)
    expect(isFederatedNotReady([PARTIAL_REASONS.ENGINE_NOT_READY])).toBe(true)
    expect(isFederatedNotReady(['other note'])).toBe(false)
    expect(isFederatedNotReady(null)).toBe(false)
  })

  it('legacy coverage body → complete=false + LEGACY reason', () => {
    const cov = normalizeFederationCoverage(
      {
        sourceTag: 'legacy',
        complete: false,
        fromTime: '2020-01-01T00:00:00Z',
        toTime: '2023-01-01T00:00:00Z',
      },
      'legacy',
    )
    expect(cov?.complete).toBe(false)
    expect(cov?.partialReasons).toContain(PARTIAL_REASONS.LEGACY)
  })

  it('legacy 即便误传 complete=true 也不得当完整成功', () => {
    const cov = normalizeFederationCoverage({ complete: true }, 'legacy')
    expect(cov?.complete).toBe(false)
  })

  it('ErrFederatedNotReady → engine_not_ready + 无导出下载', () => {
    const result = buildFederationResult({
      sourceTag: 'federated',
      federatedReady: false,
      items: [],
      notes: [FEDERATED_NOT_READY_MESSAGE],
    })
    expect(result.federatedNotReady).toBe(true)
    expect(result.classified.state).toBe('engine_not_ready')
    expect(result.classified.emptySuccessAllowed).toBe(false)
    expect(result.exportAffordance.showDownload).toBe(false)
  })

  it('404 降级 → degraded + sourceTag=legacy + banner 可见语义', () => {
    const result = buildFederationResult(legacyDegradeEnvelope(), { degraded: true })
    expect(result.degraded).toBe(true)
    expect(result.available).toBe(false)
    expect(result.sourceTag).toBe('legacy')
    expect(result.classified.emptySuccessAllowed).toBe(false)
    expect(result.classified.state).toBe('legacy')
  })

  it('传输失败 envelope → query_failed，非空成功', () => {
    const result = buildFederationResult(transportFailureEnvelope('boom'))
    expect(result.classified.state).toBe('query_failed')
    expect(result.classified.isFailure).toBe(true)
    expect(result.classified.emptySuccessAllowed).toBe(false)
    expect(result.exportAffordance.showDownload).toBe(false)

    // items=[] 且 coverage 缺失时合成不完整 coverage，仍禁止空成功。
    const bare = mapFederationEnvelope({ items: [], coverage: null, notes: ['x'] })
    expect(bare.response.coverage?.complete).toBe(false)
    const failed = buildFederationResult({ items: [], coverage: null })
    expect(failed.classified.emptySuccessAllowed).toBe(false)
  })

  it('partial coverage → exportAffordance.showDownload=false', () => {
    const result = buildFederationResult({
      sourceTag: 'federated',
      items: [{ id: 1 }],
      coverage: {
        complete: false,
        partialReasons: ['OFFLINE'],
        targets: [{ targetId: 'n1', status: 'OFFLINE', included: false }],
      },
    })
    expect(result.classified.state).toBe('offline')
    expect(result.banner.visible).toBe(true)
    expect(result.exportAffordance.showDownload).toBe(false)
  })

  it('complete + exportVerified → showDownload=true', () => {
    const result = buildFederationResult({
      sourceTag: 'federated',
      items: [{ id: 1 }],
      coverage: { complete: true, partialReasons: [], targets: [] },
      exportStatus: 'READY',
      exportVerified: true,
    })
    expect(result.classified.state).toBe('ready_complete')
    expect(result.exportAffordance.showDownload).toBe(true)
  })

	it('映射固定 View 的 next_cursor 与 exhausted', () => {
		const result = buildFederationResult({
			sourceTag: 'federated', items: [], exhausted: false, next_cursor: 'cursor-page-2',
			view: { view_id: 'cv-1' }, coverage: { complete: true, targets: [], enumeration_state: 'OPEN' },
		})
		expect(result.response.viewId).toBe('cv-1')
		expect(result.response.nextCursor).toBe('cursor-page-2')
		expect(result.response.exhausted).toBe(false)
	})
})
