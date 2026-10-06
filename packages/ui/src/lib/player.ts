/**
 * 玩家治理的核心契约（FR-054 / FR-067）。
 *
 * 归包理由：实例玩家分区与玩家独立页都要读它，应用侧 API 层改从包 import，
 * 避免两处各写一份。
 */

/** 一名在线玩家及其所在子服。 */
export interface OnlinePlayer {
  name: string
  instanceId: number
  instanceName: string
}

/** 单个后端子服的探针可用性（优雅降级提示，FR-067）。 */
export interface BackendStatus {
  instanceId: number
  instanceName: string
  available: boolean
  error?: string
}

/** 在线玩家聚合结果。 */
export interface OnlinePlayersResult {
  players: OnlinePlayer[]
  backends: BackendStatus[]
}

/** 踢/封/解封在多后端的执行汇总。 */
export interface PlayerActionResult {
  player: string
  action: string
  total: number
  succeeded: number
  failed: number
  results: { instanceId: number; instanceName: string; ok: boolean; output?: string; error?: string }[]
}

/** 封禁记录（FR-054）。 */
export interface BanRecord {
  id: number
  uuid: string
  playerName: string
  reason: string
  scope: 'network' | 'instance' | 'global'
  scopeId: number
  operatorId: number
  active: boolean
  createdAt: string
  unbannedAt?: string | null
  operator?: { id: number; username: string }
}

/** 单后端白名单查询结果。 */
export interface WhitelistResult {
  instanceId: number
  available: boolean
  players: string[]
  error?: string
}
