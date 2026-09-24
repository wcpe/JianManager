/**
 * 日志联邦覆盖 / 失败态纯函数（FR-481 foundation）。
 *
 * 硬约束（spec §3.1 / 验收）：
 * - **失败不得显示为空成功**：formatCoverageSummary / classifyLogViewState 在
 *   传输失败、coverage 缺失、complete=false 时一律 emptySuccessAllowed=false。
 * - **nextCursor=null 不单独代表全量历史结束**：historyFullyEnumerated 还要求
 *   coverage.complete 且无缺口类 reason。
 * - **覆盖不完整 → 无下载 affordance**：allowsExportDownload / showDownload 在
 *   partial、VIEW_STALE、DUPLICATE_UNRESOLVED、校验未通过时均为 false。
 *
 * 本模块无 React / i18n 运行时依赖；只产出 key 与结构化状态。
 */
import { LOGS_FEDERATION_KEYS, reasonI18nKey } from './i18n'
import { PARTIAL_REASONS } from './types'
import type {
  ClassifiedLogViewState,
  Coverage,
  CoverageBannerProps,
  CoverageTargetStats,
  ExportDownloadAffordance,
  FormattedCoverageSummary,
  LogFederationResponse,
  LogViewState,
  PartialReason,
  Quality,
} from './types'

/** 空目标统计。 */
const EMPTY_STATS: CoverageTargetStats = {
  total: 0,
  included: 0,
  offline: 0,
  notReady: 0,
  legacy: 0,
  archiveMissing: 0,
  excluded: 0,
}

/** reason 分类优先级：越靠前越主导 UI 状态。 */
const REASON_PRIORITY: readonly string[] = [
  PARTIAL_REASONS.VIEW_STALE,
  PARTIAL_REASONS.LOG_UNSUPPORTED,
  PARTIAL_REASONS.ENGINE_NOT_READY,
  PARTIAL_REASONS.PERMISSION_REVOKED,
  PARTIAL_REASONS.REHYDRATE_FAILED,
  PARTIAL_REASONS.ARCHIVE_MISSING,
  PARTIAL_REASONS.OFFLINE,
  PARTIAL_REASONS.BACKLOG,
  PARTIAL_REASONS.TRUNCATED,
  PARTIAL_REASONS.GAP,
  PARTIAL_REASONS.RECOVERY_REQUIRED,
  PARTIAL_REASONS.DUPLICATE_UNRESOLVED,
  PARTIAL_REASONS.LEGACY,
  PARTIAL_REASONS.BUDGET_EXCEEDED,
  PARTIAL_REASONS.CANCELLED,
  PARTIAL_REASONS.EXPORT_INCOMPLETE,
]

/** 缺口 / 未覆盖类 reason：即使 cursor 枚举结束也不能宣称全量历史完整。 */
const HISTORY_GAP_REASONS = new Set<string>([
  PARTIAL_REASONS.OFFLINE,
  PARTIAL_REASONS.ENGINE_NOT_READY,
  PARTIAL_REASONS.LOG_UNSUPPORTED,
  PARTIAL_REASONS.ARCHIVE_MISSING,
  PARTIAL_REASONS.REHYDRATE_FAILED,
  PARTIAL_REASONS.BACKLOG,
  PARTIAL_REASONS.GAP,
  PARTIAL_REASONS.TRUNCATED,
  PARTIAL_REASONS.VIEW_STALE,
  PARTIAL_REASONS.RECOVERY_REQUIRED,
  PARTIAL_REASONS.BUDGET_EXCEEDED,
  PARTIAL_REASONS.DUPLICATE_UNRESOLVED,
  PARTIAL_REASONS.LEGACY,
])

