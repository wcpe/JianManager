import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'

/** 节点 Docker 可用性检测结果（FR-237）。 */
export interface DockerCheckResult {
  available: boolean
  version?: string
  error?: string
}

/**
 * 探测目标节点本机 Docker 守护进程可用性（FR-237）。
 *
 * 仅在 `enabled`（创建向导选 docker 模式且已选节点）时触发；不重试、30s 缓存。
 * Docker 不可用不是错误（后端返 200 + available=false），故 data 总有值、用 available 判断。
 */
export function useNodeDockerCheck(nodeId: number, enabled: boolean) {
  return useQuery({
    queryKey: ['node-docker-check', nodeId],
    queryFn: async () => {
      const { data } = await api.post<DockerCheckResult>(`/nodes/${nodeId}/docker/check`)
      return data
    },
    enabled: enabled && nodeId > 0,
    staleTime: 30_000,
    retry: false,
  })
}
