import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'

// 存储类型已回迁应用侧（原 ADR-097 迁包已撤销）；此处转出，调用点零改动。
import type { StorageOverview, StorageFileEntry } from '@/lib/storage-types'
export type { DirUsage, ArchiveSummary, StorageOverview, StorageFileEntry } from '@/lib/storage-types'

/** 拉取平台存储概览（FR-083）。仅平台管理员可见。 */
export function useStorageOverview() {
  return useQuery({
    queryKey: ['storage', 'overview'],
    queryFn: async () => {
      const { data } = await api.get<StorageOverview>('/storage/overview')
      return data
    },
  })
}

/** 列举数据根内某目录直接子项（FR-083）。空 path 为数据根。 */
export function useStorageFiles(path: string) {
  return useQuery({
    queryKey: ['storage', 'files', path],
    queryFn: async () => {
      const { data } = await api.get<StorageFileEntry[]>('/storage/files', { params: { path } })
      return data
    },
  })
}

/** 清空 cache/ 内容（受控清理，FR-083）。返回删除条目数。 */
export async function clearStorageCache(): Promise<number> {
  const { data } = await api.post<{ removed: number }>('/storage/cache/clear')
  return data.removed
}
