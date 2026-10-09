import api from '@/api/client'

/**
 * 经济定制页只读查询（FR-123，见 ADR-028）。
 *
 * 消费 FR-122 已落地的平台级只读端点（CP 自有汇聚镜像，非业务真源）+ FR-123 新增的旁路排行端点：
 *   - 余额：GET /business/economy/mirror（逐 node→zone）
 *   - 排行：GET /business/economy/leaderboard（余额数值倒序 Top-N，旁路 mce 无排行 API）
 *   - 流水：GET /business/events?domain=economy（通用业务事件流，前端解析经济 envelope）
 *
 * 转账/加扣等**写动作**复用 @/api/business.ts 的 dispatchBusiness（POST /instances/:id/business），不在本模块。
 */

/** 经济镜像一行：某 (node, zone, player, currency) 的最新余额（与后端 model.EconomyBalanceMirror 对应）。 */
// 经济域契约已回迁应用侧（受控视图与业务页面共用，ADR-097）；此处原样再导出，调用点无需改动。
// 本地绑定供本文件的 fetch 函数签名使用。
import type {
  BusinessEvent,
  EconomyEventsParams,
  EconomyLeaderboardParams,
  EconomyLeaderboardRow,
  EconomyMirrorParams,
  EconomyMirrorRow,
} from '@/lib/economy'
export type {
  BusinessEvent,
  EconomyEventsParams,
  EconomyLeaderboardParams,
  EconomyLeaderboardRow,
  EconomyMirrorParams,
  EconomyMirrorRow,
} from '@/lib/economy'

/** 查经济镜像最新余额（逐 node→zone 行，跨区同名玩家分行不混）。 */
export async function fetchEconomyMirror(params: EconomyMirrorParams): Promise<EconomyMirrorRow[]> {
  const { data } = await api.get<{ balances: EconomyMirrorRow[] }>('/business/economy/mirror', { params })
  return data.balances ?? []
}

/** 取某货币余额倒序的 Top-N（旁路排行，从 JM 镜像表派生）。 */
export async function fetchEconomyLeaderboard(
  params: EconomyLeaderboardParams,
): Promise<EconomyLeaderboardRow[]> {
  const { data } = await api.get<{ currency: string; rows: EconomyLeaderboardRow[] }>(
    '/business/economy/leaderboard',
    { params },
  )
  return data.rows ?? []
}

/** 取最近经济业务事件（domain=economy 的通用 envelope 流，供流水视图解析）。 */
export async function fetchEconomyEvents(params: EconomyEventsParams = {}): Promise<BusinessEvent[]> {
  const { data } = await api.get<{ events: BusinessEvent[] }>('/business/events', {
    params: { domain: 'economy', ...params },
  })
  return data.events ?? []
}
