import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

// 本地绑定仍叫 `Registration`（本文件内使用），对外导出名不变。
import type { ProxyRegistration as Registration } from '@/lib/instances/proxy-registration'

/**
 * proxy↔backend 注册关系契约（对应后端 model.ServerRegistration + backend 概要，FR-032/035）
 * 已回迁应用侧，双侧共用（ADR-097）；对外保留原导出名 `Registration`，调用点无需改动。
 */
export type { ProxyRegistration as Registration } from '@/lib/instances/proxy-registration'

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
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['registrations', proxyId] })
      // 拓扑聚合视图（FR-335）随注册变更失效。
      qc.invalidateQueries({ queryKey: ['topology'] })
    },
  })
}

/** 更新注册（alias/priority/forcedHost/restricted/enabled）。 */
export function useUpdateRegistration(proxyId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ rid, body }: { rid: number; body: Partial<Omit<CreateRegistrationBody, 'backendId'>> }) =>
      api.patch(`/proxies/${proxyId}/registrations/${rid}`, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['registrations', proxyId] })
      qc.invalidateQueries({ queryKey: ['topology'] })
    },
  })
}

/** 取消注册。 */
export function useDeleteRegistration(proxyId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (rid: number) => api.delete(`/proxies/${proxyId}/registrations/${rid}`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['registrations', proxyId] })
      qc.invalidateQueries({ queryKey: ['topology'] })
    },
  })
}
