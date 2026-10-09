/**
 * 客户端分发事件契约（ADR-097）。
 * 与后端 model 对齐的纯数据类型，供 `@jianmanager/ui` 内视图逻辑与应用侧 api 共用。
 */

import type { StatsIP } from './client-dist-stats-contracts'

export interface ClientDistEvent {
  id: number
  channelId: string
  machineId: string
  playerName?: string
  coreVersion?: string
  ip: string
  /** manifest | artifact。 */
  kind: string
  version: number
  artifactSha: string
  bytes: number
  /** HTTP 状态码（200/206/304/401/404/503…）。 */
  status: number
  /** 语义错误码（FR-249）：成功为空；失败如 INVALID_CLIENT_KEY/NO_LATEST_VERSION/ARTIFACT_NOT_FOUND/SIGN_KEY_NOT_CONFIGURED。 */
  errCode: string
  /** 可读错误原因（FR-265）。 */
  errReason?: string
  /** 请求方法与脱敏路径（不含 query）。 */
  method?: string
  path?: string
  /** 响应 ETag 快捷列。 */
  etag?: string
  durationMs: number
  createdAt: string
}

/** 分发事件检索过滤（FR-249）：空/undefined 字段不约束。 */
export interface ClientDistEventFilter {
  channelId?: string
  machineId?: string
  ip?: string
  /** manifest | artifact。 */
  kind?: string
  /** 成功/失败维度：success（0<status<400）| failure（status>=400）| 空（全部）。 */
  outcome?: 'success' | 'failure' | ''
  errCode?: string
  version?: number
  limit?: number
  /** 平台管理员端点门控；非管理员不发起请求。 */
  enabled?: boolean
}

export interface ClientDistEventSearchFilter extends ClientDistEventFilter {
  artifactSha?: string
  runtimeVersion?: number
  coreVersion?: string
  platform?: string
  lag?: number
  page?: number
  pageSize?: number
}

export interface ClientDistEventPage {
  items: ClientDistEvent[]
  page: number
  pageSize: number
  total: number
}

export interface ClientDistEventDetail extends ClientDistEvent {
  requestBody: string
  responseBody: string
  requestHeaders: Record<string, string>
  responseHeaders: Record<string, string>
}

export interface ClientDistRealtimeSummary {
  manifestPulls: number
  artifactPulls: number
  errorRequests: number
  activeMachines: number
}

export interface ClientDistRatePoint {
  ts: string
  manifest: number
  artifact: number
  error: number
}

export interface ClientDistRecentError {
  id: number
  time: string
  channelId: string
  kind: string
  target: string
  ip: string
  status: number
  errCode: string
}

export interface ClientDistRealtime {
  summary1h: ClientDistRealtimeSummary
  requestRate24h: ClientDistRatePoint[]
  recentErrors: ClientDistRecentError[]
  topIps1h: StatsIP[]
}

export interface ClientDistErrorCount {
  errCode: string
  count: number
}

export interface ClientDistFailureSample {
  id: number
  time: string
  channelId: string
  kind: string
  errCode: string
  errReason: string
  status: number
  ip: string
  machineId: string
}

export interface ClientDistErrorSummary {
  from: string
  to: string
  topErrors: ClientDistErrorCount[]
  samples: ClientDistFailureSample[]
}
