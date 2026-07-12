import { useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'
import type { InstanceInfo } from '@/api/instances'

/** 搭建代理请求体（对应后端 service.ProvisionProxyRequest，FR-035）。 */
export interface ProvisionProxyBody {
  nodeId: number
  name: string
  proxyType: string // velocity | waterfall | bungeecord
  version?: string
  jdkId?: number
  memoryMb?: number
  jvmArgs?: string[]
  groupId?: number
  /** 代理是否向 Mojang 校验正版（缺省 true=正版网络；离线模式群组服传 false）。 */
  onlineMode?: boolean
}

/** 搭建代理结果。 */
export interface ProvisionProxyResult {
  instance: InstanceInfo
  forwardingSecret?: string
  registrations: unknown[]
  warnings?: string[]
}

/** 搭建代理实例（role=proxy）。 */
export function useProvisionProxy() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: ProvisionProxyBody) =>
      api.post<ProvisionProxyResult>('/instances/provision/proxy', body).then((r) => r.data),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['instances'] }),
  })
}

/** 重新把注册关系与 secret 推到代理配置与各后端。 */
export function useResyncProxy() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (proxyId: number) =>
      api
        .post<{ synced: boolean; secretConsistent: boolean; warnings?: string[] }>(`/proxies/${proxyId}/resync`)
        .then((r) => r.data),
    onSuccess: (_data, proxyId) => qc.invalidateQueries({ queryKey: ['registrations', proxyId] }),
  })
}
