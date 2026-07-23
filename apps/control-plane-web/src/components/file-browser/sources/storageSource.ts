/**
 * 平台存储 FileBrowser 数据源（FR-378 / FR-083）。
 * 仅 list：后端无读内容端点，文件预览返回 error 提示只读列表。
 */
import api from '@/api/client'
import type { StorageFileEntry } from '@/api/storage'
import type { FileBrowserSource, FileEntry, PreviewContent } from '../types'

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
 * @param messages.noPreview 文件不可预览时的中文说明（i18n 由调用方注入）
 */
export function storageFileSource(messages?: { noPreview?: string }): FileBrowserSource {
  const noPreview = messages?.noPreview ?? '平台存储仅支持浏览列表，不提供内容预览'
  return {
    flat: false,
    list: async (dirPath: string): Promise<FileEntry[]> => {
      const { data } = await api.get<StorageFileEntry[]>('/storage/files', {
        params: { path: dirPath },
      })
      return data.map((f) => toEntry(dirPath, f))
    },
    readContent: async (entry: FileEntry): Promise<PreviewContent> => {
      if (entry.isDir) return { kind: 'error', message: noPreview }
      return { kind: 'error', message: noPreview }
    },
  }
}
