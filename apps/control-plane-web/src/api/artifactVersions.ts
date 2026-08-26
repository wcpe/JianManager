import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

export interface ArtifactPackage {
  id: number
  key: string
  name: string
  assetType: string
  defaultVersionId: number
}

export interface ArtifactSource {
  id: number
  packageId: number
  provider: string
  name: string
  enabled: boolean
  lastSyncedAt: string | null
  lastError: string
}

export interface ArtifactVersion {
  id: number
  packageId: number
  sourceId: number
  version: string
  releaseRef: string
  assetName: string
  expectedSha256: string
  assetId: number
  cachedAt: string | null
  lastError: string
}

export interface ServerProbeCatalog {
  package: ArtifactPackage
  sources: ArtifactSource[]
  versions: ArtifactVersion[]
}

export interface SelectableProbeVersions {
  package: ArtifactPackage
  versions: ArtifactVersion[]
}

export interface ProbeVersionSelection {
  instanceId: number
  versionId: number
  resolvedVersion: ArtifactVersion
  origin: 'global' | 'node' | 'instance'
}

export interface NodeProbeVersion {
  nodeId: number
  versionId: number
}

export function useServerProbeCatalog(options?: { enabled?: boolean }) {
  return useQuery({
    queryKey: ['artifact-package', 'serverprobe'],
    queryFn: () => api.get<ServerProbeCatalog>('/artifact-packages/serverprobe').then((r) => r.data),
    enabled: options?.enabled ?? true,
  })
}

export function useSelectableProbeVersions(options?: { enabled?: boolean }) {
  return useQuery({
    queryKey: ['probe-versions'],
    queryFn: () => api.get<SelectableProbeVersions>('/probe-versions').then((r) => r.data),
    enabled: options?.enabled ?? true,
  })
}

function invalidateProbeVersions(qc: ReturnType<typeof useQueryClient>) {
  return () => {
    qc.invalidateQueries({ queryKey: ['artifact-package', 'serverprobe'] })
    qc.invalidateQueries({ queryKey: ['probe-versions'] })
  }
}

export function useSyncServerProbeSource() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (sourceId: number) => api.post<{ created: number }>(`/artifact-packages/serverprobe/sources/${sourceId}/sync`).then((r) => r.data),
    onSuccess: invalidateProbeVersions(qc),
  })
}

/** 管理员从本地上传 ServerProbe jar，上传完成后即作为已缓存版本可供选择。 */
export function useUploadServerProbeVersion() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ version, file }: { version: string; file: File }) => {
      const form = new FormData()
      form.append('version', version)
      form.append('file', file)
      return api.post<ArtifactVersion>('/artifact-packages/serverprobe/versions/upload', form, {
        headers: { 'Content-Type': 'multipart/form-data' },
      }).then((r) => r.data)
    },
    onSuccess: invalidateProbeVersions(qc),
  })
}

export function useCacheServerProbeVersion() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (versionId: number) => api.post<ArtifactVersion>(`/artifact-packages/serverprobe/versions/${versionId}/cache`).then((r) => r.data),
    onSuccess: invalidateProbeVersions(qc),
  })
}

export function useSetGlobalProbeVersion() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (versionId: number) => api.put<{ defaultVersionId: number }>('/artifact-packages/serverprobe/default-version', { versionId }).then((r) => r.data),
    onSuccess: invalidateProbeVersions(qc),
  })
}

export function useDeleteProbeVersion() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (versionId: number) => api.delete(`/artifact-packages/serverprobe/versions/${versionId}`),
    onSuccess: invalidateProbeVersions(qc),
  })
}

export function useNodeProbeVersion(nodeId: number, options?: { enabled?: boolean }) {
  return useQuery({
    queryKey: ['node-probe-version', nodeId],
    queryFn: () => api.get<NodeProbeVersion>(`/nodes/${nodeId}/probe-version`).then((r) => r.data),
    enabled: (options?.enabled ?? true) && nodeId > 0,
  })
}

export function useSetNodeProbeVersion(nodeId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (versionId: number) => api.put<NodeProbeVersion>(`/nodes/${nodeId}/probe-version`, { versionId }).then((r) => r.data),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['node-probe-version', nodeId] }),
  })
}

export function useInstanceProbeVersion(instanceId: number) {
  return useQuery({
    queryKey: ['instance-probe-version', instanceId],
    queryFn: () => api.get<ProbeVersionSelection>(`/instances/${instanceId}/probe-version`).then((r) => r.data),
    enabled: instanceId > 0,
  })
}

export function useSetInstanceProbeVersion(instanceId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (versionId: number) => api.put(`/instances/${instanceId}/probe-version`, { versionId }).then((r) => r.data),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['instance-probe-version', instanceId] })
      qc.invalidateQueries({ queryKey: ['probe-update', instanceId] })
    },
  })
}
