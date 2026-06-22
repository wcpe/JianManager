import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/** 探针在线更新状态（FR-068）：连接状态 + 内嵌最新版本 + 上次推送时间。 */
export interface ProbeUpdateStatus {
  instanceId: number
  instanceUuid: string
  probeConnected: boolean
  embeddedVersion: string
  embeddedFingerprint: string
  embeddedAvailable: boolean
  lastPushedAt: string | null
}

/** 探针推送结果（FR-068）。 */
export interface ProbeUpdateResult {
  instanceId: number
  deployed: boolean
  restarted: boolean
  probeConnected: boolean
  embeddedVersion: string
  embeddedFingerprint: string
  message: string
}

/** 查询某实例探针在线更新状态（连接/内嵌版本/上次推送），FR-068。 */
export function useProbeUpdateStatus(instanceId: number) {
  return useQuery({
    queryKey: ['probe-update', instanceId],
    queryFn: () => api.get<ProbeUpdateStatus>(`/instances/${instanceId}/probe/update`).then((r) => r.data),
    enabled: instanceId > 0,
    refetchInterval: 15000,
  })
}

/** 推送内嵌探针 jar 到实例（restart=true 推送并重启使其立即生效），FR-068。 */
export function useUpdateProbe(instanceId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (restart: boolean) =>
      api.post<ProbeUpdateResult>(`/instances/${instanceId}/probe/update`, { restart }).then((r) => r.data),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['probe-update', instanceId] }),
  })
}
