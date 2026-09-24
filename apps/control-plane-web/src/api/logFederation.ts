/**
 * FR-481 日志联邦可选 API 客户端。
 *
 * 职责：
 * - 探测 `GET /logs/federation`（双路径门面，契约见 FR-480/433）；
 * - 把后端 envelope / LegacyCoverage 映射为 foundation 的 {@link LogFederationResponse}；
 * - API 404 时降级为 legacy logs 视图（页面继续消费既有 `/logs` 表格，横幅说明回退）；
 * - ErrFederatedNotReady 透传为结构化失败态，**不得**把空 items 当完整零结果。
 *
 * 本模块无 React 组件依赖；LogsPage 只消费 {@link LogsFederationResult}。
 */
import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'
import {
  PARTIAL_REASONS,
  classifyLogViewState,
  resolveExportDownloadAffordance,
  toCoverageBannerProps,
  type ClassifiedLogViewState,
  type Coverage,
  type CoverageBannerProps,
  type ExportDownloadAffordance,
  type LogFederationResponse,
  type PartialReason,
  type Quality,
  type TargetCoverage,
  type TargetQueryStatus,
} from '@/lib/logs-federation'

/** 后端 ErrFederatedNotReady 错误文案（internal/controlplane/service/log_legacy.go）。 */
export const FEDERATED_NOT_READY_MESSAGE = 'federated log query path not ready'

/** 可选联邦查询路径：后端门面 GET /api/v1/logs/federation（Search + envelope）。 */
export const LOGS_FEDERATION_PATH = '/logs/federation'

/** 精确 Search 端点（FR-479 HTTP）；门面不可用时可降级探测。 */
export const LOGS_FEDERATION_SEARCH_PATH = '/logs/federation/search'
export const LOGS_FEDERATION_TAIL_PATH = '/logs/federation/tail'

export type LogSourceTag = 'legacy' | 'federated'

/** LegacyCoverage 的前端投影（字段宽松：同时接受 camelCase / snake_case）。 */
export interface FederationCoverageBody {
  sourceTag?: LogSourceTag
  complete?: boolean
  partialReasons?: PartialReason[]
  partial_reasons?: PartialReason[]
  targets?: TargetCoverage[]
  enumerationState?: Coverage['enumerationState']
  enumeration_state?: Coverage['enumerationState']
  fromTime?: string
  from_time?: string
  toTime?: string
  to_time?: string
  ndjsonInScope?: boolean
  notes?: string[]
}

/** 双路径查询 envelope（同时接受门面 envelope 与 FR-479 SearchResponse snake_case）。 */
export interface LogsFederationEnvelope {
  ok?: boolean
  sourceTag?: LogSourceTag
  sourceTags?: LogSourceTag[]
  federatedReady?: boolean
  statsExact?: boolean
  items?: unknown[] | null
  total?: number
  page?: number
  pageSize?: number
  coverage?: FederationCoverageBody | null
  quality?: {
    duplicateQuality?: string
    duplicate_quality?: string
    statsQuality?: string
    stats_quality?: string
  } | null
  notes?: string[]
  errorCode?: string
  errorMessage?: string
  exportStatus?: string
  exportVerified?: boolean
  exhausted?: boolean
  budget_cut?: boolean
  next_cursor?: string
  view?: { view_id?: string; viewId?: string }
}

/** LogsPage 消费的联邦 UI 结果。 */
export interface LogsFederationResult {
  /** 探测是否拿到可用联邦接口。false + degraded=true 表示 404 回退 legacy。 */
  available: boolean
  /** 404 → true：表格走既有 `/logs`，横幅提示回退；经典导出保留。 */
  degraded: boolean
  sourceTag: LogSourceTag | null
  /** 后端 notes/errorCode 标明 ErrFederatedNotReady / ENGINE_NOT_READY。 */
  federatedNotReady: boolean
  response: LogFederationResponse
  classified: ClassifiedLogViewState
  banner: CoverageBannerProps
  exportAffordance: ExportDownloadAffordance
}

/** axios / fetch 风格 404 判定。 */
/**
 * 联邦路径是否「不可用」（应降级到经典 /logs）。
 *
 * 覆盖：404（端点未部署）、401/403（未授权或认证不通过）、以及无响应的连接类失败
 * （ECONNREFUSED / 超时 / DNS）。这些情形下联邦拿不到数据，但经典表格仍可用。
 * 与「联邦可用但结果不完整」区分：后者带 coverage 语义，不降级、也不当空成功。
 */
