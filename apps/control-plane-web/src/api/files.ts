import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'

/** 文件/目录信息（与后端 service.FileInfo 对应，FR-008；FR-373 权限元数据加性）。 */
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

/** 写前/浏览前权限探测结果（FR-373）。 */
export interface PathAccess {
  exists: boolean
  isDir: boolean
  readable: boolean
  writable: boolean
  modeOctal?: string
  modeString?: string
  owner?: string
  group?: string
  reason?: string
}

/** 探测实例内路径可读/可写（FR-373）。 */
export async function checkFileAccess(instanceId: number, path: string): Promise<PathAccess> {
  const { data } = await api.post<PathAccess>(`/instances/${instanceId}/files/check-access`, { path })
  return data
}

/**
 * 单 path 非递归 chmod（FR-373）。
 * mode 省略时由 Worker 保证属主可读写（目录含 x）。
 */
export async function chmodFile(
  instanceId: number,
  path: string,
  mode?: string,
): Promise<{ modeOctal: string }> {
  const { data } = await api.post<{ message: string; modeOctal: string }>(
    `/instances/${instanceId}/files/chmod`,
    { path, mode },
  )
  return { modeOctal: data.modeOctal }
}

/** 列出某目录内容（FR-008）。空 path 为工作目录根。 */
export function useFileList(instanceId: number, path: string) {
  return useQuery({
    queryKey: ['files', instanceId, path],
    queryFn: async () => {
      const { data } = await api.get<FileInfo[]>(`/instances/${instanceId}/files`, {
        params: { path },
      })
      return data
    },
    enabled: !!instanceId,
  })
}

/** 直接拉取某目录内容（不走 React Query 缓存；树懒加载子目录用）。 */
export async function fetchFileList(instanceId: number, path: string): Promise<FileInfo[]> {
  const { data } = await api.get<FileInfo[]>(`/instances/${instanceId}/files`, {
    params: { path },
  })
  return data
}

/** 读取文件文本内容（FR-008）。 */
export async function readFileContent(instanceId: number, path: string): Promise<string> {
  const { data } = await api.get(`/instances/${instanceId}/files/read`, {
    params: { path },
    responseType: 'text',
  })
  return data as string
}

/** 写入文件内容（FR-008；后端改前自动快照 FR-051）。 */
export async function writeFileContent(
  instanceId: number,
  path: string,
  content: string,
): Promise<void> {
  await api.post(`/instances/${instanceId}/files/write`, { path, content })
}

/** 删除文件/目录（递归，FR-008）。 */
export async function deleteFile(instanceId: number, path: string): Promise<void> {
  await api.delete(`/instances/${instanceId}/files`, { data: { path } })
}

/** 重命名/移动文件或目录（FR-008/020；跨目录即移动）。 */
export async function renameFile(
  instanceId: number,
  oldPath: string,
  newPath: string,
): Promise<void> {
  await api.post(`/instances/${instanceId}/files/rename`, { oldPath, newPath })
}

/**
 * 上传单个文件（multipart，FR-008；覆盖前自动快照 FR-051）。
 * FR-304：目标路径经 query 参数传递——CP 流式读 multipart（不整块缓冲），
 * 读到 file 部分时必须已知目标路径，form 字段顺序不可依赖。
 */
export async function uploadFile(
  instanceId: number,
  destPath: string,
  file: File | Blob,
  onProgress?: (percent: number) => void,
): Promise<void> {
  const form = new FormData()
  form.append('file', file)
  await api.post(`/instances/${instanceId}/files/upload`, form, {
    params: { path: destPath },
    headers: { 'Content-Type': 'multipart/form-data' },
    // 上传进度百分比（FR-324）：total 可得时报 0~100，否则回落 -1（不确定态由调用方决定展示）。
    onUploadProgress: onProgress
      ? (e) => onProgress(e.total ? Math.round((e.loaded / e.total) * 100) : -1)
      : undefined,
  })
}

/** 触发浏览器下载并清理 object URL。 */
function triggerDownload(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  URL.revokeObjectURL(url)
}

/** 下载单个文件（流式，FR-008）。 */
export async function downloadFile(instanceId: number, path: string): Promise<void> {
  const { data } = await api.get(`/instances/${instanceId}/files/download`, {
    params: { path },
    responseType: 'blob',
  })
  const name = path.split('/').pop() || 'download'
  triggerDownload(data as Blob, name)
}

/** 搜索模式（FR-074）。content=全文，filename=文件名快速打开。 */
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

/** 搜索结果（FR-074）。 */
export interface SearchResult {
  hits: SearchHit[]
  /** 命中达到上限被截断。 */
  truncated: boolean
  /** 索引首建未就绪（FR-113，ADR-024）：hits 为空，应稍后用同一查询重试。 */
  indexing: boolean
}

export interface SearchScope {
  /** 限定在该相对目录内搜索，空表示全工作目录。 */
  rootPath?: string
  /** 限定文件扩展名，形如 .yml。空表示不限。 */
  extensions?: string[]
}

/**
 * 跨文件全文搜索 / 文件名快速打开（FR-074，POST /files/search）。
 * 转发到 Worker 本地倒排索引查询，返回命中文件+行+片段。
 */
export async function searchFiles(
  instanceId: number,
  query: string,
  mode: SearchMode = 'content',
  maxResults = 200,
  scope: SearchScope = {},
): Promise<SearchResult> {
  const { data } = await api.post<SearchResult>(`/instances/${instanceId}/files/search`, {
    query,
    mode,
    maxResults,
    rootPath: scope.rootPath,
    extensions: scope.extensions,
  })
  return data
}

/** 批量下载：选中的文件/目录即时打包 zip 下载（FR-070，POST /files/archive）。 */
export async function downloadArchive(
  instanceId: number,
  paths: string[],
  zipName = 'files.zip',
): Promise<void> {
  const { data } = await api.post(`/instances/${instanceId}/files/archive`, { paths }, {
    responseType: 'blob',
  })
  triggerDownload(data as Blob, zipName)
}
