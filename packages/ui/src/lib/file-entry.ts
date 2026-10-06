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

/** 归档内一条条目（与后端 service.ArchiveEntry 对应，FR-075）。 */
export interface ArchiveEntry {
  /** 归档内条目名（「/」分隔；目录条目以「/」结尾）。 */
  name: string
  isDir: boolean
  /** 解压后字节。 */
  size: number
  compressedSize: number
  /** Unix 秒。 */
  modified: number
  crc32: number
}

/** 归档内条目列表结果（FR-075）。 */
export interface ArchiveEntries {
  entries: ArchiveEntry[]
  /** 条目数超上限被截断。 */
  truncated: boolean
}

/** 文件搜索模式（FR-074）。 */
export type SearchMode = 'content' | 'filename'

/** 一条搜索命中（与后端 service.SearchHit 对应，FR-074）。 */
export interface SearchHit {
  /** 相对工作目录、以 / 分隔的路径。 */
  path: string
  /** 命中行号（1 起；filename 模式为 0）。 */
  line: number
  /** 命中行片段（仅 content 模式）。 */
  snippet: string
}

/** 搜索范围（FR-074）。 */
export interface SearchScope {
  /** 限定在该相对目录内搜索，空表示全工作目录。 */
  rootPath?: string
  /** 限定文件扩展名，形如 .yml。空表示不限。 */
  extensions?: string[]
}

/** 搜索结果（FR-074）。 */
export interface SearchResult {
  hits: SearchHit[]
  /** 命中达到上限被截断。 */
  truncated: boolean
  /** 索引首建未就绪（FR-113，ADR-024）：hits 为空，应稍后用同一查询重试。 */
  indexing: boolean
}

/** 归档内某条目的内容（FR-075）。 */
export interface ArchiveEntryContent {
  /** 条目文本内容（二进制条目时为占位提示，由调用方据 binary 决定展示）。 */
  text: string
  truncated: boolean
  binary: boolean
}