export function isFederationUnavailable(error: unknown): boolean {
  const status = (error as { response?: { status?: number } } | null)?.response?.status
  if (typeof status === 'number') {
    return status === 404 || status === 401 || status === 403
  }
  // 无响应：连接被拒/超时/DNS 失败视为不可用。
  return true
}

export function isHttp404(error: unknown): boolean {
  const status = (error as { response?: { status?: number } } | null)?.response?.status
  return status === 404
}

/** notes 中是否出现 ErrFederatedNotReady（或等价 ENGINE_NOT_READY 码）。 */
export function isFederatedNotReady(notes?: string[] | null): boolean {
  if (!Array.isArray(notes) || notes.length === 0) return false
  return notes.some(
    (n) =>
      typeof n === 'string' &&
      (n.includes(FEDERATED_NOT_READY_MESSAGE) ||
        n === PARTIAL_REASONS.ENGINE_NOT_READY ||
        n === PARTIAL_REASONS.LOG_UNSUPPORTED),
  )
}

function asString(v: unknown): string | undefined {
  return typeof v === 'string' && v.length > 0 ? v : undefined
}

function normalizeReasons(raw: unknown): PartialReason[] {
  if (!Array.isArray(raw)) return []
  return raw.filter((r): r is PartialReason => typeof r === 'string' && r.length > 0)
}

/** TargetCoverage 宽松投影：兼容 FR-472 snake_case（target_id/state）与 UI camelCase。 */
function normalizeTargets(raw: unknown): TargetCoverage[] {
  if (!Array.isArray(raw)) return []
  return raw.map((t) => {
    const o = (t && typeof t === 'object' ? t : {}) as Record<string, unknown>
    const targetId = asString(o.targetId) ?? asString(o.target_id) ?? 'unknown'
    const stateRaw = asString(o.status) ?? asString(o.state) ?? 'UNKNOWN'
    const normalized = stateRaw.toUpperCase()
    const status: TargetQueryStatus =
      normalized === 'SUCCESS' ? 'INCLUDED' :
        normalized === 'PARTIAL' ? 'UNKNOWN' :
          normalized === 'ARCHIVE_MISS' ? 'ARCHIVE_MISSING' :
            (normalized as TargetQueryStatus)
    const reasonRaw =
      o.reason ?? (Array.isArray(o.reasons) ? (o.reasons as string[])[0] : undefined)
    return {
      targetId,
      name: asString(o.name) ?? asString(o.worker_id) ?? targetId,
      status,
      included:
      o.included === true ||
        (o.included === undefined && (normalized === 'SUCCESS' || status === 'ONLINE')),
      reason: asString(reasonRaw) as TargetCoverage['reason'],
    } satisfies TargetCoverage
  })
}

/**
 * 把后端 coverage（可能是 LegacyCoverage 或 FR-472 Coverage）规范为 foundation Coverage。
 * Legacy 恒 complete=false；未知形状不返回 null-success。
 */
export function normalizeFederationCoverage(
  body: FederationCoverageBody | null | undefined,
  sourceTag: LogSourceTag | null,
): Coverage | null {
  if (body === null || body === undefined) {
    // 有 sourceTag 但无 coverage body 时仍构造可解释的 incomplete coverage。
    if (sourceTag === 'legacy') {
      return legacyCoverageFrom(undefined)
    }
    return null
  }
  if (typeof body !== 'object' || Array.isArray(body)) return null

  const rawComplete = body.complete
  const hasCoverageShape =
    rawComplete !== undefined ||
    body.partialReasons !== undefined ||
    body.partial_reasons !== undefined ||
    Array.isArray(body.targets)

  if (hasCoverageShape) {
    const reasons = normalizeReasons(body.partialReasons ?? body.partial_reasons)
    const targets = normalizeTargets(body.targets)
    const isLegacy = sourceTag === 'legacy' || body.sourceTag === 'legacy'
    // Legacy 路径后端声明 Complete 恒 false；即便漏传也不得当完整成功。
    const complete = isLegacy ? false : rawComplete === true
    return {
      complete,
      partialReasons:
        reasons.length > 0
          ? reasons
          : isLegacy
            ? [PARTIAL_REASONS.LEGACY]
            : complete
              ? []
              : [PARTIAL_REASONS.EXPORT_INCOMPLETE],
      targets,
      enumerationState:
        body.enumerationState ?? body.enumeration_state ?? (complete ? 'EXHAUSTED' : 'OPEN'),
    }
  }

  // 仅 LegacyCoverage 形状（fromTime/toTime/ndjsonInScope）。
  if (sourceTag === 'legacy' || body.sourceTag === 'legacy') {
    return legacyCoverageFrom(body)
  }
  return null
}

