/**
 * logs-federation foundation 单测（FR-482）。
 * 覆盖 partial / offline / zero-vs-failure / nextCursor 语义 / 导出下载阻断。
 */
import { describe, expect, it } from 'vitest'
import {
  classifyLogViewState,
  formatCoverageSummary,
  mergeQualityIntoSummary,
  resolveExportDownloadAffordance,
  toCoverageBannerProps,
} from './helpers'
import {
  LOGS_FEDERATION_KEYS,
  LOGS_FEDERATION_LOCALES,
  reasonI18nKey,
} from './i18n'
import { PARTIAL_REASONS } from './types'
import type { Coverage, LogFederationResponse } from './types'

function coverage(partial: Partial<Coverage> = {}): Coverage {
  return {
    complete: true,
    partialReasons: [],
    targets: [],
    enumerationState: 'EXHAUSTED',
    ...partial,
  }
}

describe('formatCoverageSummary（FR-482 foundation）', () => {
  it('complete=true 且无 partial → complete，允许成功空态', () => {
    const s = formatCoverageSummary(coverage())
    expect(s.status).toBe('complete')
    expect(s.complete).toBe(true)
    expect(s.emptySuccessAllowed).toBe(true)
    expect(s.summaryKey).toBe(LOGS_FEDERATION_KEYS.coverageComplete)
    expect(s.reasons).toEqual([])
  })

  it('complete=false → partial，禁止空成功（失败/不完整 ≠ 无日志）', () => {
    const s = formatCoverageSummary(
      coverage({
        complete: false,
        partialReasons: [PARTIAL_REASONS.OFFLINE],
        targets: [
          {
            targetId: 'n1',
            status: 'OFFLINE',
            included: false,
            reason: PARTIAL_REASONS.OFFLINE,
          },
        ],
      }),
    )
    expect(s.status).toBe('partial')
    expect(s.complete).toBe(false)
    expect(s.emptySuccessAllowed).toBe(false)
    expect(s.summaryKey).toBe(LOGS_FEDERATION_KEYS.coveragePartial)
    expect(s.reasons).toContain(PARTIAL_REASONS.OFFLINE)
    expect(s.targetStats.offline).toBe(1)
  })

  it('coverage=null（查询失败无 body）→ failed，绝不映射为空成功', () => {
    for (const input of [null, undefined] as const) {
      const s = formatCoverageSummary(input)
      expect(s.status).toBe('failed')
      expect(s.complete).toBe(false)
      expect(s.emptySuccessAllowed).toBe(false)
      expect(s.summaryKey).toBe(LOGS_FEDERATION_KEYS.coverageFailed)
    }
  })

  it('zero-vs-failure：complete=false + 空 targets ≠ complete 空成功', () => {
    const s = formatCoverageSummary(coverage({ complete: false, targets: [] }))
    expect(s.emptySuccessAllowed).toBe(false)
    expect(s.status).toBe('partial')
  })

  it('complete=true 但 partialReasons 非空 → 降级 partial（防御）', () => {
    const s = formatCoverageSummary(
      coverage({
        complete: true,
        partialReasons: [PARTIAL_REASONS.BACKLOG],
      }),
    )
    expect(s.status).toBe('partial')
    expect(s.emptySuccessAllowed).toBe(false)
  })

  it('complete=true 但目标被排除 → 降级 partial', () => {
    const s = formatCoverageSummary(
      coverage({
        complete: true,
        targets: [
          { targetId: 'n1', status: 'OFFLINE', included: false },
          { targetId: 'n2', status: 'ONLINE', included: true },
        ],
      }),
    )
    expect(s.status).toBe('partial')
    expect(s.emptySuccessAllowed).toBe(false)
    expect(s.targetStats.excluded).toBe(1)
  })

  it('quality.duplicateQuality=UNRESOLVED → 即使 complete=true 也不得当完整成功', () => {
    const s = formatCoverageSummary(coverage({ complete: true }), {
      duplicateQuality: 'UNRESOLVED',
      statsQuality: 'APPROXIMATE',
    })
    expect(s.status).toBe('partial')
    expect(s.complete).toBe(false)
    expect(s.emptySuccessAllowed).toBe(false)
    expect(s.duplicateUnresolved).toBe(true)
  })

  it('legacy 目标计入 stats 并携带 reason key', () => {
    const s = formatCoverageSummary(
      coverage({
        complete: false,
        partialReasons: [PARTIAL_REASONS.LEGACY],
        targets: [
          {
            targetId: 'legacy-1',
            status: 'LEGACY',
            included: false,
            reason: PARTIAL_REASONS.LEGACY,
            legacyRange: { from: '2020-01-01T00:00:00Z', to: '2023-01-01T00:00:00Z' },
          },
        ],
      }),
    )
    expect(s.targetStats.legacy).toBe(1)
    expect(s.reasonKeys).toEqual([reasonI18nKey(PARTIAL_REASONS.LEGACY)])
    expect(s.emptySuccessAllowed).toBe(false)
  })
})

