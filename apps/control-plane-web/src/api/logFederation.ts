/** 
 *日志联邦可选 API 客户端。
 *
 * 职责：
 * - 探测 `GET /logs/federation`（双路径门面，契约见481）；
 * - 把后端 envelope / LegacyCoverage 映射为 foundation 的 {@link LogFederationResponse}；
 * - API 404 时降级为 legacy logs 视图（页面继续消费既有 `/logs` 表格，横幅说明回退）；
 * - ErrFederatedNotReady 透传为结构化失败态，**不得**把空 items 当完整零结果。
 *
 * 本模块无 React 组件依赖；LogsPage 只消费 {@link LogsFederationResult}。
 */
import { useEffect } from 'react'
import { hashKey, keepPreviousData, useQuery, useQueryClient } from '@tanstack/react-query'
import { CanceledError, isCancel } from 'axios'
import api from '@/api/client'
import { downloadLogBlob, sameLogItems } from '@/api/logs'
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

/** 精确 Search 端点（HTTP）；门面不可用时可降级探测。 */
export const LOGS_FEDERATION_SEARCH_PATH = '/logs/federation/search'
export const LOGS_FEDERATION_TAIL_PATH = '/logs/federation/tail'

export type LogSourceTag = 'legacy' | 'federated'

/** LegacyCoverage 的前端投影（字段宽松：同时接受 camelCase / snake_case）。 */
export interface FederationCoverageBody {
  sourceTag?: LogSourceTag
  complete?: boolean
  /** 首屏快路径未做完整性校验（后端 Coverage.Pending）；缺失视为 false。 */
  pending?: boolean
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

/** 实际执行的查询预算与耗时；缺失表示旧 CP，前端不得自行猜测。 */
export interface FederationQueryMetadata {
  elapsed_ms: number
  returned_rows: number
  limit: number
  max_bytes: number
  timeout_ms: number
  max_fanout: number
}

/** 双路径查询 envelope（同时接受门面 envelope 与SearchResponse snake_case）。 */
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
  query_meta?: FederationQueryMetadata
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
  /** 
   * 持久限流（HTTP 429，重试已耗尽）：**本次没拿到数据**，不是「查到 0 条」。
   * 页面据此保留上一份已渲染列表并给出可见的限流提示，禁止空成功、也不降级 legacy
   * （联邦路径本身是好的，降级只会拿旧数据把问题掩盖掉）。
   */
  rateLimited: boolean
  response: LogFederationResponse
  classified: ClassifiedLogViewState
  banner: CoverageBannerProps
  exportAffordance: ExportDownloadAffordance
  /**
   * 降级（`degraded=true`）时被转换掉的原始错误——request 失败的原因本身。
   *
   * 为何必须带出来：降级信袋的 `items` 恒为空、`degraded` 恒为 true，从它本身分不清
   * 「403 无权」「超时」「端点不存在」。日志中心（LogsPage）不在乎这个区别（降级后它会去读平台日志），
   * 但停机控制台（StoppedLogsView）没有别的数据源可退——它必须把降级也当失败，
   * 而区分「无权」与「超时」靠的就是这里保留的原始错误。
   */
  failure?: unknown
  queryMeta?: FederationQueryMetadata
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
  if (isLogRequestCanceled(error)) return false
  const status = (error as { response?: { status?: number } } | null)?.response?.status
  if (typeof status === 'number') {
    // 502/503/504：dev server 代理无法连接后端时由代理返回（CI 无 CP 即此情形）；
    // 500 亦可能是代理层错误，与「后端已就绪但查询失败」不同。
    if (status === 404 || status === 401 || status === 403) return true
    return status >= 500
  }
  // 无响应：连接被拒/超时/DNS 失败视为不可用。
  return true
}

export function isHttp404(error: unknown): boolean {
  const status = (error as { response?: { status?: number } } | null)?.response?.status
  return status === 404
}

/** 
 * 是否为限流（HTTP 429）错误。
 *
 * 429 的含义是「这次没拿到数据」，**不是**「查到的结果是空的」：调用方必须与其它失败
 * 区别对待——保留上一份已渲染列表并显式提示限流，绝不能把它当成一次查询结果。
 */
