import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/** 探针在线更新状态（FR-068/409）：连接状态 + 当前解析版本 + 上次推送时间。 */
export interface ProbeUpdateStatus {
  instanceId: number
  instanceUuid: string
  probeConnected: boolean
  versionId: number
  version: string
  versionSha256: string
  versionOrigin: 'global' | 'node' | 'instance' | ''
  versionError?: string
  /** 兼容旧服务端响应；新版 UI 不再使用内嵌制品字段。 */
  embeddedVersion: string
  embeddedFingerprint: string
  embeddedAvailable: boolean
  librariesAvailable: boolean
  librariesBytes: number
  librariesShortSha: string
  lastPushedAt: string | null
}

/** 探针推送结果（FR-068）。 */
export interface ProbeUpdateResult {
  instanceId: number
  deployed: boolean
  restarted: boolean
  probeConnected: boolean
  versionId: number
  version: string
  /** 兼容旧服务端响应；新版 UI 不再使用内嵌制品字段。 */
  embeddedVersion: string
  embeddedFingerprint: string
  librariesAvailable: boolean
  librariesBytes: number
  librariesShortSha: string
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

/** 推送当前解析的探针版本到实例（restart=true 推送并重启使其立即生效），FR-068/409。 */
export function useUpdateProbe(instanceId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (restart: boolean) =>
      api.post<ProbeUpdateResult>(`/instances/${instanceId}/probe/update`, { restart }).then((r) => r.data),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['probe-update', instanceId] }),
  })
}
