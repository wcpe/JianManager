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
  behavior: string
  config: BotConfig
  createdAt: string
}

export interface CreateBotRequest {
  instanceId: number
  name: string
  config: BotConfig
  behavior: string
}

/** 获取 Bot 列表，可按实例 ID 过滤。 */
export function useBots(instanceId?: number) {
  return useQuery({
    queryKey: ['bots', instanceId],
    queryFn: async () => {
      const { data } = await api.get<BotInfo[]>('/bots', {
        params: instanceId ? { instanceId } : undefined,
      })
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
    mutationFn: (payload: CreateBotRequest) => api.post('/bots', payload),
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