export function isRateLimitError(error: unknown): boolean {
  const status = (error as { response?: { status?: number } } | null)?.response?.status
  return status === 429
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

/** TargetCoverage 宽松投影：兼容snake_case（target_id/state）与 UI camelCase。 */
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
 * 把后端 coverage（可能是 LegacyCoverage 或Coverage）规范为 foundation Coverage。
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
    body.pending !== undefined ||
    body.partialReasons !== undefined ||
    body.partial_reasons !== undefined ||
    Array.isArray(body.targets)

  if (hasCoverageShape) {
    const reasons = normalizeReasons(body.partialReasons ?? body.partial_reasons)
    const targets = normalizeTargets(body.targets)
    const isLegacy = sourceTag === 'legacy' || body.sourceTag === 'legacy'
    // Legacy 路径后端声明 Complete 恒 false；即便漏传也不得当完整成功。
    const complete = isLegacy ? false : rawComplete === true
    // 快路径未校验标记（Coverage.Pending）：独立于缺失原因，原样透传。
    // 未校验 ≠ 有缺失：不塞 EXPORT_INCOMPLETE 占位（那是「导出物不完整」的具体原因，
    // 套在 pending 上会把「未校验」误渲染成「覆盖不完整」）。
    const pending = !isLegacy && !complete && body.pending === true
    return {
      complete,
      pending,
      partialReasons:
        reasons.length > 0
          ? reasons
          : isLegacy
            ? [PARTIAL_REASONS.LEGACY]
            : complete || pending
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
	// `federatedNotReady` 只回答一件事：**联邦路径本身是否可用**。
	//
	// 2026-10-30 修：这里曾把 `dq === 'UNSPECIFIED'` 也算成未就绪，后果是
	// **每一次实时跟随都报“日志查询服务未启用”**——因为 tail 路径不输出质量
	// （`quality` 恒为零值 unspecified）。两个概念被混为一谈：
	//
	//   - 未就绪 = 引擎/数据面没起来（EngineNotReady / Unsupported / federatedReady=false）；
	//   - 质量未知 = 数据拿到了，只是没有一个来源断言过它的重复度。
	//
	// 后者应当交给 `classifyLogViewState` 以 `quality=unspecified` 的标签温和展示，
	// 而不是把整页判成“服务未启用”并清空。
	const federatedNotReady =
		isFederatedNotReady(notes) ||
		envelope.federatedReady === false ||
		envelope.errorCode === PARTIAL_REASONS.ENGINE_NOT_READY ||
		envelope.errorCode === PARTIAL_REASONS.LOG_UNSUPPORTED

	let coverage = normalizeFederationCoverage(envelope.coverage, sourceTag)
  let errorCode = asString(envelope.errorCode)
  const items = Array.isArray(envelope.items) ? envelope.items : null

  // §4.5 的质量标记**不得改写 coverage.complete**。
  //
  // 旧实现把 dq∈{UNRESOLVED,CONFLICT} 一律处理成 `complete=false` + 注入
  // DUPLICATE_UNRESOLVED，等于在数据映射层就宣称「覆盖不完整」。但 quality 与完整性是
  // 正交维度（spec §3.1 / types.ts），生产实测正是 `coverage.complete=true` +
  // `duplicate_quality=conflict` 被逼成 partial，横幅于是谎报「覆盖不完整」并凭空补一条
  // 「导出内容不完整」原因。质量对导出的阻断由 foundation 的 duplicateConflict /
  // duplicateUnresolved 标记单独承担（见 lib/logs-federation/helpers.ts）。
  //
  // coverage 缺失时也不在这里凭 quality 造一个 complete=false 的假覆盖：那会把
  // 「质量未知」伪装成「已知不完整」；coverage 缺失由 classifyLogViewState 判 unknown。

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
      // 空串游标归一为 null（后端无更多数据时发 ''）：'' 不是有效游标，
      // 让它以 null 形态参与「枚举是否结束」与下一页可点性判定。
      nextCursor: asString(envelope.next_cursor) ?? null,
      exhausted: envelope.exhausted,
      errorCode,
      errorMessage: asString(envelope.errorMessage),
      exportStatus: asString(envelope.exportStatus),
      exportVerified: envelope.exportVerified,
    },
  }
}

/** 
 * 降级信封：经典 `/logs` 表格继续工作，coverage 标 LEGACY 不完整。
 * UI 侧 classicExportAllowed=true，保留既有导出入口；横幅说明联邦 API 不可用。
 *
 * `notReady` 区分两种降级来源，**诊断信息必须保留**：
 * - false：联邦路径不可达（404/未部署/连接失败），问题在联邦接口本身；
 * - true：联邦接口正常响应但「日志查询引擎未就绪」（受管 VL 未配置/未启动），
 *   问题在引擎未启用，用户需要去节点页开启——若此时仍标 LEGACY，横幅会退化成
 *   「含切换前的历史数据」，把可操作的启用指引换成无动作的说明。
 */
