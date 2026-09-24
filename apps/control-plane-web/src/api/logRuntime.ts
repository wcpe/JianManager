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
