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
export interface EconomyMirrorRow {
  id: number
  nodeUuid: string
  zoneId: string
  playerName: string
  currency: string
  currencyId: number
  /** 最新余额（字符串承载 BigDecimal，禁浮点）。 */
  balance: string
  lastSeq: number
  lastLedgerId: number
  lastEntryType: string
  occurredAt: number
  updatedAt: string
}

/** 排行一行：某 (node, zone) 内某玩家某货币的余额 + 名次（与后端 service.EconomyLeaderboardRow 对应）。 */
export interface EconomyLeaderboardRow {
  rank: number
  playerName: string
  currency: string
  nodeUuid: string
  zoneId: string
  balance: string
}

/** 通用业务事件 envelope 一行（与后端 model.BusinessEvent 对应）；流水由经济域事件解析而来。 */
export interface BusinessEvent {
  id: number
  domain: string
  dedupKey: string
  action: string
  nodeUuid: string
  instanceUuid: string
  operator?: string
  /** 业务信封原始载荷 JSON（探针产物；经济流水从其 data 段解析）。 */
  payloadJson: string
  occurredAt: number
  createdAt: string
}

/** 余额镜像查询入参（任意组合，留空表示该维度不过滤）。 */
export interface EconomyMirrorParams {
  player?: string
  currency?: string
  node?: string
  zone?: string
  limit?: number
}

/** 排行查询入参；currency 必填（跨货币余额不可比）。 */
export interface EconomyLeaderboardParams {
  currency: string
  zone?: string
  node?: string
  limit?: number
}

/** 流水查询入参（经济事件流）。 */
export interface EconomyEventsParams {
  node?: string
  limit?: number
}

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
