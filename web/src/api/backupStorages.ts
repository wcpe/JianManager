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
  createdAt: string
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

export function useCreateBackupStorage() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: CreateBackupStorageBody) => api.post('/backup-storages', body),
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
