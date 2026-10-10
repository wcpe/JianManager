/**
 * logs-federation foundation 单测（FR-482）。
 * 覆盖 partial / offline / zero-vs-failure / nextCursor 语义 / 导出下载阻断。
 */
import { describe, expect, it } from 'vitest'
import {
  classifyLogViewState,
  exportBlockedKeyForReasons,
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
import { EXPORT_BLOCK_REASONS, PARTIAL_REASONS } from './types'
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

describe('导出预算上限文案（FR-480 导出闸）', () => {
  // 缺陷背景：导出上限原是编译期常量、前端导出又不传 limit，数据超 10000 行的
  // 用户只能看到笼统的「导出未通过完整性校验」，看不出是行数到顶。
  // 本组用例锁定「行数 / 字节」两条精确 reason 各自映射到专用文案。

  it('row_limit_exceeded → 行数上限专用文案', () => {
    expect(exportBlockedKeyForReasons([EXPORT_BLOCK_REASONS.ROW_LIMIT_EXCEEDED])).toBe(
      LOGS_FEDERATION_KEYS.exportBlockedRowLimit,
    )
    // 后端会同时带上笼统的 budget_exceeded，精确码优先。
    expect(
      exportBlockedKeyForReasons([
        EXPORT_BLOCK_REASONS.BUDGET_EXCEEDED,
        EXPORT_BLOCK_REASONS.ROW_LIMIT_EXCEEDED,
      ]),
    ).toBe(LOGS_FEDERATION_KEYS.exportBlockedRowLimit)
  })

  it('byte_budget_exceeded / artifact_byte_budget_exceeded → 体积上限专用文案', () => {
    expect(exportBlockedKeyForReasons([EXPORT_BLOCK_REASONS.BYTE_BUDGET_EXCEEDED])).toBe(
      LOGS_FEDERATION_KEYS.exportBlockedByteBudget,
    )
    expect(
      exportBlockedKeyForReasons([EXPORT_BLOCK_REASONS.ARTIFACT_BYTE_BUDGET_EXCEEDED]),
    ).toBe(LOGS_FEDERATION_KEYS.exportBlockedByteBudget)
  })

  it('行数优先于字节（两者同时超限时给可操作的那条）', () => {
    expect(
      exportBlockedKeyForReasons([
        EXPORT_BLOCK_REASONS.BYTE_BUDGET_EXCEEDED,
        EXPORT_BLOCK_REASONS.ROW_LIMIT_EXCEEDED,
      ]),
    ).toBe(LOGS_FEDERATION_KEYS.exportBlockedRowLimit)
  })

  it('非预算原因与空输入 → undefined（调用方走泛化文案）', () => {
    expect(exportBlockedKeyForReasons([])).toBeUndefined()
    expect(exportBlockedKeyForReasons(null)).toBeUndefined()
    expect(exportBlockedKeyForReasons(undefined)).toBeUndefined()
    expect(
      exportBlockedKeyForReasons([
        EXPORT_BLOCK_REASONS.COVERAGE_INCOMPLETE,
        EXPORT_BLOCK_REASONS.WORKER_OR_LIST_TRUNCATED,
      ]),
    ).toBeUndefined()
    expect(exportBlockedKeyForReasons(['quality_unresolved'])).toBeUndefined()
  })

  it('覆盖原因含 BUDGET_EXCEEDED → 预算专用文案（不再落到泛化 incomplete）', () => {
    const v = classifyLogViewState({
      ok: true,
      items: [{ id: 1 }],
      coverage: coverage({
        complete: false,
        partialReasons: [PARTIAL_REASONS.BUDGET_EXCEEDED],
      }),
    })
    expect(v.allowsExportDownload).toBe(false)
    expect(v.exportBlockedKey).toBe(LOGS_FEDERATION_KEYS.exportBlockedBudget)
  })

  it('新增的导出文案键在 zh / en 中都存在', () => {
    for (const key of [
      LOGS_FEDERATION_KEYS.exportBlockedRowLimit,
      LOGS_FEDERATION_KEYS.exportBlockedByteBudget,
      LOGS_FEDERATION_KEYS.exportBlockedBudget,
    ]) {
      expect(key.startsWith('logsFederation.export.')).toBe(true)
    }
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

describe('pending 未校验态（Coverage.Pending，首屏快路径）', () => {
  it('pending + 无具体原因 → verifying：不误报缺失、不宣称完整、导出入口保持开放', () => {
    const v = classifyLogViewState({
      ok: true,
      items: [{ id: 1 }],
      coverage: coverage({ complete: false, pending: true, partialReasons: [] }),
      nextCursor: 'cursor-1',
    })
    expect(v.state).toBe('verifying')
    expect(v.pendingVerification).toBe(true)
    // 不是失败、也不是「有缺失」的 partial。
    expect(v.isFailure).toBe(false)
    expect(v.isIncomplete).toBe(true)
    expect(v.bannerKey).toBe(LOGS_FEDERATION_KEYS.viewVerifying)
    // 空结果不得当完整成功；历史完整性不得宣称。
    expect(v.emptySuccessAllowed).toBe(false)
    expect(v.historyFullyEnumerated).toBe(false)
    // 导出由后端慢路径在产出前校验 ⇒ 入口开放（此前 pending 被当 partial，导出被永久锁死）。
    expect(v.allowsExportDownload).toBe(true)
    expect(v.exportBlockedKey).toBeUndefined()
  })

  it('pending 横幅：info 温和色、无「重试」误导动作、不阻断导出', () => {
    const v = classifyLogViewState({
      ok: true,
      items: [{ id: 1 }],
      coverage: coverage({ complete: false, pending: true, partialReasons: [] }),
    })
    const props = toCoverageBannerProps(v)
    expect(props.visible).toBe(true)
    expect(props.tone).toBe('info')
    expect(props.showRetry).toBe(false)
    expect(props.showExport).toBe(true)
    const affordance = resolveExportDownloadAffordance(null, v)
    expect(affordance.showDownload).toBe(true)
  })

  it('pending + 具体原因（BACKLOG）→ 原因优先，仍按 partial 阻断导出', () => {
    const v = classifyLogViewState({
      ok: true,
      items: [{ id: 1 }],
      coverage: coverage({
        complete: false,
        pending: true,
        partialReasons: [PARTIAL_REASONS.BACKLOG],
      }),
    })
    expect(v.state).toBe('backlog')
    expect(v.pendingVerification).toBe(false)
    expect(v.allowsExportDownload).toBe(false)
  })

  it('非 pending 的无原因 incomplete 仍走 partial（不误开导出、不误报「校验中」）', () => {
    const v = classifyLogViewState({
      ok: true,
      items: [{ id: 1 }],
      coverage: coverage({ complete: false, partialReasons: [] }),
    })
    expect(v.state).toBe('partial')
    expect(v.pendingVerification).toBe(false)
    expect(v.allowsExportDownload).toBe(false)
  })

  it('complete=true 与 pending 同现（矛盾输入）→ complete 优先，pending 被忽略', () => {
    const s = formatCoverageSummary(coverage({ complete: true, pending: true }))
    expect(s.pending).toBe(false)
    const v = classifyLogViewState({
      ok: true,
      items: [],
      coverage: coverage({ complete: true, pending: true }),
    })
    expect(v.state).toBe('ready_complete')
    expect(v.pendingVerification).toBe(false)
    expect(v.emptySuccessAllowed).toBe(true)
  })

  it('formatCoverageSummary：pending 原样透传，仍属不完整档且禁止空成功', () => {
    const s = formatCoverageSummary(coverage({ complete: false, pending: true, partialReasons: [] }))
    expect(s.pending).toBe(true)
    expect(s.status).toBe('partial')
    expect(s.emptySuccessAllowed).toBe(false)
  })
})
