import { useEffect, useRef, useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api, { ensureFreshToken } from '@/api/client'
import { useAuthStore } from '@/stores/auth'

/** 在线玩家及其所在子服（BC 跨服感知，FR-054）。 */
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

/** 踢/封/解封作用域（互斥，按 instanceId > networkId > 全部 解析）。 */
export interface PlayerActionScope {
  instanceId?: number
  networkId?: number
  reason?: string
}

/** 在线玩家列表（聚合可达后端探针，标注所在子服，FR-067）。每 10s 刷新。 */
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

/** 探针经反向 WS 实时上报的玩家事件类型（FR-066）。 */
export type PlayerEventType =
  | 'connected'
  | 'disconnected'
  | 'heartbeat'
  | 'player_join'
  | 'player_quit'
  | 'chat'
  | 'cross_server'

/** 一条实时玩家事件（SSE `event: player`）。 */
export interface PlayerEvent {
  instanceUuid: string
  instanceId: number
  instanceName: string
  type: PlayerEventType
  timestamp: number
  playerName?: string
  playerUuid?: string
  message?: string
  server?: string
  fromServer?: string
  toServer?: string
  platform?: string
}

/** 实时在线名册中的一名玩家及其所在子服。 */
export interface RosterPlayer {
  name: string
  server: string
}

/** usePlayerEvents 返回的实时状态。 */
export interface PlayerEventsState {
  /** 探针是否在位连接（false 时在线列表降级、提示未连入）。 */
  connected: boolean
  /** 实时在线名册（探针在位时由事件流维护）。 */
  roster: RosterPlayer[]
  /** 最近事件流（倒序，最多保留 MAX_EVENTS 条），供事件面板展示。 */
  events: PlayerEvent[]
}

/** 事件面板保留的最近事件条数上限。 */
const MAX_EVENTS = 100

/**
 * 订阅某实例的实时玩家事件 SSE（FR-066）：维护在线名册 + 最近事件流。
 *
 * 经 `/instances/:id/players/events` 连接（fetch + ReadableStream，复用 events.ts 的 SSE 解析形态，
 * 因 EventSource 不支持自定义鉴权头）。首帧 `event: init` 携带探针连接状态 + 名册快照；
 * 之后 `event: player` 增量推送。仅订阅当前实例，组件卸载/换实例即断开。
 *
 * @param instanceId 实例 DB ID；为 null 时不连接。
 */
export function usePlayerEvents(instanceId: number | null): PlayerEventsState {
  const [state, setState] = useState<PlayerEventsState>({ connected: false, roster: [], events: [] })
  // 名册以 Map 维护便于增删改，扇出到 state 时再转数组。
  const rosterRef = useRef<Map<string, string>>(new Map())

  useEffect(() => {
    // 切换实例/卸载时清空名册引用（ref 同步赋值不触发渲染，安全）。
    rosterRef.current = new Map()
    if (instanceId === null) {
      // 异步重置，避免在 effect 体内同步 setState（react-hooks/set-state-in-effect）。
      queueMicrotask(() => setState({ connected: false, roster: [], events: [] }))
      return
    }
    if (!useAuthStore.getState().accessToken) return

    // 切换实例时清空上一实例的展示（异步，避免 effect 同步体内 setState）；
    // 连上后服务端首帧 init 会立即回填最新名册与连接状态。
    queueMicrotask(() => setState({ connected: false, roster: [], events: [] }))

    const base = api.defaults.baseURL || ''
    const url = `${base}/instances/${instanceId}/players/events`
    const controller = new AbortController()

    const emitRoster = () => {
      const roster = Array.from(rosterRef.current.entries())
        .map(([name, server]) => ({ name, server }))
        .sort((a, b) => a.name.localeCompare(b.name))
      setState((s) => ({ ...s, roster }))
    }

    const applyEvent = (evt: PlayerEvent) => {
      const m = rosterRef.current
      switch (evt.type) {
        case 'connected':
          m.clear()
          setState((s) => ({ ...s, connected: true }))
          break
        case 'disconnected':
          m.clear()
          setState((s) => ({ ...s, connected: false }))
          break
        case 'player_join':
          if (evt.playerName) m.set(evt.playerName, evt.server || '')
          break
        case 'player_quit':
          if (evt.playerName) m.delete(evt.playerName)
          break
        case 'cross_server':
          if (evt.playerName) m.set(evt.playerName, evt.toServer || evt.server || '')
          break
      }
      emitRoster()
      // 仅把有意义的玩家事件入事件面板（心跳不入）。
      if (evt.type !== 'heartbeat') {
        setState((s) => ({ ...s, events: [evt, ...s.events].slice(0, MAX_EVENTS) }))
      }
    }

    async function connect() {
      try {
        const token = await ensureFreshToken()
        if (!token || controller.signal.aborted) return
        const resp = await fetch(url, {
          headers: { Authorization: `Bearer ${token}` },
          signal: controller.signal,
        })
        if (!resp.ok || !resp.body) return

        const reader = resp.body.getReader()
        const decoder = new TextDecoder()
        let buffer = ''
        let currentEvent = ''

        while (true) {
          const { done, value } = await reader.read()
          if (done) break
          buffer += decoder.decode(value, { stream: true })
          const lines = buffer.split('\n')
          buffer = lines.pop() || ''

          for (const line of lines) {
            if (line.startsWith('event: ')) {
              currentEvent = line.slice(7).trim()
              continue
            }
            if (line.startsWith('data: ')) {
              const payload = line.slice(6)
              try {
                if (currentEvent === 'init') {
                  const init = JSON.parse(payload) as { connected: boolean; players: RosterPlayer[] }
                  rosterRef.current = new Map((init.players || []).map((p) => [p.name, p.server]))
                  setState({
                    connected: init.connected,
                    roster: (init.players || []).slice().sort((a, b) => a.name.localeCompare(b.name)),
                    events: [],
                  })
                } else if (currentEvent === 'player') {
                  applyEvent(JSON.parse(payload) as PlayerEvent)
                }
              } catch {
                // 忽略解析错误
              }
            }
          }
        }
      } catch {
        if (controller.signal.aborted) return
        setTimeout(connect, 5000)
      }
    }

    connect()
    return () => controller.abort()
  }, [instanceId])

  return state
}