/** 视图状态 → 横幅 i18n key。 */
const VIEW_BANNER_KEY: Record<LogViewState, string> = {
  query_failed: LOGS_FEDERATION_KEYS.viewQueryFailed,
  view_stale: LOGS_FEDERATION_KEYS.viewStale,
  engine_not_ready: LOGS_FEDERATION_KEYS.viewEngineNotReady,
  rehydrate_failed: LOGS_FEDERATION_KEYS.viewRehydrateFailed,
  offline: LOGS_FEDERATION_KEYS.viewOffline,
  archive_missing: LOGS_FEDERATION_KEYS.viewArchiveMissing,
  backlog: LOGS_FEDERATION_KEYS.viewBacklog,
  truncated: LOGS_FEDERATION_KEYS.viewTruncated,
  legacy: LOGS_FEDERATION_KEYS.viewLegacy,
  gap: LOGS_FEDERATION_KEYS.viewGap,
  partial: LOGS_FEDERATION_KEYS.viewPartial,
  ready_complete: LOGS_FEDERATION_KEYS.viewReadyComplete,
  unknown: LOGS_FEDERATION_KEYS.viewUnknown,
}

/** 视图状态 → 建议动作 keys。 */
const VIEW_ACTION_KEYS: Record<LogViewState, string[]> = {
  query_failed: [LOGS_FEDERATION_KEYS.actionRetry],
  view_stale: [LOGS_FEDERATION_KEYS.actionReopenView, LOGS_FEDERATION_KEYS.actionRetry],
  engine_not_ready: [LOGS_FEDERATION_KEYS.actionRetry, LOGS_FEDERATION_KEYS.actionOpenNodes],
  rehydrate_failed: [LOGS_FEDERATION_KEYS.actionStartRehydrate, LOGS_FEDERATION_KEYS.actionRetry],
  offline: [LOGS_FEDERATION_KEYS.actionOpenNodes, LOGS_FEDERATION_KEYS.actionRequestOnline],
  archive_missing: [LOGS_FEDERATION_KEYS.actionStartRehydrate],
  backlog: [LOGS_FEDERATION_KEYS.actionRetry],
  truncated: [LOGS_FEDERATION_KEYS.actionRetry],
  legacy: [],
  gap: [LOGS_FEDERATION_KEYS.actionRetry],
  partial: [LOGS_FEDERATION_KEYS.actionRetry],
  ready_complete: [],
  unknown: [LOGS_FEDERATION_KEYS.actionRetry, LOGS_FEDERATION_KEYS.actionContactAdmin],
}

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

/** 规范化 partialReasons：过滤空值，保留未知字符串码。 */
function normalizeReasons(raw: unknown): PartialReason[] {
  if (!Array.isArray(raw)) return []
  const out: PartialReason[] = []
  for (const r of raw) {
    if (typeof r === 'string' && r.length > 0) out.push(r)
  }
  return out
}

/** 从 targets 统计覆盖情况。 */
function summarizeTargets(coverage: Coverage | null | undefined): CoverageTargetStats {
  if (!coverage || !Array.isArray(coverage.targets)) return { ...EMPTY_STATS }
  const stats: CoverageTargetStats = { ...EMPTY_STATS, total: coverage.targets.length }
  for (const t of coverage.targets) {
    if (!isPlainObject(t)) continue
    const status = typeof t.status === 'string' ? t.status : 'UNKNOWN'
    const included = t.included === true || (t.included === undefined && status === 'ONLINE')
    if (included) stats.included += 1
    else stats.excluded += 1
    if (status === 'OFFLINE' || t.reason === PARTIAL_REASONS.OFFLINE) stats.offline += 1
    if (status === 'NOT_READY' || t.reason === PARTIAL_REASONS.ENGINE_NOT_READY) stats.notReady += 1
    if (status === 'LEGACY' || t.reason === PARTIAL_REASONS.LEGACY) stats.legacy += 1
    if (status === 'ARCHIVE_MISSING' || t.reason === PARTIAL_REASONS.ARCHIVE_MISSING) {
      stats.archiveMissing += 1
    }
  }
  return stats
}

