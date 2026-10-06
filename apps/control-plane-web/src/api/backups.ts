import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/** 备份记录。status/mode/type 取值与后端 model.Backup 对齐。 */
// 备份记录契约已归包（受控视图与业务页面共用，ADR-097）；此处原样再导出，调用点无需改动。
import type { BackupInfo } from '@jianmanager/ui/lib/backup'
export type { BackupInfo } from '@jianmanager/ui/lib/backup'

/** 创建备份请求体。 */
export interface CreateBackupBody {
  name?: string
  /** 增量备份，挂到最近一次已完成备份后形成链（FR-056） */
  incremental?: boolean
  /** 目标远程存储后端 ID；缺省存于节点本地（FR-057） */
  storageId?: number
}

/** useBackups 选项。 */
export interface UseBackupsOptions {
  /**
   * 轮询间隔毫秒；存在进行中备份时由调用方传入以刷新进度（FR-151）。
   * 省略或传 false 时不轮询，保持既有行为（其他调用方不受影响）。
   */
  refetchInterval?: number | false
}

export function useBackups(instanceId?: number, options?: UseBackupsOptions) {
  return useQuery({
    queryKey: ['backups', instanceId],
    queryFn: async () => {
      if (!instanceId) return []
      const { data } = await api.get<BackupInfo[]>(`/instances/${instanceId}/backups`)
      return data
    },
    enabled: !!instanceId,
    refetchInterval: options?.refetchInterval ?? false,
  })
}

export function useCreateBackup(instanceId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body?: CreateBackupBody) =>
      api.post(`/instances/${instanceId}/backups`, body ?? {}),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['backups'] }); qc.invalidateQueries({ queryKey: ['tasks'] }) },
  })
}

export function useRestoreBackup() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (backupId: number) => api.post(`/backups/${backupId}/restore`),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['backups'] }); qc.invalidateQueries({ queryKey: ['tasks'] }) },
  })
}

export function useDeleteBackup() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (backupId: number) => api.delete(`/backups/${backupId}`),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['backups'] }); qc.invalidateQueries({ queryKey: ['tasks'] }) },
  })
}
