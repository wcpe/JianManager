/**
 * 客户端分发 KPI 口径字典（FR-356）。
 *
 * 权威语义见 `docs/specs/client-dist-kpi-semantics/spec.md`：
 * - 更新侧率（success/fail-static/rollback）只信 observability.summary（遥测 result）
 * - 请求侧率（HTTP status）来自 `/client-dist/stats`，**禁止**标成「更新成功」
 * - 活跃客户端优先 obs.activeMachines；exact 仅当 obs 提供 activeMachinesExact
 */

/** 共享 i18n key 前缀（zh/en 的 clientDistKpi 段）。 */
export const KPI_I18N = {
  activeClients: 'clientDistKpi.activeClients',
  activeExact: 'clientDistKpi.activeExact',
  activeApprox: 'clientDistKpi.activeApprox',
  activeFromStats: 'clientDistKpi.activeFromStats',
  updateSuccessRate: 'clientDistKpi.updateSuccessRate',
  updateFailStaticRate: 'clientDistKpi.updateFailStaticRate',
  updateFailStaticHint: 'clientDistKpi.updateFailStaticHint',
  updateRollbackRate: 'clientDistKpi.updateRollbackRate',
  requestSuccessRate: 'clientDistKpi.requestSuccessRate',
  requestFailureRate: 'clientDistKpi.requestFailureRate',
  downloadRequests: 'clientDistKpi.downloadRequests',
  downloadTrend: 'clientDistKpi.downloadTrend',
  downloadBytes: 'clientDistKpi.downloadBytes',
  manifestPulls: 'clientDistKpi.manifestPulls',
  artifactPulls: 'clientDistKpi.artifactPulls',
  rateUnavailable: 'clientDistKpi.rateUnavailable',
} as const

/** 观测 summary 中与 KPI 相关的子集。 */
export interface KpiObservabilitySummary {
  activeMachines: number
  activeMachinesExact: boolean
  successRate: number
  failStaticRate: number
  rollbackRate: number
}

/** stats 回退源中与 KPI 相关的子集。 */
export interface KpiStatsSummary {
  activeMachines: number
  /** HTTP 请求成功率（status&lt;400），**不是**更新成功率 */
  successRate: number
  failureRate: number
  rollbackRate?: number
}

export type ActiveExactness = 'exact' | 'approx' | 'unknown'

export interface ResolvedActiveClients {
  value: number
  /** exact=明细窗精确去重；approx=桶人次近似；unknown=stats 回退无 exact 标志 */
  exactness: ActiveExactness
  source: 'observability' | 'stats' | 'none'
}

export interface ResolvedUpdateRates {
  successRate: number | null
  failStaticRate: number | null
  rollbackRate: number | null
  source: 'observability' | 'none'
}

export interface ResolvedRequestRates {
  successRate: number | null
  failureRate: number | null
  source: 'stats' | 'none'
}

/** 率格式化为一位小数百分数；null → 占位。 */
export function formatKpiRate(rate: number | null | undefined, empty = '—'): string {
  if (rate === null || rate === undefined || !Number.isFinite(rate)) return empty
  return `${(rate * 100).toFixed(1)}%`
}

/** 解析活跃客户端：obs 优先，stats 回退；回退不得宣称精确去重。 */
export function resolveActiveClients(
  obs?: Partial<KpiObservabilitySummary> | null,
  stats?: Partial<KpiStatsSummary> | null,
): ResolvedActiveClients {
  if (obs && typeof obs.activeMachines === 'number' && Number.isFinite(obs.activeMachines)) {
    return {
      value: obs.activeMachines,
      exactness: obs.activeMachinesExact === true ? 'exact' : obs.activeMachinesExact === false ? 'approx' : 'unknown',
      source: 'observability',
    }
  }
  if (stats && typeof stats.activeMachines === 'number' && Number.isFinite(stats.activeMachines)) {
    return { value: stats.activeMachines, exactness: 'unknown', source: 'stats' }
  }
  return { value: 0, exactness: 'unknown', source: 'none' }
}