function hasDuplicateUnresolved(
  reasons: PartialReason[],
  quality?: Quality | null,
): boolean {
  if (reasons.includes(PARTIAL_REASONS.DUPLICATE_UNRESOLVED)) return true
  const dq = typeof quality?.duplicateQuality === 'string' ? quality.duplicateQuality.toUpperCase() : ''
  return dq === 'UNRESOLVED' || dq === 'CONFLICT'
}

function pickPrimaryReason(reasons: PartialReason[]): PartialReason | undefined {
  for (const known of REASON_PRIORITY) {
    const hit = reasons.find((r) => r === known)
    if (hit) return hit
  }
  return reasons[0]
}

/**
 * 汇总 Coverage → 结构化横幅数据。
 *
 * **契约**：入参为 null/undefined（典型：请求失败后无 body）或 complete 非 true 时，
 * 返回的 `emptySuccessAllowed` 必须为 false；绝不把 failed query 映射成
 * complete + empty 的成功空态。
 *
 * quality 不在本函数签名内；重复未对账请通过
 * `classifyLogViewState({ coverage, quality })` 或 {@link mergeQualityIntoSummary} 处理。
 */
export function formatCoverageSummary(
  coverage: Coverage | null | undefined,
  quality?: Quality | null,
): FormattedCoverageSummary {
  // 失败 / 无 coverage body：明确 failed，禁止空成功。
  if (
    coverage === null ||
    coverage === undefined ||
    typeof coverage !== 'object' ||
    Array.isArray(coverage)
  ) {
    return {
      status: 'failed',
      complete: false,
      emptySuccessAllowed: false,
      summaryKey: LOGS_FEDERATION_KEYS.coverageFailed,
      reasons: [],
      reasonKeys: [],
      targetStats: { ...EMPTY_STATS },
      duplicateUnresolved: false,
    }
  }

  const cov = coverage as Coverage
  const reasons = normalizeReasons(cov.partialReasons)
  const targetStats = summarizeTargets(cov)
  const enumerationState = cov.enumerationState
  const duplicateUnresolved = hasDuplicateUnresolved(reasons, quality)

  const completeFlag = cov.complete === true
  // 防御：complete=true 但仍有 partial_reasons / 目标未纳入 / 重复未解析 → 不得当完整成功。
  const hasExcluded = targetStats.total > 0 && targetStats.excluded > 0
  const effectiveComplete =
    completeFlag && reasons.length === 0 && !duplicateUnresolved && !hasExcluded

  if (!completeFlag) {
    // complete=false 或缺失（undefined）一律 partial 语义，禁止空成功。
    return {
      status: 'partial',
      complete: false,
      emptySuccessAllowed: false,
      summaryKey: LOGS_FEDERATION_KEYS.coveragePartial,
      reasons,
      reasonKeys: reasons.map(reasonI18nKey),
      targetStats,
      enumerationState,
      duplicateUnresolved,
    }
  }

  if (!effectiveComplete) {
    return {
      status: 'partial',
      complete: false,
      emptySuccessAllowed: false,
      summaryKey: LOGS_FEDERATION_KEYS.coveragePartial,
      reasons: reasons.length > 0 ? reasons : [PARTIAL_REASONS.EXPORT_INCOMPLETE],
      reasonKeys: (reasons.length > 0 ? reasons : [PARTIAL_REASONS.EXPORT_INCOMPLETE]).map(
        reasonI18nKey,
      ),
      targetStats,
      enumerationState,
      duplicateUnresolved,
    }
  }

  return {
    status: 'complete',
    complete: true,
    // complete 时允许 UI 在 items.length===0 呈现成功空态（仍需 items 确认）。
    emptySuccessAllowed: true,
    summaryKey: LOGS_FEDERATION_KEYS.coverageComplete,
    reasons: [],
    reasonKeys: [],
    targetStats,
    enumerationState,
    duplicateUnresolved: false,
  }
}