describe('classifyLogViewState（FR-482 foundation）', () => {
  it('传输失败 ok=false → query_failed，非空成功', () => {
    const res: LogFederationResponse = {
      ok: false,
      errorCode: 'INTERNAL',
      errorMessage: 'boom',
      items: [],
      coverage: coverage({ complete: true }), // 残留 complete 也不得当成功
    }
    const v = classifyLogViewState(res)
    expect(v.state).toBe('query_failed')
    expect(v.isFailure).toBe(true)
    expect(v.emptySuccessAllowed).toBe(false)
    expect(v.allowsExportDownload).toBe(false)
    expect(v.bannerKey).toBe(LOGS_FEDERATION_KEYS.viewQueryFailed)
    expect(v.exportBlockedKey).toBe(LOGS_FEDERATION_KEYS.exportBlockedFailed)
  })

  it('null 响应 → query_failed', () => {
    const v = classifyLogViewState(null)
    expect(v.state).toBe('query_failed')
    expect(v.emptySuccessAllowed).toBe(false)
  })

  it('offline partial → offline 态，0 条 items 不显示空成功', () => {
    const v = classifyLogViewState({
      ok: true,
      items: [],
      coverage: coverage({
        complete: false,
        partialReasons: [PARTIAL_REASONS.OFFLINE],
        targets: [{ targetId: 'n1', status: 'OFFLINE', included: false }],
      }),
    })
    expect(v.state).toBe('offline')
    expect(v.isIncomplete).toBe(true)
    expect(v.emptySuccessAllowed).toBe(false)
    expect(v.allowsExportDownload).toBe(false)
    expect(v.bannerKey).toBe(LOGS_FEDERATION_KEYS.viewOffline)
  })

  it('ENGINE_NOT_READY / LOG_UNSUPPORTED → engine_not_ready', () => {
    const v = classifyLogViewState({
      ok: true,
      items: [],
      coverage: coverage({
        complete: false,
        partialReasons: [PARTIAL_REASONS.ENGINE_NOT_READY],
      }),
    })
    expect(v.state).toBe('engine_not_ready')
    expect(v.isFailure).toBe(true)
    expect(v.emptySuccessAllowed).toBe(false)
  })

  it('LOG_UNSUPPORTED → engine_not_ready（老 Worker Unimplemented）', () => {
    const v = classifyLogViewState({
      ok: true,
      errorCode: PARTIAL_REASONS.LOG_UNSUPPORTED,
      items: [],
      coverage: coverage({ complete: false, partialReasons: [PARTIAL_REASONS.LOG_UNSUPPORTED] }),
    })
    expect(v.state).toBe('engine_not_ready')
    expect(v.emptySuccessAllowed).toBe(false)
    expect(v.allowsExportDownload).toBe(false)
  })

  it('rehydrate failed / backlog / truncated / legacy 各自独立呈现', () => {
    const cases: Array<[string, LogFederationResponse['coverage']]> = [
      [
        'rehydrate_failed',
        coverage({
          complete: false,
          partialReasons: [PARTIAL_REASONS.REHYDRATE_FAILED],
        }),
      ],
      [
        'backlog',
        coverage({ complete: false, partialReasons: [PARTIAL_REASONS.BACKLOG] }),
      ],
      [
        'truncated',
        coverage({ complete: false, partialReasons: [PARTIAL_REASONS.TRUNCATED] }),
      ],
      [
        'legacy',
        coverage({ complete: false, partialReasons: [PARTIAL_REASONS.LEGACY] }),
      ],
    ]
    for (const [expected, cov] of cases) {
      const v = classifyLogViewState({ ok: true, items: [], coverage: cov })
      expect(v.state).toBe(expected)
      expect(v.emptySuccessAllowed).toBe(false)
    }
  })

  it('VIEW_STALE（errorCode）→ view_stale，失败且不可下载', () => {
    const v = classifyLogViewState({
      ok: true,
      errorCode: PARTIAL_REASONS.VIEW_STALE,
      items: [{ id: 1 }],
      coverage: coverage({ complete: false, partialReasons: [PARTIAL_REASONS.VIEW_STALE] }),
    })
    expect(v.state).toBe('view_stale')
    expect(v.isFailure).toBe(true)
    expect(v.allowsExportDownload).toBe(false)
    expect(v.exportBlockedKey).toBe(LOGS_FEDERATION_KEYS.exportBlockedViewStale)
  })

  it('合法零结果：complete=true + items=[] → ready_complete 且 emptySuccessAllowed', () => {
    const v = classifyLogViewState({
      ok: true,
      items: [],
      coverage: coverage({ complete: true, enumerationState: 'EXHAUSTED' }),
      quality: { duplicateQuality: 'EXACT', statsQuality: 'EXACT' },
    })
    expect(v.state).toBe('ready_complete')
    expect(v.emptySuccessAllowed).toBe(true)
    expect(v.bannerKey).toBe(LOGS_FEDERATION_KEYS.viewReadyCompleteEmpty)
  })

  it('zero-vs-failure：同样 items=[]，offline 不完整 vs complete 空态可区分', () => {
    const failureEmpty = classifyLogViewState({
      ok: true,
      items: [],
      coverage: coverage({
        complete: false,
        partialReasons: [PARTIAL_REASONS.OFFLINE],
      }),
    })
    const trueEmpty = classifyLogViewState({
      ok: true,
      items: [],
      coverage: coverage({ complete: true }),
    })
    expect(failureEmpty.emptySuccessAllowed).toBe(false)
    expect(trueEmpty.emptySuccessAllowed).toBe(true)
    expect(failureEmpty.state).not.toBe(trueEmpty.state)
  })

  it('nextCursor=null 不单独表示全量历史完整（partial）', () => {
    const v = classifyLogViewState({
      ok: true,
      items: [{ id: 1 }],
      nextCursor: null,
      coverage: coverage({
        complete: false,
        partialReasons: [PARTIAL_REASONS.OFFLINE],
        enumerationState: 'EXHAUSTED',
      }),
    })
    expect(v.viewEnumerationEnded).toBe(true)
    expect(v.historyFullyEnumerated).toBe(false)
  })

  it('nextCursor=null + complete=true → 可 historyFullyEnumerated', () => {
    const v = classifyLogViewState({
      ok: true,
      items: [{ id: 1 }],
      nextCursor: null,
      coverage: coverage({ complete: true, enumerationState: 'EXHAUSTED' }),
    })
    expect(v.viewEnumerationEnded).toBe(true)
    expect(v.historyFullyEnumerated).toBe(true)
  })

  it('nextCursor=null 但 enumerationState=OPEN → view 枚举未必结束，更非全量完整', () => {
    const v = classifyLogViewState({
      ok: true,
      items: [{ id: 1 }],
      nextCursor: null,
      coverage: coverage({ complete: false, enumerationState: 'OPEN' }),
    })
    // null cursor + OPEN：契约上 null 只说明当前页游标用尽；complete=false 仍否决全量
    expect(v.historyFullyEnumerated).toBe(false)
  })

  it('duplicateQuality=UNRESOLVED 阻断导出，即使 coverage.complete', () => {
    const v = classifyLogViewState({
      ok: true,
      items: [{ id: 1 }],
      coverage: coverage({ complete: true }),
      quality: { duplicateQuality: 'UNRESOLVED', statsQuality: 'APPROXIMATE' },
      exportStatus: 'READY',
      exportVerified: true,
    })
    expect(v.coverage.duplicateUnresolved).toBe(true)
    expect(v.allowsExportDownload).toBe(false)
    expect(v.exportBlockedKey).toBe(LOGS_FEDERATION_KEYS.exportBlockedUnresolved)
  })
})

