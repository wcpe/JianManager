import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/** 备份远程存储后端。凭证以 ${ENV_VAR} 引用，后端不返回明文（FR-057）。 */
export interface BackupStorage {
  id: number
  name: string
  /** local | s3 | sftp | webdav */
  type: string
  endpoint: string
  bucket: string
  region: string
  prefix: string
  /** Access Key 的环境变量引用，如 ${JIANMANAGER_BACKUP_S3_AK} */
  accessKeyEnv: string
  /** Secret Key 的环境变量引用 */
  secretKeyEnv: string
  useSsl: boolean
  lastTestAt?: string
  lastTestOk: boolean
  lastTestMessage: string
  /** 已完成备份份数（后端聚合，不落库存储记录）。 */
  backupCount: number
  /** 已完成备份占用字节数（后端聚合，不落库存储记录）。 */
  usedBytes: number
  createdAt: string
}

export interface BackupStorageTestResult {
  ok: boolean
  message: string
  errorCode?: string
  latencyMs?: number
  nodeUuid?: string
}

/** 创建存储后端请求体。 */
export interface CreateBackupStorageBody {
  name: string
  type: string
  endpoint?: string
  bucket?: string
  region?: string
  prefix?: string
  accessKeyEnv?: string
  secretKeyEnv?: string
  useSsl?: boolean
}

export function useBackupStorages() {
  return useQuery({
    queryKey: ['backup-storages'],
    queryFn: async () => {
      const { data } = await api.get<BackupStorage[]>('/backup-storages')
      return data
    },
  })
}

/** PUT body 与创建同形；type 须与现值一致（FR-338）。 */
export type UpdateBackupStorageBody = CreateBackupStorageBody

export interface UpdateBackupStorageVars extends CreateBackupStorageBody {
  id: number
}

export function useCreateBackupStorage() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: CreateBackupStorageBody) => api.post('/backup-storages', body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['backup-storages'] }),
  })
}

/** 更新存储后端（FR-338）。全量替换，body 同创建（type 须与现值一致，否则后端回 422）。 */
export function useUpdateBackupStorage() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, ...body }: UpdateBackupStorageVars) => api.put(`/backup-storages/${id}`, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['backup-storages'] }),
  })
}

export function useDeleteBackupStorage() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.delete(`/backup-storages/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['backup-storages'] }),
  })
}

export function useTestBackupStorage() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.post<BackupStorageTestResult>(`/backup-storages/${id}/test`).then((r) => r.data),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['backup-storages'] }),
  })
}

export function useTestBackupStorageDraft() {
  return useMutation({
    mutationFn: (body: CreateBackupStorageBody) =>
      api.post<BackupStorageTestResult>('/backup-storages/test', body).then((r) => r.data),
  })
}
