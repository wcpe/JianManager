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
