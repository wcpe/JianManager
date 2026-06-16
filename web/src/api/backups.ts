import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

export interface BackupInfo {
  id: number
  uuid: string
  instanceId: number
  name: string
  filePath: string
  fileSizeMb: number
  type: number // 0=manual, 1=auto
  status: number // 0=ready, 1=creating, 2=failed
  createdAt: string
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
    mutationFn: (body?: { name?: string }) =>
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
