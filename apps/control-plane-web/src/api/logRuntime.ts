import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

export interface LogRuntimeInstance {
  namespace: 'hot' | 'cold' | 'rehydrate'
  state: string
  port: number
  listen_addr: string
  asset_tag: string
  asset_sha256: string
  last_error?: string
  health_ok: boolean
  partition_recovery_complete: boolean
  query_ready: boolean
}

export interface LogRuntimeStatus {
  supported: boolean
  instances: LogRuntimeInstance[]
  error?: { code: number; message: string }
}

export interface ApprovedLogAsset {
  tag: string
  os: string
  arch: string
  cached: boolean
  packageSha256: string
  executableSha256: string
}

export function useLogRuntime(nodeId: number, active: boolean) {
  return useQuery({
    queryKey: ['log-runtime', nodeId],
    queryFn: async () => (await api.get<LogRuntimeStatus>(`/nodes/${nodeId}/log-runtime`)).data,
    enabled: active && nodeId > 0,
    refetchInterval: active ? 10_000 : false,
  })
}

export function useApprovedLogAssets(active: boolean) {
  return useQuery({
    queryKey: ['log-vl-assets'],
    queryFn: async () => (await api.get<{ assets: ApprovedLogAsset[] }>('/log-runtime/assets')).data.assets,
    enabled: active,
  })
}

export function useUploadLogAsset() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ os, arch, file }: { os: string; arch: string; file: File }) => {
      const form = new FormData()
      form.append('package', file)
      return (await api.post(`/log-runtime/assets/${os}/${arch}`, form, { timeout: 180_000 })).data
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ['log-vl-assets'] }),
  })
}

export function useInstallLogAsset(nodeId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async () => (await api.post(`/nodes/${nodeId}/log-runtime/install`, undefined, { timeout: 180_000 })).data,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['log-runtime', nodeId] })
      qc.invalidateQueries({ queryKey: ['log-vl-assets'] })
    },
  })
}

export function useControlLogRuntime(nodeId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ namespace, action }: {
      namespace: LogRuntimeInstance['namespace']
      action: 'start' | 'stop' | 'restart'
    }) => (await api.post(`/nodes/${nodeId}/log-runtime/${namespace}/${action}`)).data,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['log-runtime', nodeId] }),
  })
}

export function useMigrateLogPartition(nodeId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ storageNamespace, utcDay }: { storageNamespace: string; utcDay: string }) =>
      (await api.post(`/nodes/${nodeId}/log-runtime/migrate`, { storageNamespace, utcDay }, { timeout: 180_000 })).data,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['log-runtime', nodeId] }),
  })
}

export function useResolveLogIngestGaps(nodeId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async () => (await api.post(`/nodes/${nodeId}/log-runtime/ingest/resolve-gaps`)).data,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['log-runtime', nodeId] }),
  })
}

/** 身份冲突人工清空的结果（CP 响应体）。 */
export interface ClearIdentityConflictsResult {
  state: string
  clearedCount: number
  clearedSources: string[]
  skippedSources: string[]
}

/**
 * 人工确认并清空「身份冲突」记录（FR 日志质量标记运维入口）。
 *
 * 语义：运维审阅后确认这些冲突已被正确处理（系统在不可裁定形态下保留先到者 = 保留正确版本，
 * 冲突行从未进入写入面），同意清空诊断记录以消除日志页的质量标记。**只移除诊断记录，不改日志内容。**
 *
 * 两个入参都是**审计必需**（后端缺任一即拒）：
 * - `storageNamespace` 省略/空 = 清该节点全部持有冲突记录的源（仅平台管理员，后端强制）；
 * - `reason` 必填 —— 它进审计凭据（谁/何时/为何/清了多少条），故不允许静默清空。
 */
export function useClearLogIdentityConflicts(nodeId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({
      storageNamespace,
      reason,
    }: {
      storageNamespace?: string
      reason: string
    }): Promise<ClearIdentityConflictsResult> =>
      (
        await api.post(
          `/nodes/${nodeId}/log-runtime/ingest/clear-identity-conflicts`,
          { storageNamespace: storageNamespace ?? '', reason },
          { timeout: 60_000 },
        )
      ).data,
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['log-runtime', nodeId] })
      // 质量标记派生自联邦查询结果，清空后必须让日志页重新取数才能看到标记消失。
      // queryKey 前缀与 useLogsFederation / useLogsFederationStats / useLogsFederationFacets
      // 保持一致（分别是 logsFederation / logsFederationStats / logsFederationFacets）。
      void qc.invalidateQueries({ queryKey: ['logsFederation'] })
      void qc.invalidateQueries({ queryKey: ['logsFederationStats'] })
      void qc.invalidateQueries({ queryKey: ['logsFederationFacets'] })
    },
  })
}
