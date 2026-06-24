import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'

/** 文件/目录信息（与后端 service.FileInfo 对应，FR-008）。 */
export interface FileInfo {
  name: string
  isDir: boolean
  size: number
  modTime: number
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

/** 上传单个文件（multipart，FR-008；覆盖前自动快照 FR-051）。 */
export async function uploadFile(
  instanceId: number,
  destPath: string,
  file: File | Blob,
): Promise<void> {
  const form = new FormData()
  form.append('file', file)
  form.append('path', destPath)
  await api.post(`/instances/${instanceId}/files/upload`, form, {
    headers: { 'Content-Type': 'multipart/form-data' },
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

/**
 * 跨文件全文搜索 / 文件名快速打开（FR-074，POST /files/search）。
 * 转发到 Worker 本地倒排索引查询，返回命中文件+行+片段。
 */
export async function searchFiles(
  instanceId: number,
  query: string,
  mode: SearchMode = 'content',
  maxResults = 200,
): Promise<SearchResult> {
  const { data } = await api.post<SearchResult>(`/instances/${instanceId}/files/search`, {
    query,
    mode,
    maxResults,
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