/**
 * 将 quality 并入已格式化的 coverage 摘要（重复未解析 → 降级为 partial）。
 * formatCoverageSummary 已可直接传 quality；本函数供已缓存 summary 的调用方使用。
 */
export function mergeQualityIntoSummary(
  summary: FormattedCoverageSummary,
  quality?: Quality | null,
): FormattedCoverageSummary {
  if (!quality || quality.duplicateQuality !== 'UNRESOLVED') return summary
  if (summary.duplicateUnresolved && summary.status === 'partial') return summary
  return {
    ...summary,
    status: 'partial',
    complete: false,
    emptySuccessAllowed: false,
    summaryKey: LOGS_FEDERATION_KEYS.coveragePartial,
    duplicateUnresolved: true,
    reasons: summary.reasons.includes(PARTIAL_REASONS.DUPLICATE_UNRESOLVED)
      ? summary.reasons
      : [...summary.reasons, PARTIAL_REASONS.DUPLICATE_UNRESOLVED],
    reasonKeys: (
      summary.reasons.includes(PARTIAL_REASONS.DUPLICATE_UNRESOLVED)
        ? summary.reasons
        : [...summary.reasons, PARTIAL_REASONS.DUPLICATE_UNRESOLVED]
    ).map(reasonI18nKey),
  }
}

/** reason 码 → LogViewState。 */
function stateFromReason(reason: PartialReason): LogViewState {
  switch (reason) {
    case PARTIAL_REASONS.VIEW_STALE:
      return 'view_stale'
    case PARTIAL_REASONS.LOG_UNSUPPORTED:
    case PARTIAL_REASONS.ENGINE_NOT_READY:
      return 'engine_not_ready'
    case PARTIAL_REASONS.REHYDRATE_FAILED:
      return 'rehydrate_failed'
    case PARTIAL_REASONS.OFFLINE:
      return 'offline'
    case PARTIAL_REASONS.ARCHIVE_MISSING:
      return 'archive_missing'
    case PARTIAL_REASONS.BACKLOG:
      return 'backlog'
    case PARTIAL_REASONS.TRUNCATED:
      return 'truncated'
    case PARTIAL_REASONS.LEGACY:
      return 'legacy'
    case PARTIAL_REASONS.GAP:
    case PARTIAL_REASONS.RECOVERY_REQUIRED:
      return 'gap'
    default:
      return 'partial'
  }
}

/** 导出阻断 key：按视图状态选择。 */
function exportBlockedKeyFor(
  state: LogViewState,
  coverage: FormattedCoverageSummary,
  response: LogFederationResponse,
): string {
  if (response.errorCode === PARTIAL_REASONS.PERMISSION_REVOKED) {
    return LOGS_FEDERATION_KEYS.exportBlockedPermission
  }
  if (state === 'view_stale') return LOGS_FEDERATION_KEYS.exportBlockedViewStale
  if (coverage.duplicateUnresolved) return LOGS_FEDERATION_KEYS.exportBlockedUnresolved
  if (state === 'query_failed' || state === 'unknown') {
    return LOGS_FEDERATION_KEYS.exportBlockedFailed
  }
  if (state === 'engine_not_ready') return LOGS_FEDERATION_KEYS.exportBlockedNotReady
  if (!coverage.complete) return LOGS_FEDERATION_KEYS.exportBlockedIncomplete
  const exportStatus = response.exportStatus
  if (exportStatus === 'FAILED' || exportStatus === 'INCOMPLETE' || exportStatus === 'PENDING') {
    return LOGS_FEDERATION_KEYS.exportBlockedNotReady
  }
  if (response.exportVerified === false) return LOGS_FEDERATION_KEYS.exportBlockedNotReady
  return LOGS_FEDERATION_KEYS.exportBlockedIncomplete
}

