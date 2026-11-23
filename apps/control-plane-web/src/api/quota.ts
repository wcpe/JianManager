import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'
import { INSTANCE_QUERY_GC_TIME_MS } from '@/api/instances'

/** 
 * 实例运行期配额契约已回迁应用侧，双侧共用；
 * 对外保留原导出名 `InstanceQuotaStatus`，调用点无需改动。
 */
export type { InstanceQuotaStatus } from '@/lib/instances/quota-status'
import type { InstanceQuotaStatus } from '@/lib/instances/quota-status'

/*
 * 配额面板展示的是**运行期实时用量**（CPU / RSS / 磁盘）与 enforced 状态，
 * 后端按采样周期更新。不轮询时，实例跑着而面板一直停在首次查询的旧读数——
 * 用户会以为配额没生效。10s 与后台采样节奏同量级，不额外加压。
 * 隐藏页自动暂停（TanStack 默认 refetchIntervalInBackground=false）。
 */
const QUOTA_REFETCH_MS = 10_000

export function useInstanceQuota(instanceId: number, enabled = true) {
  return useQuery({
    queryKey: ['instance-quota', instanceId],
    queryFn: () => api.get<InstanceQuotaStatus>(`/instances/${instanceId}/quota`).then((r) => r.data),
    enabled: enabled && instanceId > 0,
    gcTime: INSTANCE_QUERY_GC_TIME_MS,
    refetchInterval: QUOTA_REFETCH_MS,
    refetchIntervalInBackground: false,
  })
}
