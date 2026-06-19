import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

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
