/**
 * 经济域的契约（FR-128 / FR-129）。
 *
 * 归包理由：经济页与流水解析纯逻辑都要读它，应用侧 API 层改从包 import，避免两处各写一份。
 * 实现（三个 fetch 函数）仍留应用侧——它们要拿 axios 实例与鉴权。
 */

/** 余额镜像一行（逐 node→zone，跨区同名玩家分行不混）。 */
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
