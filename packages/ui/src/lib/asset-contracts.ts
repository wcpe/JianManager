export type AssetType = 'core' | 'plugin' | 'image' | 'video' | 'archive' | 'blob' | 'client-file'
export interface AssetInfo {
  id: number
  type: AssetType
  name: string
  version: string
  filename: string
  sha256: string
  md5: string
  size: number
  contentType: string
  sourceUrl: string
  metadata: string
  storageState: 'hot' | 'archived' | 'external' | 'lost'
  storageBackend: string
  storageChannelId: number
  refCount: number
  relPath: string
  createdAt: string
  lastUsedAt: string | null
}