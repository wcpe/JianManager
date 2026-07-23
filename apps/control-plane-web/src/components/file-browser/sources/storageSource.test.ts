import { describe, it, expect, vi, beforeEach } from 'vitest'
import { storageFileSource } from './storageSource'

vi.mock('@/api/client', () => ({
  default: {
    get: vi.fn(),
  },
}))

import api from '@/api/client'

describe('storageFileSource（FR-378）', () => {
  beforeEach(() => {
    vi.mocked(api.get).mockReset()
  })

  it('list 映射 StorageFileEntry → FileEntry', async () => {
    vi.mocked(api.get).mockResolvedValue({
      data: [
        { name: 'var', isDir: true, size: 0, modTime: 1 },
        { name: 'readme.txt', isDir: false, size: 12, modTime: 2 },
      ],
    })
    const src = storageFileSource()
    const entries = await src.list('')
    expect(api.get).toHaveBeenCalledWith('/storage/files', { params: { path: '' } })
    expect(entries).toEqual([
      { path: 'var', name: 'var', isDir: true, size: 0, modTime: 1 },
      { path: 'readme.txt', name: 'readme.txt', isDir: false, size: 12, modTime: 2 },
    ])
  })

  it('子目录 path 拼接', async () => {
    vi.mocked(api.get).mockResolvedValue({
      data: [{ name: 'log', isDir: true, size: 0, modTime: 1 }],
    })
    const src = storageFileSource()
    const entries = await src.list('var')
    expect(entries[0].path).toBe('var/log')
  })

  it('readContent 恒为 error（无预览端点）', async () => {
    const src = storageFileSource({ noPreview: '不可预览' })
    const res = await src.readContent({ path: 'a', name: 'a', isDir: false })
    expect(res).toEqual({ kind: 'error', message: '不可预览' })
  })
})
