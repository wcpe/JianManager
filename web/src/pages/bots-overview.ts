import type { BotListParams, BotSummary, BotSummaryGroup } from '@/api/bots'

/**
 * 全局 Bot 总览页（FR-040）的纯逻辑：状态分类、健康条分段、分组→筛选映射、分布计数。
 * 抽成无 React 依赖的模块以便单测（参照 console/instance-tree.ts 约定）。
 */

/** 后端 Bot 状态枚举（model.BotStatus）。`disconnected` 不是后端真实状态，故不在此列。 */
export const BOT_STATUSES = ['pending', 'connecting', 'connected', 'error', 'stopped'] as const
export type BotStatusKind = (typeof BOT_STATUSES)[number]

/** 概览卡片用的状态计数：在线=connected，连接中=connecting，异常=error。 */
export interface BotStatusCounts {
  total: number
  online: number
  connecting: number
  error: number
}

/** 从全局摘要（无 groupBy）提取概览卡片计数。byStatus 缺失维度按 0 处理。 */
export function statusCounts(summary?: BotSummary): BotStatusCounts {
  const by = summary?.byStatus ?? {}
  return {
    total: summary?.total ?? 0,
    online: by.connected ?? 0,
    connecting: by.connecting ?? 0,
    error: by.error ?? 0,
  }
}

/** 健康条的一段：占比 0~1，用于按比例渲染宽度。 */
export interface HealthSegment {
  kind: 'online' | 'other'
  count: number
  ratio: number
}

/**
 * 把一个分组的 total/online 拆成健康条分段。
 *
 * 后端摘要分组只暴露 `online`(=connected) 与 `total` 两个量（见 BotSummaryGroup），
 * 无法细分 connecting/error，故健康条呈现「在线 vs 其余」两段；其余段含连接中/异常/已停止等。
 * total<=0 时返回空数组（调用方渲染为空轨道）。
 */
export function healthSegments(total: number, online: number): HealthSegment[] {
  if (total <= 0) return []
  const safeOnline = Math.max(0, Math.min(online, total))
  const other = total - safeOnline
  const segments: HealthSegment[] = []
  if (safeOnline > 0) segments.push({ kind: 'online', count: safeOnline, ratio: safeOnline / total })
  if (other > 0) segments.push({ kind: 'other', count: other, ratio: other / total })
  return segments
}

/** 分组维度。默认按实例，另支持节点/状态/行为。 */
export type GroupByDim = 'instance' | 'node' | 'status' | 'behavior'

export const GROUP_BY_DIMS: GroupByDim[] = ['instance', 'node', 'status', 'behavior']

/** 全局工具栏筛选条件（搜索 + 节点 + 状态），分组摘要与批量目标共用。 */
export interface OverviewFilter {
  q?: string
  nodeId?: number
  status?: string
}

/** 把工具栏筛选拼成 useBotSummary/useBots 的查询参数（剔除空值）。 */
export function toListParams(filter: OverviewFilter): BotListParams {
  const params: BotListParams = {}
  if (filter.q) params.q = filter.q
  if (filter.nodeId != null) params.nodeId = filter.nodeId
  if (filter.status) params.status = filter.status
  return params
}

/**
 * 把「某个分组」解析为批量操作 / 展开分页所需的精确筛选维度。
 * 分组键叠加在当前工具栏筛选之上：instance/node 键是数字 id，status/behavior 键是字符串。
 * 分组维度对应的筛选字段会被该组键覆盖（如按实例分组时 instanceId 取组键）。
 */
export function groupFilter(
  dim: GroupByDim,
  group: BotSummaryGroup,
  base: OverviewFilter,
): BotListParams {
  const params = toListParams(base)
  switch (dim) {
    case 'instance':
      params.instanceId = Number(group.key)
      break
    case 'node':
      params.nodeId = Number(group.key)
      break
    case 'status':
      params.status = group.key
      break
    case 'behavior':
      params.behavior = group.key
      break
  }
  return params
}

/** 分布计数：实例数 / 节点数（来自按 instance / node 分组摘要的组数量）。 */
export interface Distribution {
  instances: number
  nodes: number
}

export function distribution(
  byInstance?: BotSummary,
  byNode?: BotSummary,
): Distribution {
  return {
    instances: byInstance?.groups?.length ?? 0,
    nodes: byNode?.groups?.length ?? 0,
  }
}
