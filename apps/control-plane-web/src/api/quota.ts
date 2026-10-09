import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'
import { INSTANCE_QUERY_GC_TIME_MS } from '@/api/instances'

/**
 * 实例运行期配额契约（FR-467）已回迁应用侧，双侧共用（ADR-097）；
 * 对外保留原导出名 `InstanceQuotaStatus`，调用点无需改动。
 */
export type { InstanceQuotaStatus } from '@/lib/instances/quota-status'
import type { InstanceQuotaStatus } from '@/lib/instances/quota-status'

export function useInstanceQuota(instanceId: number, enabled = true) {
  return useQuery({
    queryKey: ['instance-quota', instanceId],
    queryFn: () => api.get<InstanceQuotaStatus>(`/instances/${instanceId}/quota`).then((r) => r.data),
    enabled: enabled && instanceId > 0,
    gcTime: INSTANCE_QUERY_GC_TIME_MS,
  })
}
