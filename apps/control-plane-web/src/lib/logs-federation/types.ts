/**
 * 日志联邦 coverage / quality 类型（FR-482 foundation，契约继承 FR-473）。
 *
 * 字段语义以 `docs/specs/worker-log-platform-contract/spec.md` 为准：
 * - `complete` / `partial_reasons` / `targets` / `enumeration_state` 是正交维度；
 * - `duplicate_quality`、`stats_quality` 独立于完整性，不得混为一谈；
 * - `nextCursor=null` 仅表示当前 Query View 枚举结束，不表示全量历史完整。
 *
 * 本模块只声明前端消费所需的形状；后端 proto 冻结前允许宽松字符串 reason，
 * 已知 reason 进入 {@link PARTIAL_REASONS} 常量，未知 reason 原样透传（不得静默丢弃）。
 */

/** FR-473 Coverage.enumeration_state。 */
export type EnumerationState = 'OPEN' | 'EXHAUSTED' | 'STALE' | 'CANCELLED'

/** FR-473 Quality.duplicate_quality / stats_quality 的已登记取值。 */
export type QualityLevel = 'EXACT' | 'APPROXIMATE' | 'UNRESOLVED' | 'UNKNOWN'

/**
 * 已登记的 partial_reasons / 失败原因码（FR-482 §3.1 + FR-473 §6.3）。
 * 后端未冻结枚举时仍可能出现其它字符串；分类逻辑对未知码走 generic partial。
 */
export const PARTIAL_REASONS = {
  /** 查询引擎 / Worker 日志数据面未就绪（启动恢复中）。 */
  ENGINE_NOT_READY: 'ENGINE_NOT_READY',
  /** 老 Worker Unimplemented / 节点未启用日志平台能力。 */
  LOG_UNSUPPORTED: 'LOG_UNSUPPORTED',
  /** Legacy 数据源：仅有来源标识与时间覆盖，不在联邦 Query View 内。 */
  LEGACY: 'LEGACY',
  /** 归档回灌（Rehydrate）失败。 */
  REHYDRATE_FAILED: 'REHYDRATE_FAILED',
  /** 采集积压：上游写入未追上，查询结果滞后。 */
  BACKLOG: 'BACKLOG',
  /** 结果 / Facets 被截断（limit、预算或高基数）。 */
  TRUNCATED: 'TRUNCATED',
  /** 目标节点 / Worker 离线。 */
  OFFLINE: 'OFFLINE',
  /** 归档层缺失（Archive 开启但副本不可用）。 */
  ARCHIVE_MISSING: 'ARCHIVE_MISSING',
  /** closed_visible_seq / 投影存在数据缺口。 */
  GAP: 'GAP',
  /** Query View 失效（集合变化 / 权限 / 过期）。 */
  VIEW_STALE: 'VIEW_STALE',
  /** 重复事件未对账（duplicate_quality=UNRESOLVED）。 */
  DUPLICATE_UNRESOLVED: 'DUPLICATE_UNRESOLVED',
  /** 权限撤销（下载前复核会再次拦截）。 */
  PERMISSION_REVOKED: 'PERMISSION_REVOKED',
  /** 查询被取消。 */
  CANCELLED: 'CANCELLED',
  /** Worker / CP 资源预算不足。 */
  BUDGET_EXCEEDED: 'BUDGET_EXCEEDED',
  /** 导出物化不完整，不得发布下载。 */
  EXPORT_INCOMPLETE: 'EXPORT_INCOMPLETE',
  /** 启动恢复中，受影响范围 PARTIAL/RECOVERY_REQUIRED。 */
  RECOVERY_REQUIRED: 'RECOVERY_REQUIRED',
} as const

export type PartialReasonCode = (typeof PARTIAL_REASONS)[keyof typeof PARTIAL_REASONS]

/** partial_reasons 元素：已登记码或后端透传的原始字符串。 */
export type PartialReason = PartialReasonCode | (string & {})

/** 单目标覆盖状态（节点 / 实例 / Worker）。 */
export type TargetQueryStatus =
  | 'ONLINE'
  | 'OFFLINE'
  | 'NOT_READY'
  | 'LEGACY'
  | 'ARCHIVE_MISSING'
  | 'INCLUDED'
  | 'EXCLUDED'
  | 'UNKNOWN'

