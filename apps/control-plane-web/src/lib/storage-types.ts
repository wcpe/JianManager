/**
 * 平台存储的类型（FR-083）。
 *
 * 从应用侧 `api/storage.ts` 抽出：那里混着两个取数 hook，而概览/条目/归档统计
 * 都是纯数据，视图侧要复用。应用侧原文件原样转出，调用点零改动。
 */

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
