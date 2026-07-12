import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/** proxy↔backend 注册关系（对应后端 model.ServerRegistration + backend 概要，FR-032/035）。 */
export interface Registration {
  id: number
  proxyId: number
  backendId: number
  alias: string
  priority: number
  forcedHost: string
  restricted: boolean
  enabled: boolean
  backend?: {
    id: number
    name: string
    role: string
    nodeId: number
    serverPort: number
    status: string
  }
}

/** 创建注册请求体。 */
export interface CreateRegistrationBody {
  backendId: number
  alias?: string
  priority?: number
  forcedHost?: string
  restricted?: boolean
  enabled?: boolean
}

/** 某代理已注册的后端列表。 */
export function useRegistrations(proxyId: number) {
  return useQuery({
    queryKey: ['registrations', proxyId],
    queryFn: async () => {
      const { data } = await api.get<Registration[]>(`/proxies/${proxyId}/registrations`)
      return data
    },
    enabled: !!proxyId,
  })
}

/** 将后端注册进代理。 */
export function useCreateRegistration(proxyId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: CreateRegistrationBody) =>
      api.post(`/proxies/${proxyId}/registrations`, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['registrations', proxyId] }),
  })
}

/** 更新注册（alias/priority/forcedHost/restricted/enabled）。 */
export function useUpdateRegistration(proxyId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ rid, body }: { rid: number; body: Partial<Omit<CreateRegistrationBody, 'backendId'>> }) =>
      api.patch(`/proxies/${proxyId}/registrations/${rid}`, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['registrations', proxyId] }),
  })
}

/** 取消注册。 */
export function useDeleteRegistration(proxyId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (rid: number) => api.delete(`/proxies/${proxyId}/registrations/${rid}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['registrations', proxyId] }),
  })
}