function legacyCoverageFrom(body: FederationCoverageBody | null | undefined): Coverage {
  const from = asString(body?.fromTime) ?? asString(body?.from_time)
  const to = asString(body?.toTime) ?? asString(body?.to_time)
  return {
    complete: false,
    partialReasons: [PARTIAL_REASONS.LEGACY],
    targets: [
      {
        targetId: 'legacy-cp-logs',
        name: 'Legacy CP logs',
        status: 'LEGACY',
        included: true,
        reason: PARTIAL_REASONS.LEGACY,
        ...(from || to ? { legacyRange: { from, to } } : {}),
      },
    ],
    enumerationState: 'EXHAUSTED',
  }
}

function resolveSourceTag(envelope: LogsFederationEnvelope): LogSourceTag | null {
  if (envelope.sourceTag === 'legacy' || envelope.sourceTag === 'federated') {
    return envelope.sourceTag
  }
  const tags = Array.isArray(envelope.sourceTags) ? envelope.sourceTags : []
  const hasFed = tags.includes('federated')
  const hasLegacy = tags.includes('legacy')
  if (hasFed && !hasLegacy) return 'federated'
  if (hasLegacy && !hasFed) return 'legacy'
  return null
}

/** 把 quality（snake/camel、大小写混用）归一到 foundation Quality（大写枚举）。 */
function normalizeQuality(
  raw: LogsFederationEnvelope['quality'],
): Quality | undefined {
  if (!raw || typeof raw !== 'object') return undefined
  const dq = asString(raw.duplicateQuality) ?? asString(raw.duplicate_quality)
  const sq = asString(raw.statsQuality) ?? asString(raw.stats_quality)
  return {
    duplicateQuality: dq?.toUpperCase() ?? 'UNKNOWN',
    statsQuality: sq?.toUpperCase() ?? 'UNKNOWN',
  }
}

