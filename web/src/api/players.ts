import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/** 在线玩家及其所在子服（BC 跨服感知，FR-054）。 */
export interface OnlinePlayer {
  name: string
  instanceId: number
  instanceName: string
}

/** 单个后端子服的 RCON 可用性（优雅降级提示）。 */
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

/** 踢/封/解封作用域（互斥，按 instanceId > networkId > 全部 解析）。 */
export interface PlayerActionScope {
  instanceId?: number
  networkId?: number
  reason?: string
}

/** 在线玩家列表（聚合可达后端 RCON，标注所在子服）。每 10s 刷新。 */
export function useOnlinePlayers() {
  return useQuery({
    queryKey: ['players', 'online'],
    queryFn: async () => {
      const { data } = await api.get<OnlinePlayersResult>('/players')
      return data
    },
    refetchInterval: 10000,
  })
}

/** 踢出玩家。 */
export function useKickPlayer() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ name, scope }: { name: string; scope?: PlayerActionScope }) =>
      api.post<PlayerActionResult>(`/players/${encodeURIComponent(name)}/kick`, scope ?? {}).then((r) => r.data),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['players', 'online'] }),
  })
}

/** 封禁玩家。 */
export function useBanPlayer() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ name, scope }: { name: string; scope?: PlayerActionScope }) =>
      api.post<PlayerActionResult>(`/players/${encodeURIComponent(name)}/ban`, scope ?? {}).then((r) => r.data),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['players', 'online'] })
      qc.invalidateQueries({ queryKey: ['bans'] })
    },
  })
}

/** 解封玩家。 */
export function useUnbanPlayer() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ name, scope }: { name: string; scope?: PlayerActionScope }) =>
      api.post<PlayerActionResult>(`/players/${encodeURIComponent(name)}/unban`, scope ?? {}).then((r) => r.data),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['bans'] }),
  })
}

/** 封禁记录列表。 */
export function useBans(params?: { player?: string; active?: boolean }) {
  return useQuery({
    queryKey: ['bans', params],
    queryFn: async () => {
      const { data } = await api.get<BanRecord[]>('/bans', {
        params: { player: params?.player || undefined, active: params?.active ? 'true' : undefined },
      })
      return data
    },
  })
}

/** 单后端白名单查询。 */
export function useWhitelist(instanceId: number | null) {
  return useQuery({
    queryKey: ['whitelist', instanceId],
    queryFn: async () => {
      const { data } = await api.get<WhitelistResult>(`/instances/${instanceId}/whitelist`)
      return data
    },
    enabled: instanceId !== null,
  })
}

/** 单后端白名单增删。 */
export function useWhitelistAction(instanceId: number | null) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ action, player }: { action: 'add' | 'remove'; player: string }) =>
      api.post(`/instances/${instanceId}/whitelist`, { action, player }).then((r) => r.data),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['whitelist', instanceId] }),
  })
}
