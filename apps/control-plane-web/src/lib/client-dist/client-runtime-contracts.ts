/**
 * 客户端运行态契约（FR-265，ADR-097）。
 * 与后端 model 对齐的纯数据类型，供 `@jianmanager/ui` 内视图逻辑与应用侧 api 共用。
 */

export interface ClientRuntimeState {
  id: number
  channelId: string
  machineId: string
  ip: string
  platform: string
  javaVersion: string
  launcher: string
  coreVersion: string
  localVersion: number
  firstSeenAt: string
  lastHeartbeatAt: string
  createdAt?: string
  updatedAt?: string
}

/** 客户端 Tab KPI：启动心跳 + 更新结果遥测。 */
export interface ClientRuntimeSummary {
  recentStarted: number
  todayStarted: number
  recentStarts: number
  todayStarts: number
  updateSuccessRate: number
  updateFailureRate: number
}

export interface RuntimeVersionCount {
  version: number
  count: number
}

export interface RuntimeStringCount {
  value: string
  count: number
}

export interface RuntimeLagCount {
  lag: number
  count: number
}

export interface RuntimeUpdateSeriesPoint {
  ts: string
  success: number
  failStatic: number
  rolledBack: number
  error: number
}

/** 客户端运行态聚合响应（FR-265）。 */
export interface ClientRuntimeOverview {
  channelId: string
  from: string
  to: string
  summary: ClientRuntimeSummary
  items: ClientRuntimeState[]
  runtimeVersionDist: RuntimeVersionCount[]
  coreVersionDist: RuntimeStringCount[]
  platformDist: RuntimeStringCount[]
  launcherDist: RuntimeStringCount[]
  lagDist: RuntimeLagCount[]
  updateResultSeries: RuntimeUpdateSeriesPoint[]
}