export function legacyDegradeEnvelope(notReady = false): LogsFederationEnvelope {
  const notes = notReady
    ? [
        FEDERATED_NOT_READY_MESSAGE,
        'degraded to legacy logs view (engine not ready)',
      ]
    : ['logs federation API unavailable (404); degraded to legacy logs view']
  return {
    sourceTag: 'legacy',
    sourceTags: ['legacy'],
    items: null,
    coverage: {
      sourceTag: 'legacy',
      complete: false,
      partialReasons: [
        notReady ? PARTIAL_REASONS.ENGINE_NOT_READY : PARTIAL_REASONS.LEGACY,
      ],
      targets: [
        {
          targetId: 'legacy-cp-logs',
          name: 'Legacy CP logs',
          status: notReady ? 'NOT_READY' : 'LEGACY',
          included: true,
          reason: notReady ? PARTIAL_REASONS.ENGINE_NOT_READY : PARTIAL_REASONS.LEGACY,
        },
      ],
      notes,
    },
    notes,
    ...(notReady ? { federatedReady: false } : {}),
  }
}

/** 保留服务端可行动的查询错误；不把 400 的语法/预算原因改成 axios 泛化文案。 */
export function logQueryErrorMessage(error: unknown): string | undefined {
  if (error == null || isLogRequestCanceled(error)) return undefined
  const response = (error as { response?: { data?: unknown } }).response
  if (response?.data && typeof response.data === 'object' && !(response.data instanceof Blob)) {
    const data = response.data as Record<string, unknown>
    const reason = asString(data.message) ?? asString(data.errorMessage) ?? asString(data.error)
    if (reason) return reason
  }
  if (error instanceof Error) return error.message
  return typeof error === 'string' ? error : undefined
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

/** 
 * 限流失败信封：HTTP 429（重试已耗尽）。
 *
 * 与 {@link transportFailureEnvelope} 的区别只在一个可机读的原因码，但语义完全不同：
 * - 它不是「查到 0 条」⇒ 同样 ok=false、无 coverage、禁止空成功；
 * - 也不是「联邦路径不可用」⇒ **不降级 legacy**（联邦本身是好的，降级只会把限流
 *   掩盖成一份看起来正常的旧数据）。
 * 页面据 errorCode=RATE_LIMITED 保留上一份已渲染列表并给出可重试的限流提示。
 */
export function rateLimitedEnvelope(message?: string): LogsFederationEnvelope {
  return {
    ok: false,
    items: null,
    coverage: null,
    errorCode: PARTIAL_REASONS.RATE_LIMITED,
    errorMessage: message,
    notes: ['federation rate limited (429)'],
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
  options: { degraded?: boolean; rateLimited?: boolean; failure?: unknown } = {},
): LogsFederationResult {
  const mapped = mapFederationEnvelope(envelope)
  return finalize({
    available: !options.degraded,
    degraded: options.degraded === true,
    sourceTag: mapped.sourceTag,
    federatedNotReady: mapped.federatedNotReady,
    // 限流可由显式选项或信封的 errorCode 标出：两条路径都必须呈成失败态（非空成功）。
    rateLimited:
      options.rateLimited === true || mapped.response.errorCode === PARTIAL_REASONS.RATE_LIMITED,
    response: mapped.response,
    // 只对降级保留：正常结果不需要这份原始错误。
    ...(options.failure !== undefined ? { failure: options.failure } : {}),
    ...(envelope.query_meta ? { queryMeta: envelope.query_meta } : {}),
  })
}

/** 
 * 限流重试策略（主查询 / stats / facets 共用，避免三处定义漂移）。
 *
 * 生产实测（2026-10 `/api/v1/logs/federation`，近 20MB cp.out、9199 条请求）：
 * 该端点 2676 次请求中 683 次非 200，其中 **429 限流 636 次（占该端点 23.8%）**。
 * CP 的 `middleware.RateLimit(10, 20)` 按 ClientIP 分桶，**整个 SPA 共用一个桶**，
 * 单页并发请求（联邦主查询 + stats + facets）叠加轮询就会稳定触限——**不是**偶发，
 * 原先「人手正常速度不会触限」的注释已被生产数据推翻。
 *
 * queryFn 内部若把失败吞成"失败态结果"，react-query 会视为成功、不再重试 ⇒ 用户直接
 * 看到"传输失败"（且列表被清空）。故分两层重试：
 * ① {@link getWithRateLimitRetry} 的短退避（250/600ms）吸收瞬时抖动；
 * ② 仍失败则**抛出**，交给 react-query 按本策略再退避重试（500/1000/2000ms）。
 * 合计单个请求最多 3 × 3 = 9 次尝试，足以穿过令牌桶（10 req/s、桶 20）的回补窗口，
 * 且仍在用户在意的等待范围内；超过才把限流呈现给用户。
 */
export const RATE_LIMIT_RETRY_DELAYS_MS = [250, 600] as const

/** react-query 侧的额外重试预算（次数，不含首次尝试）。 */
export const RATE_LIMIT_QUERY_RETRIES = 2
export const RATE_LIMIT_QUERY_RETRY_DELAYS_MS = [500, 1000, 2000] as const

/** 给 react-query 的通用限流重试策略：**仅 429 重试**，其它错误立即落地。 */
export function rateLimitRetry(failureCount: number, error: unknown): boolean {
  return isRateLimitError(error) && failureCount < RATE_LIMIT_QUERY_RETRIES
}

/** 与 rateLimitRetry 配套的退避（按失败次数取，越界用最后一段）。 */
export function rateLimitRetryDelay(attempt: number): number {
  // 末尾兜底：延迟表是字面量元组、恒非空，`!` 仅安抚 noUncheckedIndexedAccess
  // （合并态：主干此段写于本仓库启用该严格选项之前）。
  return RATE_LIMIT_QUERY_RETRY_DELAYS_MS[attempt] ??
    RATE_LIMIT_QUERY_RETRY_DELAYS_MS[RATE_LIMIT_QUERY_RETRY_DELAYS_MS.length - 1]!
}

/** 主动取消不是传输失败，不能触发降级或错误横幅。 */
export function isLogRequestCanceled(error: unknown): boolean {
  return isCancel(error) || (error instanceof Error && error.name === 'AbortError')
}

/** 短退避也消费取消信号，退出后不再向过期条件重发请求。 */
function waitForLogRetry(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) { reject(new CanceledError()); return }
    const finish = () => { signal?.removeEventListener('abort', abort); resolve() }
    const timer = setTimeout(finish, ms)
    const abort = () => { clearTimeout(timer); signal?.removeEventListener('abort', abort); reject(new CanceledError()) }
    signal?.addEventListener('abort', abort, { once: true })
  })
}

