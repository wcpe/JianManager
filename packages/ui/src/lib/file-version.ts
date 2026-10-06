/**
 * 文件历史版本的类型（FR-051）。
 *
 * 从应用侧 `api/fileVersions.ts` 抽出：那里混着三个取数 hook，而版本元数据与
 * diff 结果是纯数据，视图侧要复用。应用侧原文件原样转出，调用点零改动。
 */

/** 通用文件版本元数据（与后端 service.FileVersion 对应，FR-051）。 */
export interface FileVersion {
  id: number
  filePath: string
  size: number
  authorId: number
  createdAt: string
  rollbackOfVersionId?: number
}

/** 文件版本差异结果；二进制内容 binary=true 且 unifiedDiff 为空。 */
export interface FileVersionDiff {
  fromVersionId: number
  toVersionId: number
  unifiedDiff: string
  binary: boolean
}
