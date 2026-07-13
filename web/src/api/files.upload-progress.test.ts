import { describe, it, expect, vi, beforeEach } from 'vitest'

// hoisted mock 暴露 post spy，断言 uploadFile 的 onUploadProgress → onProgress 百分比换算（FR-324）。
const { postMock } = vi.hoisted(() => ({ postMock: vi.fn() }))
vi.mock('@/api/client', () => ({ default: { post: postMock, get: vi.fn() } }))

import { uploadFile } from './files'

describe('uploadFile 上传进度百分比（FR-324）', () => {
  beforeEach(() => {
    postMock.mockReset()
    postMock.mockResolvedValue({ data: null })
  })

  it('total 可得时 onProgress 收到 0~100 整数百分比', async () => {
    const seen: number[] = []
    await uploadFile(1, 'a.txt', new Blob(['x']), (p) => seen.push(p))
    const cfg = postMock.mock.calls[0]![2] as { onUploadProgress?: (e: { loaded: number; total?: number }) => void }
    expect(cfg.onUploadProgress).toBeTypeOf('function')
    cfg.onUploadProgress!({ loaded: 0, total: 200 })
    cfg.onUploadProgress!({ loaded: 100, total: 200 })
    cfg.onUploadProgress!({ loaded: 200, total: 200 })
    expect(seen).toEqual([0, 50, 100])
  })

  it('total 不可得时回落 -1（不确定态）', async () => {
    const seen: number[] = []
    await uploadFile(1, 'a.txt', new Blob(['x']), (p) => seen.push(p))
    const cfg = postMock.mock.calls[0]![2] as { onUploadProgress?: (e: { loaded: number; total?: number }) => void }
    cfg.onUploadProgress!({ loaded: 123 })
    expect(seen).toEqual([-1])
  })

  it('不传 onProgress 时不注册 onUploadProgress（零开销旁路）', async () => {
    await uploadFile(1, 'a.txt', new Blob(['x']))
    const cfg = postMock.mock.calls[0]![2] as { onUploadProgress?: unknown }
    expect(cfg.onUploadProgress).toBeUndefined()
  })

  it('目标路径经 query 传递（FR-304 契约不回归）', async () => {
    await uploadFile(7, 'dir/b.txt', new Blob(['x']))
    const [url, , cfg] = postMock.mock.calls[0] as [string, unknown, { params?: { path?: string } }]
    expect(url).toBe('/instances/7/files/upload')
    expect(cfg.params?.path).toBe('dir/b.txt')
  })
})