describe('resolveExportDownloadAffordance（FR-482）', () => {
  it('覆盖完整 + 校验通过 → showDownload=true，并提示下载前复核权限', () => {
    const res: LogFederationResponse = {
      ok: true,
      items: [{ id: 1 }],
      coverage: coverage({ complete: true }),
      quality: { duplicateQuality: 'EXACT', statsQuality: 'EXACT' },
      exportStatus: 'READY',
      exportVerified: true,
    }
    const a = resolveExportDownloadAffordance(res)
    expect(a.showDownload).toBe(true)
    expect(a.allowedKey).toBe(LOGS_FEDERATION_KEYS.exportAllowed)
  })

  it('覆盖不完整 → 无下载 affordance', () => {
    const a = resolveExportDownloadAffordance({
      ok: true,
      items: [{ id: 1 }],
      coverage: coverage({
        complete: false,
        partialReasons: [PARTIAL_REASONS.BACKLOG],
      }),
      exportStatus: 'READY',
      exportVerified: true,
    })
    expect(a.showDownload).toBe(false)
    expect(a.blockedKey).toBe(LOGS_FEDERATION_KEYS.exportBlockedIncomplete)
  })

  it('查询失败 → 无下载', () => {
    const a = resolveExportDownloadAffordance({
      ok: false,
      items: [],
      coverage: null,
    })
    expect(a.showDownload).toBe(false)
    expect(a.blockedKey).toBe(LOGS_FEDERATION_KEYS.exportBlockedFailed)
  })

  it('exportVerified=false / PENDING / FAILED → 无下载', () => {
    for (const patch of [
      { exportStatus: 'PENDING' as const, exportVerified: false },
      { exportStatus: 'FAILED' as const, exportVerified: true },
      { exportStatus: 'INCOMPLETE' as const, exportVerified: true },
    ]) {
      const a = resolveExportDownloadAffordance({
        ok: true,
        items: [{ id: 1 }],
        coverage: coverage({ complete: true }),
        ...patch,
      })
      expect(a.showDownload).toBe(false)
    }
  })

  it('VIEW_STALE → 无下载（不得下载成功附件）', () => {
    const a = resolveExportDownloadAffordance({
      ok: true,
      errorCode: PARTIAL_REASONS.VIEW_STALE,
      items: [],
      coverage: coverage({
        complete: false,
        partialReasons: [PARTIAL_REASONS.VIEW_STALE],
      }),
      exportStatus: 'READY',
      exportVerified: true,
    })
    expect(a.showDownload).toBe(false)
    expect(a.blockedKey).toBe(LOGS_FEDERATION_KEYS.exportBlockedViewStale)
  })

  it('PERMISSION_REVOKED → 无下载', () => {
    const a = resolveExportDownloadAffordance({
      ok: true,
      errorCode: PARTIAL_REASONS.PERMISSION_REVOKED,
      items: [{ id: 1 }],
      coverage: coverage({ complete: true }),
      exportStatus: 'READY',
      exportVerified: true,
    })
    expect(a.showDownload).toBe(false)
    expect(a.blockedKey).toBe(LOGS_FEDERATION_KEYS.exportBlockedPermission)
  })
})

