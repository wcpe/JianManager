/**
 * 资源浏览器的一条目录项与文件名判定（FR-373 等）。
 *
 * 从应用侧 `api/files.ts` 与 `api/archive.ts` 抽出：这两处混着取数函数与 axios 调用，
 * 而「一条条目长什么样」「这个名字是不是归档/类文件」都是纯数据与纯函数，
 * 视图侧要复用。应用侧原文件原样转出，调用点零改动。
 */

/** 一条目录项（文件或目录）。 */
export interface FileInfo {
  name: string
  isDir: boolean
  size: number
  modTime: number
  /** 八进制权限串，如 "0644"（Windows 可空）。 */
  modeOctal?: string
  /** rwx 展示串，如 "rw-r--r--"（Windows 可空）。 */
  modeString?: string
  /** 相对 Worker 进程用户是否可读。 */
  readable?: boolean
  /** 相对 Worker 进程用户是否可写。 */
  writable?: boolean
  owner?: string
  group?: string
}

/** 判断文件名是否为可查看的归档（jar/zip）。 */
export function isArchiveName(name: string): boolean {
  const lower = name.toLowerCase()
  return lower.endsWith('.jar') || lower.endsWith('.zip')
}

/** 判断某归档内条目（或工作目录文件名）是否为可反编译的 class。 */
export function isClassName(name: string): boolean {
  return name.toLowerCase().endsWith('.class')
}