/** 带短退避重试的 GET：**仅对 429（限流）重试**，其它错误原样抛出。 */
export async function getWithRateLimitRetry<T>(
  path: string,
  config: Parameters<typeof api.get>[1],
  delaysMs: number[] = [...RATE_LIMIT_RETRY_DELAYS_MS],
): Promise<{ data: T }> {
  for (let attempt = 0; ; attempt++) {
    if (config?.signal?.aborted) throw new CanceledError()
    try {
      return await api.get<T>(path, config)
    } catch (error) {
		if (isLogRequestCanceled(error) || !isRateLimitError(error) || attempt >= delaysMs.length) throw error
		// delaysMs[attempt] 在 attempt < length 时必有值（上一行已先排除越界），
		// 但 TS 的 noUncheckedIndexedAccess 不知道这层关系，故显式断言。
		await waitForLogRetry(delaysMs[attempt]!, config?.signal as AbortSignal | undefined)
    }
  }
}

/** 禁用observer不等于取消；仅取消精确身份且已无活跃消费者的在途日志查询。 */
function useCancelDisabledLogQuery(namespace: string, params: Record<string, unknown>, enabled: boolean) {
  const client = useQueryClient()
  const requestHash = hashKey([namespace, params])
  useEffect(() => {
    if (enabled) return
    const pending = client.getQueryCache().get(requestHash)
    if (!pending || pending.state.fetchStatus !== 'fetching' || pending.isActive()) return
    void client.cancelQueries({ predicate: (query) => query.queryHash === requestHash && !query.isActive() })
  }, [client, enabled, requestHash])
}

/**
 * 探测联邦覆盖（与 LogsPage 筛选参数同源）。
 * 404 → degraded legacy；其它错误 → 传输失败态（非空成功）。
 */