describe('toCoverageBannerProps / mergeQualityIntoSummary / i18n', () => {
  it('partial 横幅可见且不展示导出下载', () => {
    const v = classifyLogViewState({
      ok: true,
      items: [],
      coverage: coverage({
        complete: false,
        partialReasons: [PARTIAL_REASONS.OFFLINE, PARTIAL_REASONS.BACKLOG],
      }),
    })
    const props = toCoverageBannerProps(v)
    expect(props.visible).toBe(true)
    expect(props.tone).toBe('warning')
    expect(props.showExport).toBe(false)
    expect(props.reasonKeys.length).toBe(2)
  })

  it('mergeQualityIntoSummary：UNRESOLVED 把 complete 降为 partial', () => {
    const base = formatCoverageSummary(coverage({ complete: true }))
    expect(base.status).toBe('complete')
    const merged = mergeQualityIntoSummary(base, {
      duplicateQuality: 'UNRESOLVED',
      statsQuality: 'EXACT',
    })
    expect(merged.status).toBe('partial')
    expect(merged.emptySuccessAllowed).toBe(false)
  })

  it('i18n zh/en logsFederation 键对齐', () => {
    function flatten(obj: Record<string, unknown>, prefix = ''): Set<string> {
      const out = new Set<string>()
      for (const [k, v] of Object.entries(obj)) {
        const path = prefix ? `${prefix}.${k}` : k
        if (v && typeof v === 'object') {
          for (const nested of flatten(v as Record<string, unknown>, path)) out.add(nested)
        } else {
          out.add(path)
        }
      }
      return out
    }
    const zh = flatten(LOGS_FEDERATION_LOCALES.zh as unknown as Record<string, unknown>)
    const en = flatten(LOGS_FEDERATION_LOCALES.en as unknown as Record<string, unknown>)
    expect([...zh].sort()).toEqual([...en].sort())
    // LOGS_FEDERATION_KEYS 中的 key 都必须能在 zh 树中解析
    for (const key of Object.values(LOGS_FEDERATION_KEYS)) {
      expect(zh.has(key)).toBe(true)
      expect(en.has(key)).toBe(true)
    }
  })

  it('reasonI18nKey：已登记码与未知码', () => {
    expect(reasonI18nKey(PARTIAL_REASONS.ENGINE_NOT_READY)).toBe(
      'logsFederation.reasons.engineNotReady',
    )
    expect(reasonI18nKey('SOME_FUTURE_CODE')).toBe('logsFederation.reasons.unknown')
  })
})