/**
 * 分类日志联邦响应 → UI 视图状态。
 *
 * - `ok===false` / `errorCode` / coverage 缺失 → `query_failed`（失败，非空成功）。
 * - complete=false 的「0 条 items」→ 对应 partial 态，`emptySuccessAllowed=false`。
 * - complete=true 且 items 空 → `ready_complete` + emptySuccessAllowed（合法零结果）。
 * - nextCursor=null 只置 `viewEnumerationEnded`；`historyFullyEnumerated` 另行判定。
 */
export function classifyLogViewState(
  response: LogFederationResponse | null | undefined,
): ClassifiedLogViewState {
  if (response === null || response === undefined || typeof response !== 'object' || Array.isArray(response)) {
    const coverage = formatCoverageSummary(null)
    return {
      state: 'query_failed',
      isFailure: true,
      isIncomplete: false,
      emptySuccessAllowed: false,
      allowsExportDownload: false,
      viewEnumerationEnded: false,
      historyFullyEnumerated: false,
      bannerKey: VIEW_BANNER_KEY.query_failed,
      actionKeys: [...VIEW_ACTION_KEYS.query_failed],
      coverage,
      exportBlockedKey: LOGS_FEDERATION_KEYS.exportBlockedFailed,
    }
  }

  // 通过对象形状检查后恢复精确类型（isPlainObject 的 Record 收窄会丢掉 Coverage 字段）。
  const res = response as LogFederationResponse
  const transportFailed = res.ok === false
  const errorCode = typeof res.errorCode === 'string' ? res.errorCode : undefined
  const coverage = formatCoverageSummary(res.coverage, res.quality)
  const items = Array.isArray(res.items) ? res.items : null
  const reasons = coverage.reasons

  // 传输失败优先：即使 body 残留 coverage.complete=true，也不得当成功。
  if (transportFailed) {
    return {
      state: 'query_failed',
      isFailure: true,
      isIncomplete: false,
      emptySuccessAllowed: false,
      allowsExportDownload: false,
      viewEnumerationEnded: false,
      historyFullyEnumerated: false,
      bannerKey: VIEW_BANNER_KEY.query_failed,
      actionKeys: [...VIEW_ACTION_KEYS.query_failed],
      coverage: {
        ...coverage,
        status: 'failed',
        complete: false,
        emptySuccessAllowed: false,
        summaryKey: LOGS_FEDERATION_KEYS.coverageFailed,
      },
      primaryReason: errorCode,
      exportBlockedKey: LOGS_FEDERATION_KEYS.exportBlockedFailed,
    }
  }

  // coverage body 缺失 + 未声明成功 → 失败；声明 ok:true 但无 coverage → unknown（仍非空成功）。
  if (res.coverage === null || res.coverage === undefined) {
    const state: LogViewState = res.ok === true ? 'unknown' : 'query_failed'
    return {
      state,
      isFailure: state === 'query_failed',
      isIncomplete: true,
      emptySuccessAllowed: false,
      allowsExportDownload: false,
      viewEnumerationEnded: false,
      historyFullyEnumerated: false,
      bannerKey: VIEW_BANNER_KEY[state],
      actionKeys: [...VIEW_ACTION_KEYS[state]],
      coverage,
      primaryReason: errorCode,
      exportBlockedKey: LOGS_FEDERATION_KEYS.exportBlockedFailed,
    }
  }

  const primaryReason = errorCode ?? pickPrimaryReason(reasons)
  let state: LogViewState = coverage.complete
    ? 'ready_complete'
    : primaryReason
      ? stateFromReason(primaryReason)
      : 'partial'

  // errorCode（如 VIEW_STALE）可覆盖 coverage.reasons 未登记的情况。
  if (errorCode && stateFromReason(errorCode) !== 'partial') {
    state = stateFromReason(errorCode)
  }

  const isFailure =
    state === 'query_failed' ||
    state === 'view_stale' ||
    state === 'engine_not_ready' ||
    state === 'unknown'

  const isIncomplete = !coverage.complete || state !== 'ready_complete'
  const emptySuccessAllowed = coverage.emptySuccessAllowed && !isFailure && !isIncomplete

  const nextCursorNull = res.nextCursor === null
  const enumState = res.coverage.enumerationState
  const viewEnumerationEnded = nextCursorNull || enumState === 'EXHAUSTED'
  const historyGap = reasons.some((r) => HISTORY_GAP_REASONS.has(r))
  // nextCursor=null 单独不足以宣称全量历史完整。
  const historyFullyEnumerated =
    viewEnumerationEnded && coverage.complete && !historyGap && !coverage.duplicateUnresolved

  const allowsExportDownload =
    coverage.complete &&
    !isFailure &&
    !isIncomplete &&
    !coverage.duplicateUnresolved &&
    res.exportStatus !== 'FAILED' &&
    res.exportStatus !== 'INCOMPLETE' &&
    res.exportStatus !== 'PENDING' &&
    res.exportVerified !== false &&
    errorCode !== PARTIAL_REASONS.PERMISSION_REVOKED &&
    errorCode !== PARTIAL_REASONS.VIEW_STALE

  // ready_complete 且空列表时用「合法空态」文案。
  const bannerKey =
    state === 'ready_complete' && items !== null && items.length === 0
      ? LOGS_FEDERATION_KEYS.viewReadyCompleteEmpty
      : VIEW_BANNER_KEY[state]

  return {
    state,
    isFailure,
    isIncomplete: state === 'ready_complete' && coverage.complete ? false : isIncomplete,
    emptySuccessAllowed,
    allowsExportDownload,
    viewEnumerationEnded,
    historyFullyEnumerated,
    bannerKey,
    actionKeys: [...VIEW_ACTION_KEYS[state]],
    coverage,
    primaryReason,
    exportBlockedKey: allowsExportDownload
      ? undefined
      : exportBlockedKeyFor(state, coverage, res),
  }
}

