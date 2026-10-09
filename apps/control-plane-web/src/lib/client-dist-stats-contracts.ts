/**
 * 客户端分发统计契约（FR-095，ADR-097）。
 * 与后端 model 对齐的纯数据类型，供 `@jianmanager/ui` 内视图逻辑与应用侧 api 共用。
 */

import type { ClientDistObservabilitySummary, ClientDistObservabilityCompare } from './client-dist-observability-contracts'

/** 下载量按日点（FR-095）。 */
export interface StatsDayPoint {
  day: string
  requests: number
  bytes: number
}

/** 版本分布项。 */
export interface StatsVersion {
  version: number
  requests: number
}

/** 请求结果分布项（success|failure，来自 client_dist_events）。 */
export interface StatsResult {
  result: string
  count: number
}

/** 来源 IP 分布项。 */
export interface StatsIP {
  ip: string
  count: number
}

/** 分发统计复合视图（FR-095，来自 FR-093/094/092 聚合）。 */
export interface ClientDistStats {
  channelId: string
  days: number
  downloads: StatsDayPoint[]
  versions: StatsVersion[]
  results: StatsResult[]
  successRate: number
  failureRate: number
  rollbackRate: number
  activeMachines: number
  topIps: StatsIP[]
}

/** 版本/平台/滞后分布项（区间内跨桶合并）。 */
export interface ClientDistDistItem {
  version?: number
  os?: string
  lag?: number
  count: number
}

/** 小时桶时序点（series[]，按 ts 升序；跨频道时同小时合并；缺数小时无点）。FR-218 分发监控页画时序趋势消费。 */
export interface ClientDistSeriesPoint {
  ts: string
  manifestPulls: number
  artifactPulls: number
  downloadBytes: number
  activeMachines: number
  updateTotal: number
  updateSuccess: number
  updateFailStatic: number
  updateRolledBack: number
  updateError: number
}

/**
 * 客户端分发观测复合视图（FR-217，见 ADR-049）。
 * 平台统计页（FR-220）只取 summary + 分布；分发监控页（FR-218）额外画 series 时序趋势。
 */
export interface ClientDistObservability {
  channelId: string
  from: string
  to: string
  series: ClientDistSeriesPoint[]
  summary: ClientDistObservabilitySummary
  /** FR-428：前一等长窗口同环比基数（缺省=后端未升级，前端不渲染同环比）。 */
  compare?: ClientDistObservabilityCompare
  versionDist: ClientDistDistItem[]
  platformDist: ClientDistDistItem[]
  lagDist: ClientDistDistItem[]
}

/**
 * 客户端分发观测（FR-217）：省略 channelId=跨频道总。
 * FR-425/428：窗口支持预设 range 或任意 from/to（RFC3339，后端 parseObsRange 已支持）；summary.compare=前一等长窗口同环比。
 * **平台管理员**端点：非管理员返 403 → 调用方据 query error 局部降级（retry:false 让 403 快速失败、不重试）。
 */
export type ClientDistWindow = { range: string } | { from: string; to: string }