/** 存储层级（HOT/COLD/DEEP/RAW）。 */
export type StorageTier = 'HOT' | 'COLD' | 'DEEP' | 'RAW' | (string & {})

/** FR-473 TargetCoverage 的前端投影。 */
export interface TargetCoverage {
  /** 节点 / 实例 / 日志源标识。 */
  targetId: string
  /** 可选展示名。 */
  name?: string
  /** 该目标在本次查询中的层级；Legacy 可能无 tier。 */
  tier?: StorageTier
  /** 查询侧状态。 */
  status: TargetQueryStatus
  /** 是否实际纳入本次结果集合。 */
  included?: boolean
  /** 未纳入 / 降级的原因码。 */
  reason?: PartialReason
  /** closed_visible_seq 摘要（可选，仅用于展示）。 */
  closedVisibleSeq?: string
  /** Legacy 数据时间覆盖（FR-482：Legacy 必须有来源标识和时间覆盖）。 */
  legacyRange?: { from?: string; to?: string }
  /** 可观测缺口数（可选）。 */
  gapCount?: number
}

/** FR-473 Coverage。 */
export interface Coverage {
  /** true = 本次结果集在声明的授权目标与时间窗内完整。 */
  complete: boolean
  /** 结构化部分原因；失败 / 降级时不得省略。 */
  partialReasons: PartialReason[]
  /** 目标覆盖摘要。 */
  targets: TargetCoverage[]
  /** 当前 view 枚举状态；与 complete 正交。 */
  enumerationState?: EnumerationState
}

/** FR-473 Quality（duplicate / stats 正交维度）。 */
export interface Quality {
  duplicateQuality: QualityLevel | (string & {})
  statsQuality: QualityLevel | (string & {})
}

/** EngineNotReady 明细（FR-482 用户可见失败态之一）。 */
export interface EngineNotReadyDetail {
  kind: 'engine_not_ready'
  reason: PartialReason
  targetId?: string
  message?: string
}

/** Legacy 数据源明细。 */
export interface LegacyDetail {
  kind: 'legacy'
  sourceId?: string
  targetId?: string
  timeFrom?: string
  timeTo?: string
}

/** Rehydrate 失败明细。 */
export interface RehydrateFailedDetail {
  kind: 'rehydrate_failed'
  targetId?: string
  message?: string
}

/** 采集积压明细。 */
export interface BacklogDetail {
  kind: 'backlog'
  targetId?: string
  pendingCount?: number
}

/** 截断明细。 */
export interface TruncatedDetail {
  kind: 'truncated'
  targetId?: string
  limit?: number
  dimension?: string
}

/** 用户可见失败态明细联合（UI 横幅可用）。 */
export type PartialStateDetail =
  | EngineNotReadyDetail
  | LegacyDetail
  | RehydrateFailedDetail
  | BacklogDetail
  | TruncatedDetail

/** 导出产物状态。 */
export type ExportStatus = 'READY' | 'PENDING' | 'FAILED' | 'INCOMPLETE' | (string & {})

/**
 * 日志联邦查询 / 导出响应的前端信封。
 * 覆盖 useLogs 未来升级后的字段，以及错误注入 / mock 的传输失败形态。
 */
export interface LogFederationResponse {
  /** 传输 / HTTP 层是否成功；undefined 表示调用方未提供（按成功路径继续看 coverage）。 */
  ok?: boolean
  viewId?: string
  coverage?: Coverage | null
  quality?: Quality | null
  items?: unknown[] | null
  /** 下一页游标；null 仅表示当前 view 枚举结束。 */
  nextCursor?: string | null
  exhausted?: boolean
  /** 结构化错误码（VIEW_STALE / LOG_UNSUPPORTED / PERMISSION_REVOKED…）。 */
  errorCode?: string
  errorMessage?: string
  /** 导出作业状态；仅导出路径有意义。 */
  exportStatus?: ExportStatus
  /** 导出是否已通过完整性校验（FR-482：只有校验后可下载）。 */
  exportVerified?: boolean
}

