import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/**
 * 制品存储渠道（FR-347，见 ADR-073）：客户端分发制品（client-file）的落点路由配置。
 * 凭证面板直填、后端可逆加密落库；响应永不含明文/密文，以 hasAccessKey/hasSecretKey 标示。
 */
export interface ArtifactStorageChannel {
  id: number
  name: string
  /** local（内置「本机存储」独占）| s3 */
  type: 'local' | 's3'
  endpoint: string
  bucket: string
  region: string
  prefix: string
  useSsl: boolean
  /** 预签名下载 URL 有效期秒数，[60, 3600]，默认 600。 */
  presignTtlSeconds: number
  /** 活跃渠道 = 新上传 client-file 制品的落点（全表恰一条）。 */
  active: boolean
  /** 内置「本机存储」：不可编辑、不可删除。 */
  builtin: boolean
  hasAccessKey: boolean
  hasSecretKey: boolean
  lastTestAt?: string
  lastTestOk: boolean
  lastTestMessage: string
  createdAt: string
  updatedAt: string
}

/** 创建/编辑渠道请求体。编辑时 accessKey/secretKey 留空 = 保留原值（脱敏编辑语义）。 */
export interface SaveArtifactStorageBody {
  name: string
  type: string
  endpoint?: string
  bucket?: string
  region?: string
  prefix?: string
  accessKey?: string
  secretKey?: string
  useSsl?: boolean
  presignTtlSeconds?: number
}

export interface ArtifactStorageTestResult {
  ok: boolean
  message: string
  errorCode?: string
  latencyMs: number
}

export function useArtifactStorages() {
  return useQuery({
    queryKey: ['artifact-storages'],
    queryFn: async () => {
      const { data } = await api.get<ArtifactStorageChannel[]>('/artifact-storages')
      return data
    },
  })
}

export interface UpdateArtifactStorageVars extends SaveArtifactStorageBody {
  id: number
}

export function useCreateArtifactStorage() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: SaveArtifactStorageBody) => api.post('/artifact-storages', body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['artifact-storages'] }),
  })
}

export function useUpdateArtifactStorage() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, ...body }: UpdateArtifactStorageVars) => api.put(`/artifact-storages/${id}`, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['artifact-storages'] }),
  })
}

export function useDeleteArtifactStorage() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.delete(`/artifact-storages/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['artifact-storages'] }),
  })
}

/** 设活跃渠道：影响后续上传落点；存量制品按各自记录读取，不受影响。 */
export function useActivateArtifactStorage() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) =>
      api.post<ArtifactStorageChannel>(`/artifact-storages/${id}/activate`).then((r) => r.data),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['artifact-storages'] }),
  })
}

/** 已存渠道真连测试（持久化 lastTest*）。 */
export function useTestArtifactStorage() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) =>
      api.post<ArtifactStorageTestResult>(`/artifact-storages/${id}/test`).then((r) => r.data),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['artifact-storages'] }),
  })
}

/** 表单草稿真连测试（不写库）；编辑态凭证留空时带 id 复用存库凭证。 */
export function useTestArtifactStorageDraft() {
  return useMutation({
    mutationFn: (body: SaveArtifactStorageBody & { id?: number }) =>
      api.post<ArtifactStorageTestResult>('/artifact-storages/test', body).then((r) => r.data),
  })
}