export function useLogsFederation(
  params: Record<string, unknown>,
  options: { enabled?: boolean; refetchInterval?: number | false } = {},
) {
  // 跟随由页面在上一轮完成后调度；它只用短退避，避免轮询再次放大限流。
  const isFollowMode = asString(params.tailMode) !== undefined
  const query = useQuery({
    queryKey: ['logsFederation', params],
    // 限流耗尽必须抛出，页面保留上次行并显示失败；不能吞成成功空结果。
    // 静态查询另有外层限流预算；跟随只用可取消的250/600ms短退避，完成后再调度下一轮。
    retry: isFollowMode ? false : rateLimitRetry,
    retryDelay: rateLimitRetryDelay,
    queryFn: async ({ signal }): Promise<LogsFederationResult> => {
      try {
		const requestParams = { ...params }
		const tailMode = asString(requestParams.tailMode)
		delete requestParams.tailMode
		if (tailMode) requestParams.mode = tailMode
		const path = tailMode ? LOGS_FEDERATION_TAIL_PATH : LOGS_FEDERATION_PATH
        // 联邦查询是重活：162 目标逐目标覆盖解析，实测 6–13s（24h 全量窗曾达 12.8s）✗。
        // 全局 10s 超时会把慢查询 abort ⇒ 页面据 abort 误判"实时日志查询服务暂不可用"并回退
        // legacy（legacy 只有切换前旧数据 ⇒ 24h 窗 0 条）✗。故给联邦查询单独的 60s 预算；
        // tail 探针（0.43s）与其它请求不受影响 ✓。
        // 429（限流）由 getWithRateLimitRetry 短退避吸收；仍失败则抛出，交给页面
        // （保留上一份列表 + 限流提示），**不得**在这里吞成空结果 ✗。
        const { data } = await getWithRateLimitRetry<LogsFederationEnvelope>(path, {
          params: requestParams,
          timeout: 60_000,
          signal,
        })
        const envelope = data && typeof data === 'object' ? data : {}
        // Older CP versions silently ignore advanced/logsQL and would return
        // unfiltered rows. Do not present those rows as an advanced result.
        if ((params.advanced === true || params.advanced === 'true') && !envelope.query_meta) {
          const unsupported = new Error('LOG_ADVANCED_UNSUPPORTED')
          return buildFederationResult(transportFailureEnvelope(unsupported.message), { failure: unsupported })
        }
        // 日志查询引擎未就绪（受管 VL 未配置/未启动）时降级到经典 `/logs` 路径：
        // 平台日志与切换前的实例日志本就能从 CP 库直接读，不应因为新引擎没开就让
        // 用户连这些日志都看不到。
        //
        // 此前 federatedNotReady 只用于「显示」未就绪横幅，从未触发降级，导致未启用
        // 日志平台时日志中心整体不可用（含平台日志本身）。这里复用同一判定（它已覆盖
        // notes / errorCode / federatedReady=false / duplicate 未决）来决定降级。
        if (mapFederationEnvelope(envelope).federatedNotReady) {
          return buildFederationResult(legacyDegradeEnvelope(true), {
            degraded: true,
            failure: new Error(envelope.errorMessage ?? ((envelope.notes ?? []).join(', ') || FEDERATED_NOT_READY_MESSAGE)),
          })
        }
        return buildFederationResult(envelope, { degraded: false })
      } catch (error) {
        if (signal.aborted || isLogRequestCanceled(error)) throw error
        // 持久限流：必须抛出，让 react-query 保留上一份 data 并置 isError；
        // 返回限流信封会被当成"成功"，页面无从区分「没查到」与「查到 0 条」。
        if (isRateLimitError(error)) {
          throw error
        }
        // 「联邦不可用」一律降级为 legacy：404（未部署）、401（认证/未授权）、
        // 连接失败（ECONNREFUSED/超时）都表示该路径当前拿不到数据。
        // 此时经典 `/logs` 表格仍可用，必须回退而非让列表停留在空态
        // （此前仅 404 降级，其他失败走 transportFailureEnvelope 且 degraded=false，
        //   使 federationActive 误判为 true 并以空的 federationItems 为准 → 列表永久空白）。
        if (isFederationUnavailable(error)) {
          return buildFederationResult(legacyDegradeEnvelope(), { degraded: true, failure: error })
        }
        return buildFederationResult(
          transportFailureEnvelope(logQueryErrorMessage(error)),
          { degraded: false, failure: error },
        )
      }
    },
    enabled: options.enabled ?? true,
    refetchOnWindowFocus: false,
	refetchInterval: options.refetchInterval,
    // 覆盖状态变化不频繁；跟随轮询时由页面 key 带动 refetch。
    staleTime: 30_000,
    // 切窗口/筛选时保留上一份列表（stale-while-revalidate）：服务端命中缓存约 0.03s、
    // 冷查 1-2s，期间若清空列表会"闪白"，观感远慢于实际耗时。保留旧数据后由
    // isPlaceholderData 暴露「正在刷新」——页面据此维持 loading 语义（timeline 不判空），
    // 新数据到达即自动替换。
    placeholderData: keepPreviousData,
  })
  useCancelDisabledLogQuery('logsFederation', params, options.enabled ?? true)
  return query
}

export interface FederationStatsAggregate {
	count?: number
	sum?: number
	min?: number
	max?: number
	avg?: number
	has_min_max?: boolean
}

export interface FederationStatsPoint {
	dimensions?: Record<string, string>
	/** 后端 StatsPoint 的聚合体（wire 真源 `agg.count/sum/min/max`）。 */
	agg?: FederationStatsAggregate
	/** 兼容旧/扁平形态（部分 fixture 用顶层 count）。 */
	count?: number
}

export interface FederationAggregateResponse {
  explore_applied?: boolean
  coverage?: FederationCoverageBody
  quality?: LogsFederationEnvelope['quality']
  truncated?: boolean
  view?: LogsFederationEnvelope['view']
}

