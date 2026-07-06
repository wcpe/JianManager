import { useEffect, useRef, useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api, { ensureFreshToken } from '@/api/client'
import { useAuthStore } from '@/stores/auth'

export interface BotConfig {
  server: string
  port: number
  auth: string
}

export interface BotInfo {
  id: number
  uuid: string
  instanceId: number
  name: string
  status: string
  /** Bot 连接配置，后端以 JSON 字符串存储。 */
  config: string
  behavior: string
  workerId: string
  createdAt: string
  updatedAt: string
}

/** Bot 实时事件（SSE event: bot）。 */
export interface BotRealtimeEvent {
  botId: number
  botUuid: string
  type: string
  data: Record<string, unknown>
  timestamp: number
}

/** 单 Bot 实时状态。 */
export interface BotRealtimeState {
  status?: string
  health?: number
  food?: number
  behavior?: string
  position?: { x: number; y: number; z: number }
  events: BotRealtimeEvent[]
  connected: boolean
}

export interface BotStressSessionCounts {
  total: number
  byStatus: Record<string, number>
}

export interface BotStressOrchestrationSummary {
  enabled: boolean
  loop: boolean
  staggerMs: number
  phaseCount: number
  durationSec: number
  behaviors: string[]
}

export interface BotStressSession {
  id: number
  uuid: string
  instanceId: number
  count: number
  behavior: string
  namePrefix: string
  config?: BotConfig
  orchestrationYaml?: string
  orchestrationSummary?: BotStressOrchestrationSummary
  status: string
  startedAt?: string | null
  stoppedAt?: string | null
  createdAt: string
  updatedAt: string
  counts: BotStressSessionCounts
}

export interface BotStressSessionListResponse {
  items: BotStressSession[]
  total: number
  page: number
  pageSize: number
}

export interface CreateBotStressSessionRequest {
  instanceId: number
  count: number
  behavior?: string
  namePrefix: string
  config?: BotConfig
  orchestrationYaml?: string
}

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

/** 获取 Bot 分页列表，支持多维筛选（FR-038）。 */
export function useBots(params?: BotListParams) {
  return useQuery({
    queryKey: ['bots', params],
    queryFn: async () => {
      const { data } = await api.get<BotListResponse>('/bots', { params })
      return data
    },
  })
}

/** 获取 Bot 计数聚合，可按 instance/node/status/behavior 分组（FR-038）。 */
export function useBotSummary(params?: BotListParams & { groupBy?: string }) {
  return useQuery({
    queryKey: ['bots', 'summary', params],
    queryFn: async () => {
      const { data } = await api.get<BotSummary>('/bots/summary', { params })
      return data
    },
  })
}

/** 获取单个 Bot 详情。 */
export function useBot(id: number) {
  return useQuery({
    queryKey: ['bots', id],
    queryFn: async () => {
      const { data } = await api.get<BotInfo>(`/bots/${id}`)
      return data
    },
    enabled: !!id,
  })
}

/** 创建 Bot。 */
export function useCreateBot() {
  const qc = useQueryClient()
  return useMutation({
    // 后端 Bot.config 以 JSON 字符串存储（CreateBotRequest.Config string），
    // 表单的 config 是对象，必须序列化后再提交，否则 Gin 绑定失败返回 400。
    mutationFn: (payload: CreateBotRequest) =>
      api.post('/bots', { ...payload, config: JSON.stringify(payload.config) }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['bots'] }),
  })
}

