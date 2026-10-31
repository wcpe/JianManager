/** 
 * api/logFederation 纯映射单测。
 * 覆盖 envelope→foundation 归一、ErrFederatedNotReady、404 降级、导出门禁结果。
 */
import { describe, expect, it } from 'vitest'
import {
  FEDERATED_NOT_READY_MESSAGE,
  FederatedExportIncompleteError,
  buildFederationResult,
  isFederatedNotReady,
  isHttp404,
  legacyDegradeEnvelope,
  mapFederationEnvelope,
  normalizeFederationCoverage,
  parseExportBlockedReasons,
  transportFailureEnvelope,
} from './logFederation'
import { PARTIAL_REASONS } from '@/lib/logs-federation'

describe('logFederation client mapping', () => {
  it('永久位置证据无损映射，旧Worker缺字段兼容，且不改变coverage/quality/导出闸', () => {
    const evidence = { worker_id: 'trusted-worker', storage_namespace: 'shared', from: '18446744073709551614', to: '18446744073709551615', reason_code: 'PERMANENTLY_LOST', operator: '实名', at_utc: '2026-10-08T00:00:00Z', log_source_id: 'inst:1/stdout', source_generation: 'gen-1' }
    const base = { sourceTag: 'federated' as const, items: [], coverage: { complete: true, targets: [] }, quality: { duplicate_quality: 'exact', stats_quality: 'exact' } }
    const old = buildFederationResult(base)
    const result = buildFederationResult({ ...base, ingest_position_gaps: [evidence, { ...evidence, log_source_id: 'inst:2/stdout' }] })
    expect(result.response.ingestPositionGaps).toHaveLength(2)
    expect(result.response.ingestPositionGaps?.[0]).toEqual({ workerId: 'trusted-worker', storageNamespace: 'shared', from: evidence.from, to: evidence.to, reasonCode: evidence.reason_code, operator: '实名', atUTC: evidence.at_utc, logSourceId: 'inst:1/stdout', sourceGeneration: 'gen-1' })
    expect(old.response.ingestPositionGaps).toEqual([])
    expect(result.response.coverage).toEqual(old.response.coverage)
    expect(result.response.quality).toEqual(old.response.quality)
    expect(result.exportAffordance).toEqual(old.exportAffordance)
  })
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

  it('门面 pending 响应：pending 原样保留，不塞 EXPORT_INCOMPLETE 占位', () => {
    const cov = normalizeFederationCoverage(
      { complete: false, pending: true, partialReasons: [], targets: [] },
      'federated',
    )
    expect(cov?.complete).toBe(false)
    expect(cov?.pending).toBe(true)
    // 未校验 ≠ 有缺失：不得把「导出不完整」占位原因套在 pending 上。
    expect(cov?.partialReasons).toEqual([])
  })

  it('非 pending 的无原因 incomplete：仍塞 EXPORT_INCOMPLETE 保底（既有行为不回退）', () => {
    const cov = normalizeFederationCoverage(
      { complete: false, partialReasons: [], targets: [] },
      'federated',
    )
    expect(cov?.pending).toBe(false)
    expect(cov?.partialReasons).toContain(PARTIAL_REASONS.EXPORT_INCOMPLETE)
  })

  it('门面 pending → verifying：横幅校验中、导出入口开放（后端闸把关）', () => {
    const result = buildFederationResult({
      sourceTag: 'federated',
      items: [{ id: 1 }],
      view: { view_id: 'cv-pending' },
      coverage: { complete: false, pending: true, partial_reasons: [], targets: [] },
    })
    expect(result.classified.state).toBe('verifying')
    expect(result.classified.pendingVerification).toBe(true)
    expect(result.banner.tone).toBe('info')
    expect(result.exportAffordance.showDownload).toBe(true)
  })

  it('空串 next_cursor 归一为 null（空串不是有效游标）', () => {
    const mapped = mapFederationEnvelope({
      sourceTag: 'federated',
      items: [],
      coverage: { complete: true, targets: [] },
      next_cursor: '',
    })
    expect(mapped.response.nextCursor).toBeNull()
  })

  it('导出闸阻断：解析 incomplete_reasons 并抛结构化错误（供 UI 展示原因）', () => {
    const body = JSON.stringify({
      artifact: null,
      export_incomplete: true,
      incomplete_reasons: ['quality_unresolved', 'coverage_incomplete', ''],
    })
    const reasons = parseExportBlockedReasons(body)
    // 空串原因被过滤，未知码原样保留。
    expect(reasons).toEqual(['quality_unresolved', 'coverage_incomplete'])
    const err = new FederatedExportIncompleteError(reasons)
    expect(err).toBeInstanceOf(Error)
    expect(err.name).toBe('FederatedExportIncompleteError')
    expect(err.reasons).toEqual(reasons)
    // 非法输入不抛错：返回空 reasons，调用方走泛化失败文案。
    expect(parseExportBlockedReasons('not-json')).toEqual([])
    expect(parseExportBlockedReasons('{}')).toEqual([])
    expect(parseExportBlockedReasons('{"incomplete_reasons": "x"}')).toEqual([])
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