export interface FederationStatsResponse extends FederationAggregateResponse {
  points?: FederationStatsPoint[]
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

export interface FederationFacetsResponse extends FederationAggregateResponse {
  dimensions?: FederationFacetDimension[]
}

export interface FederationFieldsResponse extends FederationAggregateResponse {
  /** 文档字段全集（不是当前页的字段子集）。 */
  fields?: string[]
}

/** 聚合自身的覆盖、质量与截断分别呈现；未知质量不宣称精确或至少。 */
export function describeLogAggregate(result: FederationAggregateResponse | undefined) {
  const coverage = result?.coverage
  const quality = normalizeQuality(result?.quality)
  const reasons = normalizeReasons(coverage?.partialReasons ?? coverage?.partial_reasons)
  const partial = reasons.length > 0 || (coverage?.complete === false && coverage.pending !== true)
  const pending = coverage?.pending === true
  const qualityExact = quality?.duplicateQuality === 'EXACT' && quality.statsQuality === 'EXACT'
  const truncated = result?.truncated === true
  return {
    exact: coverage?.complete === true && !pending && reasons.length === 0 && qualityExact && !truncated,
    partial,
    pending,
    qualityExact,
    quality: quality ? `${quality.duplicateQuality} / ${quality.statsQuality}` : 'UNKNOWN',
    truncated,
    reasons,
  }
}

/** 
 * 联邦聚合统计（洞察栏「当前视图 N 个事件」）。
 *
 * 429 与主查询同样走两层重试（短退避 + react-query 退避）：聚合与主查询共用同一个
 * ClientIP 令牌桶，生产实测 stats p90=6.20s、facets p90=9.99s，本就是最容易被限流的
 * 两条请求。失败不阻断页面——洞察栏是可选装饰，行列表与横幅不受其影响。
 */
export function useLogsFederationStats(params: Record<string, unknown>, enabled: boolean) {
	const query = useQuery({
		queryKey: ['logsFederationStats', params],
		queryFn: async ({ signal }) =>
			(await getWithRateLimitRetry<FederationStatsResponse>('/logs/federation/stats', { params, signal, timeout: 60_000 })).data,
		enabled,
		retry: rateLimitRetry,
		retryDelay: rateLimitRetryDelay,
	})
  useCancelDisabledLogQuery('logsFederationStats', params, enabled)
  return query
}

/** Explicit execution acknowledgement prevents an older CP from silently ignoring explore. */
function requireFieldExploration<T extends FederationAggregateResponse>(params: Record<string, unknown>, data: T): T {
  if (params.explore === true && data.explore_applied !== true) {
    throw Object.assign(new Error('Document-field exploration unsupported by control plane'), {
      code: 'LOG_UNSUPPORTED', response: { status: 501, data: { error: 'LOG_UNSUPPORTED' } },
    })
  }
  return data
}

/** 联邦维度聚合（洞察栏级别分布）；重试策略与 stats 同源，失败同样不阻断页面。 */
export function useLogsFederationFacets(params: Record<string, unknown>, enabled: boolean) {
	const query = useQuery({
		queryKey: ['logsFederationFacets', params],
		queryFn: async ({ signal }) => requireFieldExploration(params,
			(await getWithRateLimitRetry<FederationFacetsResponse>('/logs/federation/facets', { params, signal, timeout: 60_000 })).data),
		enabled,
		retry: rateLimitRetry,
		retryDelay: rateLimitRetryDelay,
	})
  useCancelDisabledLogQuery('logsFederationFacets', params, enabled)
  return query
}

/**
 * 联邦字段枚举：查询时间窗内出现过的**文档字段全集**。
 *
 * 与当前页行的字段推断不同：Fields 由 Worker/VL 在固定 Query View 的完整范围上计算，
 * 因此可用于字段面板和后续字段值探索。unsupported/partial 必须由调用方显式展示，
 * 不把 `fields: []` 解释成「确实没有字段」。
 */
export function useLogsFederationFields(params: Record<string, unknown>, enabled: boolean) {
  const query = useQuery({
    queryKey: ['logsFederationFields', params],
    queryFn: async ({ signal }) => requireFieldExploration(params,
      (await getWithRateLimitRetry<FederationFieldsResponse>('/logs/federation/fields', { params, signal, timeout: 60_000 })).data),
    enabled,
    retry: rateLimitRetry,
    retryDelay: rateLimitRetryDelay,
  })
  useCancelDisabledLogQuery('logsFederationFields', params, enabled)
  return query
}

/** 一条流（粒度：源 + 流名）与其在窗内的条数。 */
export interface FederationStreamItem {
  log_source_id: string
  stream: string
  raw?: string
  /**
   * 窗内条数。
   *
   * ⚠️ **必须结合响应体的 complete 一起读**：complete=false 时这个数不可信。
   * 原因（实测 VictoriaLogs）：`streams` 端点一旦被上游截断，会把**所有**
   * hits 静默归零——所以「0」在那种情况下不是「没数据」，而是「不知道」。
   */
  hits: number
}

export interface FederationStreamsResponse extends FederationAggregateResponse {
  streams?: FederationStreamItem[]
  /** false ⇒ 条数不可信（上游截断）；不得把 hits=0 当成该流没有数据。 */
  complete?: boolean
}

export interface FederationStreamFieldNamesResponse extends FederationAggregateResponse {
  fields?: string[]
}

export interface FederationStreamFieldValue {
  value: string
  count: number
}

export interface FederationStreamFieldValuesResponse extends FederationAggregateResponse {
  field?: string
  values?: FederationStreamFieldValue[]
  complete?: boolean
}

/**
 * 联邦流枚举（窗内有哪些流、各多少条）。
 *
 * 与 Fields 的区别（别搞混）：
 *   - Fields   = 窗内出现过哪些**文档字段名**（level / parser_version …）
 *   - Streams  = 窗内有哪些**流**、各多少条（源 + stdout/stderr）
 *
 * 失败不阻断页面（与 stats/facets 同口径：它们都是可选装饰）。
 */
export function useLogsFederationStreams(params: Record<string, unknown>, enabled: boolean) {
  const query = useQuery({
    queryKey: ['logsFederationStreams', params],
    queryFn: async ({ signal }) =>
      (await getWithRateLimitRetry<FederationStreamsResponse>('/logs/federation/streams', { params, signal, timeout: 60_000 })).data,
    enabled,
    retry: rateLimitRetry,
    retryDelay: rateLimitRetryDelay,
  })
  useCancelDisabledLogQuery('logsFederationStreams', params, enabled)
  return query
}

/** 联邦 stream 标签维度名（实测恒为 log_source_id / stream）。 */
export function useLogsFederationStreamFieldNames(params: Record<string, unknown>, enabled: boolean) {
  const query = useQuery({
    queryKey: ['logsFederationStreamFieldNames', params],
    queryFn: async ({ signal }) =>
      (await getWithRateLimitRetry<FederationStreamFieldNamesResponse>('/logs/federation/stream-field-names', { params, signal, timeout: 60_000 })).data,
    enabled,
    retry: rateLimitRetry,
    retryDelay: rateLimitRetryDelay,
  })
  useCancelDisabledLogQuery('logsFederationStreamFieldNames', params, enabled)
  return query
}

/**
 * 联邦 stream 标签维度取值分布。
 *
 * `field` 只能是 stream 标签维度（log_source_id / stream）；传文档字段会拿到
 * 空结果而**不报错**（VL 行为），所以后端与本层都提前拦下。
 */
export function useLogsFederationStreamFieldValues(
  params: Record<string, unknown>,
  field: string,
  enabled: boolean,
) {
  const withField = { ...params, field }
  const query = useQuery({
    queryKey: ['logsFederationStreamFieldValues', withField],
    queryFn: async ({ signal }) =>
      (await getWithRateLimitRetry<FederationStreamFieldValuesResponse>('/logs/federation/stream-field-values', { params: withField, signal, timeout: 60_000 })).data,
    enabled,
    retry: rateLimitRetry,
    retryDelay: rateLimitRetryDelay,
  })
  useCancelDisabledLogQuery('logsFederationStreamFieldValues', withField, enabled)
  return query
}

/** 
 * 后端导出闸阻断（200 JSON 报告 + artifact:null，不交付附件）。
 *
 *的「覆盖不完整时不得下载成功附件」由后端闸执行：覆盖 / 质量 / 预算
 * 任一未过即不产出文件（见 internal/controlplane/router/log_federation.go 的 Export）。
 * 本错误携带 incomplete_reasons，供 UI 展示「为什么没有文件」而不是笼统失败。
 * limits 为后端本次实际采用的上限（行/字节），让「超上限」类原因能说出具体数字。
 */
export class FederatedExportIncompleteError extends Error {
  readonly reasons: string[]