/** 删除 Bot。 */
export function useDeleteBot() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.delete(`/bots/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['bots'] }),
  })
}

/** 切换 Bot 行为模式。 */
export function useSetBotBehavior() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, behavior, target }: { id: number; behavior: string; target?: string }) =>
      api.post(`/bots/${id}/behavior`, { behavior, target }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['bots'] }),
  })
}

/** 批量执行 set-behavior/start/stop/delete（FR-038）。 */
export function useBotBatch() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (payload: BotBatchRequest) => {
      const { data } = await api.post<BotBatchResult>('/bots/batch', payload)
      return data
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ['bots'] }),
  })
}

/** 查询 Bot 压测会话列表。 */
export function useBotStressSessions(params?: { page?: number; pageSize?: number }) {
  return useQuery({
    queryKey: ['bots', 'stress-sessions', params],
    queryFn: async () => {
      const { data } = await api.get<BotStressSessionListResponse>('/bots/stress-sessions', { params })
      return data
    },
  })
}

/** 查询单个 Bot 压测会话详情。 */
export function useBotStressSession(id: number | null) {
  return useQuery({
    queryKey: ['bots', 'stress-sessions', id],
    queryFn: async () => {
      const { data } = await api.get<BotStressSession>(`/bots/stress-sessions/${id}`)
      return data
    },
    enabled: id !== null,
  })
}

/** 创建 Bot 压测会话。 */
export function useCreateBotStressSession() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (payload: CreateBotStressSessionRequest) => {
      const { data } = await api.post<BotStressSession>('/bots/stress-sessions', payload)
      return data
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ['bots', 'stress-sessions'] }),
  })
}

/** 启动 Bot 压测会话。 */
export function useStartBotStressSession() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (id: number) => {
      const { data } = await api.post<BotStressSession>(`/bots/stress-sessions/${id}/start`)
      return data
    },
    onSuccess: (_data, id) => {
      qc.invalidateQueries({ queryKey: ['bots'] })
      qc.invalidateQueries({ queryKey: ['bots', 'stress-sessions'] })
      qc.invalidateQueries({ queryKey: ['bots', 'stress-sessions', id] })
    },
  })
}

/** 停止 Bot 压测会话。 */
export function useStopBotStressSession() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (id: number) => {
      const { data } = await api.post<BotStressSession>(`/bots/stress-sessions/${id}/stop`)
      return data
    },
    onSuccess: (_data, id) => {
      qc.invalidateQueries({ queryKey: ['bots'] })
      qc.invalidateQueries({ queryKey: ['bots', 'stress-sessions'] })
      qc.invalidateQueries({ queryKey: ['bots', 'stress-sessions', id] })
    },
  })
}

/** 向单个 Bot 发送聊天/控制命令。 */
export function useSendBotCommand() {
  return useMutation({
    mutationFn: ({ id, command }: { id: number; command: string }) =>
      api.post(`/bots/${id}/command`, { command }),
  })
}

/** 订阅单个 Bot 的实时事件流（FR-041）。 */
export function useBotEvents(botId: number | null): BotRealtimeState {
  const [state, setState] = useState<BotRealtimeState>({ connected: false, events: [] })
  const mounted = useRef(true)

  useEffect(() => {
    mounted.current = true
    if (!botId || !useAuthStore.getState().accessToken) {
      queueMicrotask(() => setState({ connected: false, events: [] }))
      return
    }

    queueMicrotask(() => setState({ connected: false, events: [] }))
    const controller = new AbortController()
    const base = api.defaults.baseURL || ''
    const url = `${base}/bots/${botId}/events`

    const applyEvent = (evt: BotRealtimeEvent) => {
      setState((cur) => {
        const next: BotRealtimeState = {
          ...cur,
          connected: true,
          events: evt.type === 'state' ? cur.events : [evt, ...cur.events].slice(0, 100),
        }
        if (evt.type === 'state') {
          const data = evt.data
          next.status = typeof data.status === 'string' ? data.status : next.status
          next.behavior = typeof data.behavior === 'string' ? data.behavior : next.behavior
          next.health = typeof data.health === 'number' ? data.health : next.health
          next.food = typeof data.food === 'number' ? data.food : next.food
          if (data.position && typeof data.position === 'object') {
            next.position = data.position as BotRealtimeState['position']
          }
        }
        return next
      })
    }

    async function connect() {
      try {
        const token = await ensureFreshToken()
        if (!token || controller.signal.aborted) return
        const resp = await fetch(url, {
          headers: { Authorization: `Bearer ${token}` },
          signal: controller.signal,
        })
        if (!resp.ok || !resp.body) {
          scheduleReconnect()
          return
        }

        const reader = resp.body.getReader()
        const decoder = new TextDecoder()
        let buffer = ''
        let currentEvent = ''

        while (true) {
          const { done, value } = await reader.read()
          if (done) {
            scheduleReconnect()
            break
          }
          buffer += decoder.decode(value, { stream: true })
          const lines = buffer.split('\n')
          buffer = lines.pop() || ''

          for (const line of lines) {
            if (line.startsWith('event: ')) {
              currentEvent = line.slice(7).trim()
              continue
            }
            if (line.startsWith('data: ') && currentEvent === 'bot') {
              try {
                applyEvent(JSON.parse(line.slice(6)) as BotRealtimeEvent)
              } catch {
                // 忽略坏帧，保持事件流不断。
              }
            }
          }
        }
      } catch {
        scheduleReconnect()
      }
    }

    function scheduleReconnect() {
      if (controller.signal.aborted || !mounted.current) return
      setState((cur) => ({ ...cur, connected: false }))
      window.setTimeout(connect, 5000)
    }

    connect()
    return () => {
      mounted.current = false
      controller.abort()
    }
  }, [botId])

  return state
}