/** 从 Coverage.targets 提取的目标统计。 */
export interface CoverageTargetStats {
  total: number
  included: number
  offline: number
  notReady: number
  legacy: number
  archiveMissing: number
  excluded: number
}

/** formatCoverageSummary 结构化结果。 */
export interface FormattedCoverageSummary {
  /**
   * 横幅档位。
   * - complete：显式 complete 且无 partial / 未解析重复
   * - partial：有 coverage 但不完整
   * - failed：无 coverage（查询失败信封），**禁止**当成空成功
   * - unknown：coverage 形状无法识别
   */
  status: 'complete' | 'partial' | 'failed' | 'unknown'
  /** 仅 status=complete 时为 true。 */
  complete: boolean
  /**
   * 是否允许 UI 把「0 条结果」呈现为成功空态。
   * 失败 / 不完整 / coverage 缺失时一律 false——失败不得显示为空成功。
   */
  emptySuccessAllowed: boolean
  /** 主摘要 i18n key（logsFederation.coverage.*）。 */
  summaryKey: string
  /** 原始 reason 码（保留未知码）。 */
  reasons: PartialReason[]
  /** reason → i18n key（logsFederation.reasons.*）。 */
  reasonKeys: string[]
  targetStats: CoverageTargetStats
  enumerationState?: EnumerationState
  /** 重复未对账时为 true（即使 coverage.complete 也不得当完整成功）。 */
  duplicateUnresolved: boolean
}

/** classifyLogViewState 的视图状态。 */
export type LogViewState =
  | 'query_failed'
  | 'view_stale'
  | 'engine_not_ready'
  | 'rehydrate_failed'
  | 'offline'
  | 'archive_missing'
  | 'backlog'
  | 'truncated'
  | 'legacy'
  | 'gap'
  | 'partial'
  | 'ready_complete'
  | 'unknown'

/** classifyLogViewState 结果。 */
export interface ClassifiedLogViewState {
  state: LogViewState
  /** 是否为失败态（传输错误 / VIEW_STALE / 引擎未就绪等，不可渲染成空成功）。 */
  isFailure: boolean
  /** 是否为不完整结果（partial 各态）。 */
  isIncomplete: boolean
  /** 空结果是否可作为「成功且完整」展示。 */
  emptySuccessAllowed: boolean
  /** 是否允许导出下载入口（仅在覆盖完整且校验通过时为 true）。 */
  allowsExportDownload: boolean
  /** 当前 view 枚举是否已结束（nextCursor=null 或 EXHAUSTED）；≠ 全量历史完整。 */
  viewEnumerationEnded: boolean
  /**
   * 全量历史是否完整枚举。
   * **绝不能**仅因 nextCursor=null 为 true；还需 coverage.complete 且无缺口类 reason。
   */
  historyFullyEnumerated: boolean
  /** 主横幅 i18n key（logsFederation.viewState.*）。 */
  bannerKey: string
  /** 建议动作 i18n key（retry / openNodes / rehydrate…）。 */
  actionKeys: string[]
  /** 覆盖摘要。 */
  coverage: FormattedCoverageSummary
  /** 主原因码（取最高优先级）。 */
  primaryReason?: PartialReason
  /** 导出阻断原因 i18n key；allowsExportDownload=true 时为 undefined。 */
  exportBlockedKey?: string
}

/** 导出下载 affordance。 */
export interface ExportDownloadAffordance {
  /** UI 是否展示下载入口。 */
  showDownload: boolean
  /** 阻断说明 i18n key；showDownload=false 时必有。 */
  blockedKey: string
  /** 允许下载时的说明 key（可选提示：下载前仍会复核权限）。 */
  allowedKey?: string
}

/** 薄 UI 适配：Coverage 横幅 props（LogsPage 未改版前可直接消费）。 */
export interface CoverageBannerProps {
  visible: boolean
  tone: 'success' | 'warning' | 'danger' | 'info'
  titleKey: string
  reasonKeys: string[]
  actionKeys: string[]
  showRetry: boolean
  showExport: boolean
  exportBlockedKey?: string
}
