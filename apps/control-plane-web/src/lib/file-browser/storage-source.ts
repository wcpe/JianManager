/**
 * 平台存储 FileBrowser 数据源（FR-378 / FR-083）。
 * 仅 list：后端无读内容端点，文件预览返回 error 提示只读列表。
 */
import type { StorageFileEntry } from './storage-types'
import type { FileBrowserSource, FileEntry, PreviewContent } from './file-browser-types'

function joinStorage(dir: string, name: string): string {
  if (!dir) return name
  return `${dir.replace(/\/+$/, '')}/${name}`
}

function toEntry(dir: string, f: StorageFileEntry): FileEntry {
  return {
    path: joinStorage(dir, f.name),
    name: f.name,
    isDir: f.isDir,
    size: f.size,
    modTime: f.modTime,
  }
}

/**
 * 构造平台存储数据源（外壳注入取数，包内不依赖应用侧 api）。
 *
 * @param deps.listFiles 列举数据根内某目录直接子项
 * @param deps.noPreview 文件不可预览时的说明（i18n 由调用方注入）
 */
export function storageFileSource(deps: {
  listFiles: (path: string) => Promise<StorageFileEntry[]>
  noPreview?: string
}): FileBrowserSource {
  const noPreview = deps.noPreview ?? '平台存储仅支持浏览列表，不提供内容预览'
  return {
    flat: false,
    list: async (dirPath: string): Promise<FileEntry[]> => {
      const data = await deps.listFiles(dirPath)
      return data.map((f) => toEntry(dirPath, f))
    },
    readContent: async (entry: FileEntry): Promise<PreviewContent> => {
      if (entry.isDir) return { kind: 'error', message: noPreview }
      return { kind: 'error', message: noPreview }
    },
  }
}