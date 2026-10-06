import { storageFileSource as buildStorageFileSource } from '@jianmanager/ui/lib/storage-source'
import api from '@/api/client'
import type { StorageFileEntry } from '@/api/storage'

/**
 * 平台存储数据源接线层（ADR-097）：把取数注入包内构造器，消费方零改动。
 */
export function storageFileSource(messages?: { noPreview?: string }) {
  return buildStorageFileSource({
    noPreview: messages?.noPreview,
    listFiles: async (path: string) => {
      const { data } = await api.get<StorageFileEntry[]>('/storage/files', { params: { path } })
      return data
    },
  })
}