/**
 * 导出下载 affordance：覆盖不完整 / 失败 / 未校验 → 不展示下载入口。
 *
 * FR-481：「导出只有校验后可下载且下载前复核权限」；「覆盖不完整时不得下载成功附件」。
 * 允许下载时仍提示服务端会在下载前复核权限。
 */
export function resolveExportDownloadAffordance(
  response: LogFederationResponse | null | undefined,
  classified?: ClassifiedLogViewState,
): ExportDownloadAffordance {
  const view = classified ?? classifyLogViewState(response)
  if (view.allowsExportDownload) {
    return {
      showDownload: true,
      blockedKey: '',
      allowedKey: LOGS_FEDERATION_KEYS.exportAllowed,
    }
  }
  return {
    showDownload: false,
    blockedKey: view.exportBlockedKey ?? LOGS_FEDERATION_KEYS.exportBlockedIncomplete,
    allowedKey: undefined,
  }
}

/**
 * 薄 UI 适配：把 classify 结果映射为 Coverage 横幅 props。
 * LogsPage 未改版时可先在测试 / 后续横幅组件中消费；不触碰页面本体。
 */
export function toCoverageBannerProps(
  classified: ClassifiedLogViewState,
): CoverageBannerProps {
  const { state, coverage } = classified
  const tone: CoverageBannerProps['tone'] =
    state === 'ready_complete'
      ? 'success'
      : classified.isFailure
        ? 'danger'
        : classified.isIncomplete
          ? 'warning'
          : 'success'

  return {
    visible: classified.isFailure || classified.isIncomplete || !coverage.complete,
    tone,
    titleKey: classified.bannerKey,
    reasonKeys: [...coverage.reasonKeys],
    actionKeys: [...classified.actionKeys],
    showRetry: classified.actionKeys.includes(LOGS_FEDERATION_KEYS.actionRetry),
    showExport: classified.allowsExportDownload,
    exportBlockedKey: classified.exportBlockedKey,
  }
}
