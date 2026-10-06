/**
 * Bot 域的核心契约（FR-038 / FR-039）。
 *
 * 归包理由：Bot 段视图与 Bot 列表纯逻辑都要读它，应用侧 API 层改从包 import，
 * 避免两处各写一份。实现（useBots / useBotBatch 等 hook）仍留应用侧。
 */

/** Bot 连接配置。 */
export interface BotConfig {
  server: string
  port: number
  auth: string
}

/** 一条 Bot。 */
export interface BotInfo {
  id: number
  uuid: string
  instanceId: number
  /**
   * 实例展示名（后端已回填）。有了它，Bot 列表不必为了显示名字而拉全量实例列表。
   * 实例被删时缺省，前端回退显示 #id。
   */
  instanceName?: string
  name: string
  status: string
  /** 最近一次委托 Worker 失败的原因（status=error 时非空，如 bot 依赖未装）。 */
  lastError?: string
  /** Bot 连接配置，后端以 JSON 字符串存储。 */
  config: string
  behavior: string
  workerId: string
  createdAt: string
  updatedAt: string
}

/** 创建 Bot 请求体。 */
export interface CreateBotRequest {
  instanceId: number
  name: string
  config: BotConfig
  behavior: string
}

/** Bot 列表筛选条件（分页 + 多维过滤，FR-038）。 */
export interface BotListParams {
  page?: number
  pageSize?: number
  instanceId?: number
  nodeId?: number
  status?: string
  behavior?: string
  /** 关键字，匹配 name 或 uuid。 */
  q?: string
}

/** 分页列表响应。 */
export interface BotListResponse {
  items: BotInfo[]
  total: number
  page: number
  pageSize: number
}

/** 摘要分组计数。 */
export interface BotSummaryGroup {
  key: string
  label: string
  total: number
  online: number
}

/** Bot 计数聚合（FR-038），不含逐条 Bot。 */
export interface BotSummary {
  total: number
  byStatus: Record<string, number>
  groupBy?: string
  groups?: BotSummaryGroup[]
}

/** 批量操作动作。 */
export type BotBatchAction = 'set-behavior' | 'start' | 'stop' | 'delete'

/** 批量操作筛选条件（与列表筛选维度一致）。 */
export interface BotBatchFilter {
  instanceId?: number
  nodeId?: number
  status?: string
  behavior?: string
  q?: string
}

/** 批量操作请求，目标由 ids 或 filter 二选一指定。 */
export interface BotBatchRequest {
  action: BotBatchAction
  ids?: number[]
  filter?: BotBatchFilter
  behavior?: string
  target?: string
}

/** 批量操作结果计数。 */
export interface BotBatchResult {
  action: string
  requested: number
  succeeded: number
  failed: number
  skipped: number
  errors: { botId: number; error: string }[]
}