/** 更新侧率：仅 observability；stats.successRate 是请求率，禁止混用。 */
export function resolveUpdateRates(
  obs?: Partial<KpiObservabilitySummary> | null,
): ResolvedUpdateRates {
  if (!obs) {
    return { successRate: null, failStaticRate: null, rollbackRate: null, source: 'none' }
  }
  const hasAny =
    typeof obs.successRate === 'number' ||
    typeof obs.failStaticRate === 'number' ||
    typeof obs.rollbackRate === 'number'
  if (!hasAny) {
    return { successRate: null, failStaticRate: null, rollbackRate: null, source: 'none' }
  }
  return {
    successRate: finiteOrNull(obs.successRate),
    failStaticRate: finiteOrNull(obs.failStaticRate),
    rollbackRate: finiteOrNull(obs.rollbackRate),
    source: 'observability',
  }
}

/** 请求侧率：仅 stats（HTTP status 聚合）。 */
export function resolveRequestRates(stats?: Partial<KpiStatsSummary> | null): ResolvedRequestRates {
  if (!stats) return { successRate: null, failureRate: null, source: 'none' }
  const successRate = finiteOrNull(stats.successRate)
  const failureRate = finiteOrNull(stats.failureRate)
  if (successRate === null && failureRate === null) {
    return { successRate: null, failureRate: null, source: 'none' }
  }
  return { successRate, failureRate, source: 'stats' }
}

/** 活跃脚注 i18n key；unknown 时返回 null（不展示假精确）。 */
export function activeClientsHintKey(exactness: ActiveExactness, source: ResolvedActiveClients['source']): string | null {
  if (exactness === 'exact') return KPI_I18N.activeExact
  if (exactness === 'approx') return KPI_I18N.activeApprox
  if (source === 'stats') return KPI_I18N.activeFromStats
  return null
}

/**
 * 空态三类（FR-357）：
 * - no_traffic：窗口内无请求且无更新/遥测
 * - no_telemetry：有请求流量但更新侧全空（未开遥测或未上报）
 * - out_of_window：有活跃但仅近似去重（明细窗外）
 * - none：有数据且不需要顶栏空态提示
 */
export type ClientDistEmptyKind = 'no_traffic' | 'no_telemetry' | 'out_of_window' | 'none'

export interface EmptyStateSignals {
  /** 请求侧：下载/manifest/artifact 次数合计 */
  requestCount?: number | null
  /** 更新侧：updateTotal（遥测） */
  updateTotal?: number | null
  /** 活跃客户端解析结果 */
  active?: Pick<ResolvedActiveClients, 'value' | 'exactness' | 'source'> | null
  /** 是否仍在加载（加载中不展示空态） */
  loading?: boolean
}

/** 解析空态类别；loading 时返回 none。 */
export function resolveClientDistEmptyKind(signals: EmptyStateSignals): ClientDistEmptyKind {
  if (signals.loading) return 'none'
  const requests = Math.max(0, Number(signals.requestCount ?? 0) || 0)
  const updates = Math.max(0, Number(signals.updateTotal ?? 0) || 0)
  const active = signals.active

  if (requests === 0 && updates === 0) {
    // 活跃>0 但无请求/更新：通常是窗外近似残留，优先窗外提示
    if (active && active.value > 0 && active.exactness === 'approx') return 'out_of_window'
    return 'no_traffic'
  }
  if (requests > 0 && updates === 0) return 'no_telemetry'
  if (active && active.exactness === 'approx' && active.source === 'observability') return 'out_of_window'
  return 'none'
}

/** 空态 i18n key（clientStats / clientDistMonitor 可共用文案键）。 */
export function clientDistEmptyI18nKey(kind: ClientDistEmptyKind): string | null {
  if (kind === 'no_traffic') return 'clientStats.emptyNoTraffic'
  if (kind === 'no_telemetry') return 'clientStats.emptyNoTelemetry'
  if (kind === 'out_of_window') return 'clientStats.emptyOutOfWindow'
  return null
}

function finiteOrNull(n: number | undefined): number | null {
  return typeof n === 'number' && Number.isFinite(n) ? n : null
}
