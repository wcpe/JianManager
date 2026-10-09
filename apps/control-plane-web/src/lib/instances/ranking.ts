/**
 * 全局排行的契约（FR-469）。
 *
 * 归包理由：排行面板与它的代表值格式化纯逻辑都要读它，应用侧 API 层改从包 import。
 */

/** 可切换的排行指标（与后端 rankingSupportedMetrics 对齐）。 */
export type RankingMetric = 'inst_tps' | 'inst_mspt' | 'inst_cpu_pct' | 'inst_heap_used' | 'inst_players_online'

/** 排行一项（FR-469）。 */
export interface RankingItem {
  instanceId: number
  instanceUuid: string
  name: string
  nodeUuid: string
  value: number
  rank: number
  sampledAt: string
}

/** 排行结果（FR-469）。scoped=true 表示非管理员受限视图。 */
export interface RankingResult {
  metricKey: string
  order: 'asc' | 'desc'
  windowSeconds: number
  scoped: boolean
  skippedNoData: number
  items: RankingItem[]
}
