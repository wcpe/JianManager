import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/** 备份记录。status/mode/type 取值与后端 model.Backup 对齐。 */
export interface BackupInfo {
  id: number
  uuid: string
  instanceId: number
  name: string
  filePath: string
  fileSizeMb: number
  /** 触发来源：0=手动, 1=定时 */
  type: number
  /** 备份模式：0=全量, 1=增量（FR-056） */
  mode: number
  /** 状态：0=待处理, 1=进行中, 2=已完成, 3=失败 */
  status: number
  /** 增量备份的父备份 ID，串成备份链；全量为空（FR-056） */
  parentId?: number
  /** 远程存储后端 ID；空表示本地（FR-057） */
  storageId?: number
  /** 远程对象键；本地备份为空（FR-057） */
  storageKey?: string
  createdAt: string
}

/** 创建备份请求体。 */
export interface CreateBackupBody {
  name?: string
  /** 增量备份，挂到最近一次已完成备份后形成链（FR-056） */
  incremental?: boolean
  /** 目标远程存储后端 ID；缺省存于节点本地（FR-057） */
  storageId?: number
}

export function useBackups(instanceId?: number) {
  return useQuery({
    queryKey: ['backups', instanceId],
    queryFn: async () => {
      if (!instanceId) return []
      const { data } = await api.get<BackupInfo[]>(`/instances/${instanceId}/backups`)
      return data
    },
    enabled: !!instanceId,
  })
}

export function useCreateBackup(instanceId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body?: CreateBackupBody) =>
      api.post(`/instances/${instanceId}/backups`, body ?? {}),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['backups'] }),
  })
}

export function useRestoreBackup() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (backupId: number) => api.post(`/backups/${backupId}/restore`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['backups'] }),
  })
}

export function useDeleteBackup() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (backupId: number) => api.delete(`/backups/${backupId}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['backups'] }),
  })
}
