import type { BotInfo, BotConfig, BotSummary, BotSummaryGroup } from '@/api/bots'
import type { NodeInfo } from '@/api/nodes'

/** 实例树徽标用的聚合计数：在线 / 总数。 */
export interface InstanceBotBadge {
  total: number
  online: number
}

/**
 * 把 `GET /bots/summary?groupBy=instance` 的分组结果索引为「实例 id → 在线/总数」。
 * 分组 key 为实例 id 字符串，便于实例树逐行 O(1) 取徽标（FR-039，单次摘要覆盖可见集）。
 */
export function indexBotBadgesByInstance(
  groups: BotSummaryGroup[] | undefined,
): Map<number, InstanceBotBadge> {
  const map = new Map<number, InstanceBotBadge>()
  for (const g of groups ?? []) {
    const id = Number(g.key)
    if (Number.isNaN(id)) continue
    map.set(id, { total: g.total, online: g.online })
  }
  return map
}

/**
 * Bot 状态的语义分桶（FR-039）。
 * 后端 status 取值为 connected/connecting/disconnected/error；
 * 概览卡片与状态点按以下四类语义聚合，未知值归入 offline 兜底。
 */
export type BotStatusKind = 'online' | 'connecting' | 'offline' | 'error'

/** 把后端 Bot status 字符串映射到语义分桶。 */
export function botStatusKind(status: string): BotStatusKind {
  switch (status) {
    case 'connected':
      return 'online'
    case 'connecting':
      return 'connecting'
    case 'error':
      return 'error'
    default:
      // disconnected 及任何未知值统一视为离线
      return 'offline'
  }
}

/** 概览卡片的四项计数：总计 / 在线 / 连接中 / 异常。 */
export interface BotStatusCounts {
  total: number
  online: number
  connecting: number
  error: number
}

/**
 * 从聚合摘要派生概览卡片计数（FR-039）。
 * 优先用 `byStatus`（后端聚合，覆盖全量而非当前页），避免按分页数据低估；
 * 摘要缺失时返回全零。在线数取自摘要的 connected。
 */
export function summaryCounts(summary: BotSummary | undefined): BotStatusCounts {
  if (!summary) return { total: 0, online: 0, connecting: 0, error: 0 }
  const by = summary.byStatus ?? {}
  return {
    total: summary.total,
    online: by.connected ?? 0,
    connecting: by.connecting ?? 0,
    error: by.error ?? 0,
  }
}

/** 列表分组维度：按行为 或 按状态语义分桶。 */
export type BotGroupBy = 'behavior' | 'status'

/** 一个 Bot 分组：分组键 + 该组在「当前页」的成员。 */
export interface BotGroup {
  /** 分组键（behavior 原值，或 BotStatusKind） */
  key: string
  bots: BotInfo[]
}

const STATUS_KIND_ORDER: BotStatusKind[] = ['online', 'connecting', 'error', 'offline']

/**
 * 把「当前页」的 Bot 列表按维度分组，仅对已加载的页内数据分组（不拉全量）。
 * 按状态分组时键为语义分桶并按 online→connecting→error→offline 固定排序；
 * 按行为分组时键为 behavior 原值并按名称排序，保证渲染顺序稳定。
 */
export function groupBots(bots: BotInfo[], groupBy: BotGroupBy): BotGroup[] {
  const map = new Map<string, BotInfo[]>()
  for (const bot of bots) {
    const key = groupBy === 'status' ? botStatusKind(bot.status) : bot.behavior || 'unknown'
    const list = map.get(key)
    if (list) list.push(bot)
    else map.set(key, [bot])
  }

  const keys = [...map.keys()]
  if (groupBy === 'status') {
    keys.sort(
      (a, b) =>
        STATUS_KIND_ORDER.indexOf(a as BotStatusKind) - STATUS_KIND_ORDER.indexOf(b as BotStatusKind),
    )
  } else {
    keys.sort((a, b) => a.localeCompare(b))
  }

  return keys.map((key) => ({ key, bots: map.get(key)! }))
}

/** 安全解析 Bot 的 JSON 配置字符串，解析失败时返回占位（与 BotsPage 一致）。 */
export function parseBotConfig(config: string): BotConfig {
  try {
    return JSON.parse(config) as BotConfig
  } catch {
    return { server: '', port: 0, auth: '' }
  }
}

/**
 * 新建 Bot 时的连接地址预填：默认连到「所在节点 host + 该实例实际 server-port」，可改。
 * 这样 Bot 默认就指向它所属的实例，避免用户手填端口出错导致连不进服务器。
 * serverPort 缺省（未分配）时回退 MC 默认 25565；节点 host 缺省回退回环。
 */
export function suggestBotServer(
  node: NodeInfo | undefined,
  serverPort?: number,
): { server: string; port: number } {
  return {
    server: node?.host || '127.0.0.1',
    port: serverPort && serverPort > 0 ? serverPort : 25565,
  }
}
