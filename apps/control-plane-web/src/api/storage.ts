import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'

/** 一个 FHS 子目录的占用统计与用途（与后端 service.DirUsage 对应，FR-083）。 */
export interface DirUsage {
  /** 相对数据根、以「/」分隔的路径（如 "var/artifacts"）。 */
  path: string
  /** 用途标注键（前端 i18n 解析，如 "artifacts"）。 */
  label: string
  size: number
  fileCount: number
  exists: boolean
  /** 是否允许受控清理（仅 cache/）。 */
  clearable: boolean
}

/** 制品库归档冷热分布（FR-045 storage_state 可见，FR-083）。 */
export interface ArchiveSummary {
  hotCount: number
  archivedCount: number
  externalCount: number
  hotSize: number
  archivedSize: number
  externalSize: number
}

/** 平台存储概览（与后端 service.StorageOverview 对应，FR-083）。 */
export interface StorageOverview {
  /** 数据根绝对路径（只读展示）。 */
  base: string
  dirs: DirUsage[]
  totalSize: number
  totalFiles: number
  archive: ArchiveSummary
}

/** 数据根内一个文件/目录项（与后端 service.FileEntry 对应，复用 explorer FileInfo 同形）。 */
export interface StorageFileEntry {
  name: string
  isDir: boolean
  size: number
  modTime: number
}

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