/** envelope → foundation response + 来源标记。 */
export function mapFederationEnvelope(envelope: LogsFederationEnvelope): {
  sourceTag: LogSourceTag | null
  federatedNotReady: boolean
  response: LogFederationResponse
} {
  const notes = Array.isArray(envelope.notes) ? envelope.notes : []
  // SearchResponse 裸响应（无 ok/sourceTag）视为 federated 门面/端点结果。
  const looksLikeSearch =
    envelope.sourceTag === undefined &&
    envelope.coverage !== undefined &&
    (envelope.items !== undefined || envelope.quality !== undefined)
  const sourceTag =
    resolveSourceTag(envelope) ?? (looksLikeSearch ? 'federated' : null)
  const quality = normalizeQuality(envelope.quality)
  const dq = quality?.duplicateQuality
  const federatedNotReady =
    isFederatedNotReady(notes) ||
    envelope.federatedReady === false ||
    envelope.errorCode === PARTIAL_REASONS.ENGINE_NOT_READY ||
    envelope.errorCode === PARTIAL_REASONS.LOG_UNSUPPORTED ||
    dq === 'UNSPECIFIED'

  let coverage = normalizeFederationCoverage(envelope.coverage, sourceTag)
  let errorCode = asString(envelope.errorCode)
  const items = Array.isArray(envelope.items) ? envelope.items : null

  // FR-472 §4.5：duplicate 未决/冲突 → coverage 不完整，禁止空成功。
  if (dq === 'UNRESOLVED' || dq === 'CONFLICT') {
    const reason = PARTIAL_REASONS.DUPLICATE_UNRESOLVED
    if (!coverage || coverage.complete) {
      coverage = {
        complete: false,
        partialReasons: [reason],
        targets: coverage?.targets ?? [],
        enumerationState: 'OPEN',
      }
    } else if (!coverage.partialReasons.includes(reason)) {
      coverage = {
        ...coverage,
        partialReasons: [...coverage.partialReasons, reason],
      }
    }
  }

  if (federatedNotReady) {
    errorCode = errorCode ?? PARTIAL_REASONS.ENGINE_NOT_READY
    // 引擎未就绪：coverage 必须不完整，禁止把空 items 呈成完整零结果。
    if (!coverage || coverage.complete) {
      coverage = {
        complete: false,
        partialReasons: [PARTIAL_REASONS.ENGINE_NOT_READY],
        targets: coverage?.targets ?? [],
        enumerationState: 'OPEN',
      }
    } else if (!coverage.partialReasons.includes(PARTIAL_REASONS.ENGINE_NOT_READY)) {
      coverage = {
        ...coverage,
        partialReasons: [...coverage.partialReasons, PARTIAL_REASONS.ENGINE_NOT_READY],
      }
    }
  }

  // 有 items 但无 coverage 且未声明成功 → 仍不得当完整；构造 partial 保底。
  if (!coverage && items !== null && envelope.ok !== true) {
    coverage = {
      complete: false,
      partialReasons: [PARTIAL_REASONS.EXPORT_INCOMPLETE],
      targets: [],
    }
  }

  // ok=false 或 body 无任何可解释覆盖/数据 → 传输/查询失败，禁止空成功。
  const hasUsableBody = coverage !== null || items !== null || sourceTag !== null
  const ok = envelope.ok === false ? false : hasUsableBody

  return {
    sourceTag,
    federatedNotReady,
    response: {
      ok,
      items,
      coverage,
      quality,
      viewId: asString(envelope.view?.view_id) ?? asString(envelope.view?.viewId),
      nextCursor: envelope.next_cursor ?? null,
      exhausted: envelope.exhausted,
      errorCode,
      errorMessage: asString(envelope.errorMessage),
      exportStatus: asString(envelope.exportStatus),
      exportVerified: envelope.exportVerified,
    },
  }
}

/**
 * 404 降级信封：经典 `/logs` 表格继续工作，coverage 标 LEGACY 不完整。
 * UI 侧 classicExportAllowed=true，保留既有导出入口；横幅说明联邦 API 不可用。
 */
export function legacyDegradeEnvelope(): LogsFederationEnvelope {
  return {
    sourceTag: 'legacy',
    sourceTags: ['legacy'],
    items: null,
    coverage: {
      sourceTag: 'legacy',
      complete: false,
      partialReasons: [PARTIAL_REASONS.LEGACY],
      targets: [
        {
          targetId: 'legacy-cp-logs',
          name: 'Legacy CP logs',
          status: 'LEGACY',
          included: true,
          reason: PARTIAL_REASONS.LEGACY,
        },
      ],
      notes: ['logs federation API unavailable (404); degraded to legacy logs view'],
    },
    notes: ['logs federation API unavailable (404); degraded to legacy logs view'],
  }
}

/** 传输失败信封：ok=false 语义，禁止空成功。 */
export function transportFailureEnvelope(message?: string): LogsFederationEnvelope {
  return {
    ok: false,
    items: null,
    coverage: null,
    errorCode: PARTIAL_REASONS.EXPORT_INCOMPLETE,
    errorMessage: message,
    notes: ['federation transport failure'],
  }
}

function finalize(
  base: Omit<LogsFederationResult, 'classified' | 'banner' | 'exportAffordance'>,
): LogsFederationResult {
  const classified = classifyLogViewState(base.response)
  return {
    ...base,
    classified,
    banner: toCoverageBannerProps(classified),
    exportAffordance: resolveExportDownloadAffordance(base.response, classified),
  }
}

/** 把 envelope 组装为完整 UI 结果。 */
export function buildFederationResult(
  envelope: LogsFederationEnvelope,
  options: { degraded?: boolean } = {},
): LogsFederationResult {
  const mapped = mapFederationEnvelope(envelope)
  return finalize({
    available: !options.degraded,
    degraded: options.degraded === true,
    sourceTag: mapped.sourceTag,
    federatedNotReady: mapped.federatedNotReady,
    response: mapped.response,
  })
}