  /** 后端回传的本次实际上限与实取行数；缺失（旧后端）时为 undefined。 */
  readonly limits?: { maxRows?: number; maxBytes?: number; rows?: number }

  constructor(
    reasons: string[],
    limits?: { maxRows?: number; maxBytes?: number; rows?: number },
  ) {
    super(`federated export blocked: ${reasons.join(',') || 'unknown'}`)
    this.name = 'FederatedExportIncompleteError'
    this.reasons = reasons
    this.limits = limits
  }
}

/** 导出闸阻断报告（200 JSON）。字段名与后端 wire 形态一致。 */
export interface ExportBlockedReport {
  reasons: string[]
  limits?: { maxRows?: number; maxBytes?: number; rows?: number }
}

function asPositiveNumber(v: unknown): number | undefined {
  return typeof v === 'number' && Number.isFinite(v) && v > 0 ? v : undefined
}

/** 
 * 从导出闸阻断响应体（JSON 文本）提取 incomplete_reasons 与 limits；非法输入返回空。
 *
 * limits 供 UI 把「预算超了」说成「单次最多 N 行」——没有它，用户只知道失败、
 * 不知道该缩小时间范围还是该调大配置（生产实测：超 10000 行时只看到笼统提示）。
 */
export function parseExportBlockedReport(text: string): ExportBlockedReport {
  try {
    const body = JSON.parse(text) as { incomplete_reasons?: unknown; limits?: unknown }
    const reasons = Array.isArray(body.incomplete_reasons)
      ? body.incomplete_reasons.filter(
          (r): r is string => typeof r === 'string' && r.length > 0,
        )
      : []
    let limits: ExportBlockedReport['limits']
    if (body.limits && typeof body.limits === 'object' && !Array.isArray(body.limits)) {
      const raw = body.limits as Record<string, unknown>
      limits = {
        maxRows: asPositiveNumber(raw.max_rows),
        maxBytes: asPositiveNumber(raw.max_bytes),
        rows: asPositiveNumber(raw.rows),
      }
    }
    return { reasons, limits }
  } catch {
    return { reasons: [] }
  }
}

/** 从导出闸阻断响应体提取 incomplete_reasons；非法输入返回空数组。 */
export function parseExportBlockedReasons(text: string): string[] {
  return parseExportBlockedReport(text).reasons
}

/** 
 * 单次导出默认请求行数上限。
 *
 * 为什么导出**必须**带显式 limit（原实现删掉了它）：
 * limit 是联邦查询的「本页最多返回多少行」参数（LogsPage 列表用 PAGE_SIZE=100）；
 * 导出场景删掉它本是合理的「不按页取」意图，但后端把**缺失的 limit 当 0**、
 * 0 又回退到服务端默认上限（10000 行，见 internal/controlplane/logcoord/export.go）。
 * 于是「数据超 10000 行」必然被判超限，而前端又没有任何途径调大 —— 导出永久失败。
 *
 * 现在改为显式带上请求上限：既表达「导出要全量」的意图，也让后端有机会
 * 按请求侧参数执行（请求侧只能收紧服务端上限，不能放大，故这不会成为资源入口）。
 * 服务端上限由 log_export.max_rows / max_bytes 配置（默认 10000 / 64MiB）。
 */
export const FEDERATED_EXPORT_REQUEST_LIMIT = 1_000_000

/** 在同一联邦 Query View 上物化并下载经覆盖校验的 NDJSON。 */
export async function exportFederatedLogs(
  params: Record<string, unknown>,
  options: { signal?: AbortSignal; pageItems?: readonly unknown[] } = {},
): Promise<void> {
  const exportParams = { ...params }
  delete exportParams.page
  delete exportParams.pageSize
  if (exportParams.range !== 'page') delete exportParams.cursor
  // 当前页必须沿固定视图的入页游标和原页大小；全窗才使用导出预算。
  if (exportParams.range === 'page') {
    if (!asString(exportParams.viewId) || asPositiveNumber(exportParams.limit) === undefined) throw new Error('LOG_PAGE_VIEW_REQUIRED')
  } else if (asPositiveNumber(exportParams.limit) === undefined) {
    exportParams.limit = FEDERATED_EXPORT_REQUEST_LIMIT
  }
  const response = await api.post(LOGS_FEDERATION_PATH + '/export', null, {
    params: exportParams,
    responseType: 'blob',
    timeout: 120_000,
    signal: options.signal,
  })
  const contentType = String(response.headers['content-type'] ?? '')
  if (!contentType.includes('application/x-ndjson')) {
    // 非 NDJSON = 导出闸阻断（200 + 报告 JSON）。解析原因与 limits 后抛出结构化错误；
    // 非 JSON（网关错误页等）时 reasons 为空，调用方走泛化失败文案。
    let report: ExportBlockedReport = { reasons: [] }
    try {
      report = parseExportBlockedReport(await (response.data as Blob).text())
    } catch {
      // Blob 读取失败：保持空 reasons。
    }
    throw new FederatedExportIncompleteError(report.reasons, report.limits)
  }
  if (options.pageItems) {
    const text = await (response.data as Blob).text()
    const items: unknown[] = text.split('\n').filter((line) => line.trim()).map((line) => JSON.parse(line) as unknown)
    if (!sameLogItems(options.pageItems, items)) throw new Error('LOG_PAGE_CHANGED')
  }
  downloadLogBlob(response.data as Blob, 'logs-federation', options.signal)
}
