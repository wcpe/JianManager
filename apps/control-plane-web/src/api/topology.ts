import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'
import type { Registration } from '@/api/registrations'

/** 拓扑聚合响应里的单个代理概要（对应后端 service.ProxyTopology，FR-335）。 */
export interface TopologyProxy {
  id: number
  name: string
  status: string
  serverPort: number
  nodeId: number
  /** 该代理已注册的后端（与 GET /proxies/:id/registrations 同构）。 */
  registrations: Registration[]
}

/** 拓扑分组概要：一个 network 的成员实例归属（软标签非独占，ADR-007）。 */
export interface TopologyNetwork {
  id: number
  name: string
  memberInstanceIds: number[]
}

/** 全量群组拓扑聚合响应（对应后端 GET /topology，消 per-proxy N+1，FR-335）。 */
export interface TopologyResponse {
  proxies: TopologyProxy[]
  networks: TopologyNetwork[]
}

/**
 * 全量群组拓扑（一次请求，FR-335）。
 * 替代拓扑图对每个 proxy 各发一条 GET /proxies/:id/registrations 的 per-proxy N+1；
 * queryKey ['topology'] 与注册/群组变更失效联动（见 api/registrations.ts 说明）。
 */
export function useTopology() {
  return useQuery({
    queryKey: ['topology'],
    queryFn: async () => {
      const { data } = await api.get<TopologyResponse>('/topology')
      return data
    },
  })
}
