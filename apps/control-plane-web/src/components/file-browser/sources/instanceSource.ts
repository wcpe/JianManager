/**
 * 实例工作目录的文件浏览器数据源适配器（FR-213）。
 *
 * 把既有实例文件端点（`@/api/files`，FR-008/070）适配成与后端解耦的 {@link FileBrowserSource}，
 * 使共享 {@link FileBrowser} 能用于实例工作目录的**只读浏览 / 内容预览 / 下载**。
 * 二进制 / 超大判定的口径在此（适配器决定，组件只消费结果）。
 *
 * 注意：实例的**写/编辑/版本/上传**仍由 `explorer/ResourceExplorer` + `config-explorer/*` 承载，
 * 本适配器只提供只读浏览面（与全功能管理器并存，能力不减；详见 spec）。
 */
import { fetchFileList, readFileContent, downloadFile, type FileInfo } from '@/api/files'
import { joinPath } from '@/components/explorer/paths'
import type { FileBrowserSource, FileEntry, PreviewContent } from '../types'

/** 超过此字节数的文件不读全量、降级为「仅下载」。 */
export const PREVIEW_MAX_BYTES = 1024 * 1024 // 1 MiB

/** 含 NUL 字节即判为二进制（与 ArchiveViewer 二进制判定范式一致的启发式）。 */
export function looksBinary(text: string): boolean {
  return /\0/.test(text)
}

/** 把后端 FileInfo 映射为浏览器条目（拼出相对根的完整 path）。 */
function toEntry(dir: string, f: FileInfo): FileEntry {
  return {
    path: joinPath(dir, f.name),
    name: f.name,
    isDir: f.isDir,
    size: f.size,
    modTime: f.modTime,
  }
}

/**
 * 构建实例工作目录数据源。
 * 懒加载分层：点目录展开时拉该层（`GET /instances/:id/files?path=`）。
 */
export function instanceFileSource(instanceId: number): FileBrowserSource {
  return {
    flat: false,
    list: async (dirPath: string): Promise<FileEntry[]> => {
      const data = await fetchFileList(instanceId, dirPath)
      return data.map((f) => toEntry(dirPath, f))
    },
    readContent: async (entry: FileEntry): Promise<PreviewContent> => {
      // 超大：不读全量，直接降级（按列表给出的 size 判定）。
      if (entry.size != null && entry.size > PREVIEW_MAX_BYTES) {
        return { kind: 'too-large', size: entry.size }
      }
      const text = await readFileContent(instanceId, entry.path)
      if (looksBinary(text)) return { kind: 'binary' }
      return { kind: 'text', content: text }
    },
    download: (entry: FileEntry) => downloadFile(instanceId, entry.path),
  }
}
