import api from '@/api/client'

/** 归档（jar/zip）内的单个条目（与后端 service.ArchiveEntry 对应，FR-075）。 */
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

/** 列举归档条目的结果（FR-075）。 */
export interface ArchiveEntries {
  entries: ArchiveEntry[]
  /** 条目数超上限被截断。 */
  truncated: boolean
}

/** 反编译结果（与后端 service.DecompileResult 对应，FR-075）。 */
export interface DecompileResult {
  success: boolean
  /** 失败/降级原因（success=false 时）。 */
  error?: string
  /** 反编译 Java 源码（截断到上限）。 */
  source: string
  truncated: boolean
  /** 反编译器标识（如 "CFR 0.152"）。 */
  decompiler?: string
}

/** 读取归档内某条目内容的结果（FR-075）。 */
export interface ArchiveEntryContent {
  /** 条目文本内容（二进制条目时为占位提示，由调用方据 binary 决定展示）。 */
  text: string
  truncated: boolean
  binary: boolean
}

/** 列出某归档（jar/zip）内全部条目（FR-075）。 */
export async function listArchiveEntries(
  instanceId: number,
  path: string,
): Promise<ArchiveEntries> {
  const { data } = await api.get<ArchiveEntries>(
    `/instances/${instanceId}/files/archive/entries`,
    { params: { path } },
  )
  return data
}

/** 读取归档内某条目内容（文本预览，FR-075）。截断/二进制经响应头标注。 */
export async function readArchiveEntry(
  instanceId: number,
  path: string,
  entry: string,
): Promise<ArchiveEntryContent> {
  const resp = await api.get(`/instances/${instanceId}/files/archive/read`, {
    params: { path, entry },
    responseType: 'text',
  })
  const truncated = resp.headers['x-truncated'] === 'true'
  const binary = resp.headers['x-binary'] === 'true'
  return { text: resp.data as string, truncated, binary }
}

/** 反编译工作目录内 class/jar（或归档内某 class）为 Java 源码（FR-075）。 */
export async function decompile(
  instanceId: number,
  path: string,
  entry?: string,
): Promise<DecompileResult> {
  const { data } = await api.post<DecompileResult>(
    `/instances/${instanceId}/files/decompile`,
    { path, entry: entry ?? '' },
  )
  return data
}

/** 判断某文件名是否为可打开的归档（jar/zip）。 */
export function isArchiveName(name: string): boolean {
  const lower = name.toLowerCase()
  return lower.endsWith('.jar') || lower.endsWith('.zip')
}

/** 判断某归档内条目（或工作目录文件名）是否为可反编译的 class。 */
export function isClassName(name: string): boolean {
  return name.toLowerCase().endsWith('.class')
}