/**
 * 探测联邦覆盖（与 LogsPage 筛选参数同源）。
 * 404 → degraded legacy；其它错误 → 传输失败态（非空成功）。
 */
export function useLogsFederation(
  params: Record<string, unknown>,
  options: { enabled?: boolean; refetchInterval?: number | false } = {},
) {
  return useQuery({
    queryKey: ['logsFederation', params],
    queryFn: async (): Promise<LogsFederationResult> => {
      try {
		const requestParams = { ...params }
		const tailMode = asString(requestParams.tailMode)
		delete requestParams.tailMode
		if (tailMode) requestParams.mode = tailMode
		const path = tailMode ? LOGS_FEDERATION_TAIL_PATH : LOGS_FEDERATION_PATH
        const { data } = await api.get<LogsFederationEnvelope>(path, { params: requestParams })
        return buildFederationResult(data && typeof data === 'object' ? data : {}, {
          degraded: false,
        })
      } catch (error) {
        // 「联邦不可用」一律降级为 legacy：404（未部署）、401（认证/未授权）、
        // 连接失败（ECONNREFUSED/超时）都表示该路径当前拿不到数据。
        // 此时经典 `/logs` 表格仍可用，必须回退而非让列表停留在空态
        // （此前仅 404 降级，其他失败走 transportFailureEnvelope 且 degraded=false，
        //   使 federationActive 误判为 true 并以空的 federationItems 为准 → 列表永久空白）。
        if (isFederationUnavailable(error)) {
          return buildFederationResult(legacyDegradeEnvelope(), { degraded: true })
        }
        return buildFederationResult(
          transportFailureEnvelope(
            error instanceof Error ? error.message : String(error),
          ),
          { degraded: false },
        )
      }
    },
    enabled: options.enabled ?? true,
	refetchInterval: options.refetchInterval,
    // 覆盖状态变化不频繁；跟随轮询时由页面 key 带动 refetch。
    staleTime: 30_000,
  })
}

export interface FederationStatsAggregate {
	count?: number
	sum?: number
	min?: number
	max?: number
	has_min_max?: boolean
}

export interface FederationStatsPoint {
	dimensions?: Record<string, string>
	/** 后端 StatsPoint 的聚合体（wire 真源 `agg.count/sum/min/max`）。 */
	agg?: FederationStatsAggregate
	/** 兼容旧/扁平形态（部分 fixture 用顶层 count）。 */
	count?: number
}

export interface FederationStatsResponse {
	points?: FederationStatsPoint[]
	coverage?: FederationCoverageBody
}

export interface FederationFacetValue {
	value: string
	count: number
}

export interface FederationFacetDimension {
	dimension: string
	values: FederationFacetValue[]
	truncated?: boolean
}

export interface FederationFacetsResponse {
	dimensions?: FederationFacetDimension[]
	coverage?: FederationCoverageBody
}

export function useLogsFederationStats(params: Record<string, unknown>, enabled: boolean) {
	return useQuery({
		queryKey: ['logsFederationStats', params],
		queryFn: async () => (await api.get<FederationStatsResponse>('/logs/federation/stats', { params })).data,
		enabled,
	})
}

export function useLogsFederationFacets(params: Record<string, unknown>, enabled: boolean) {
	return useQuery({
		queryKey: ['logsFederationFacets', params],
		queryFn: async () => (await api.get<FederationFacetsResponse>('/logs/federation/facets', { params })).data,
		enabled,
	})
}

/** 在同一联邦 Query View 上物化并下载经覆盖校验的 NDJSON。 */
export async function exportFederatedLogs(params: Record<string, unknown>): Promise<void> {
  const exportParams = { ...params }
  delete exportParams.page
  delete exportParams.pageSize
  delete exportParams.cursor
  delete exportParams.limit
  const response = await api.post(LOGS_FEDERATION_PATH + '/export', null, {
    params: exportParams,
    responseType: 'blob',
    timeout: 120_000,
  })
  const contentType = String(response.headers['content-type'] ?? '')
  if (!contentType.includes('application/x-ndjson')) {
    throw new Error('federated export did not produce a verified NDJSON artifact')
  }
  const url = URL.createObjectURL(response.data as Blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = `logs-federation-${new Date().toISOString().replace(/[:.]/g, '-')}.ndjson`
  anchor.click()
  URL.revokeObjectURL(url)
}
