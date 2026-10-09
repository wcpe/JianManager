import { describe, it, expect, vi, beforeEach } from 'vitest'

// hoisted mock 暴露 api 方法 spy，供 uploadFileChunked 的 0 字节文件契约断言（BUG：空文件被 init 400 弃单）。
const { postMock, putMock, deleteMock } = vi.hoisted(() => ({
  postMock: vi.fn(),
  putMock: vi.fn(),
  deleteMock: vi.fn(),
}))
vi.mock('@/api/client', () => ({ default: { post: postMock, put: putMock, delete: deleteMock } }))

import { sliceRanges, progressBytes, uploadFileChunked, type ChunkRange } from './chunkedUpload'

/** 分块上传切片数学（FR-251）：片数 / 边界 / 末片 / 进度归并纯逻辑。 */
describe('sliceRanges', () => {
  it('整除：等分且无末片余量', () => {
    const r = sliceRanges(30, 10)
    expect(r).toEqual<ChunkRange[]>([
      { index: 0, start: 0, end: 10 },
      { index: 1, start: 10, end: 20 },
      { index: 2, start: 20, end: 30 },
    ])
  })

  it('非整除：末片为余量（ceil 片数）', () => {
    const r = sliceRanges(25, 10)
    expect(r).toHaveLength(3) // ceil(25/10)=3
    expect(r[2]).toEqual({ index: 2, start: 20, end: 25 })
    // 末片字节数 = 5
    expect(r[2].end - r[2].start).toBe(5)
  })

  it('单片：文件小于一片', () => {
    expect(sliceRanges(5, 10)).toEqual([{ index: 0, start: 0, end: 5 }])
  })

  it('恰好一整片：不产生空末片', () => {
    expect(sliceRanges(10, 10)).toEqual([{ index: 0, start: 0, end: 10 }])
  })

  it('空文件：无分片', () => {
    expect(sliceRanges(0, 10)).toEqual([])
  })

  it('区间连续、覆盖全文件、无重叠', () => {
    const total = 12345
    const ranges = sliceRanges(total, 1000)
    expect(ranges[0].start).toBe(0)
    expect(ranges[ranges.length - 1].end).toBe(total)
    for (let i = 1; i < ranges.length; i++) {
      expect(ranges[i].start).toBe(ranges[i - 1].end) // 首尾相接
    }
    // 覆盖总字节。
    const covered = ranges.reduce((s, r) => s + (r.end - r.start), 0)
    expect(covered).toBe(total)
  })

  it('大文件不整数截断（>2GiB，验 number 精度足够）', () => {
    const total = 2 * 1024 * 1024 * 1024 + 12345 // ~2GiB+
    const chunk = 8 * 1024 * 1024 // 8 MiB
    const ranges = sliceRanges(total, chunk)
    expect(ranges).toHaveLength(Math.ceil(total / chunk))
    expect(ranges[ranges.length - 1].end).toBe(total)
  })

  it('非法参数抛错', () => {
    expect(() => sliceRanges(10, 0)).toThrow()
    expect(() => sliceRanges(10, -1)).toThrow()
    expect(() => sliceRanges(-1, 10)).toThrow()
  })
})

/**
 * 0 字节文件（整合包常见 .gitkeep / 空配置）上传契约：init(totalSize=0) → 零次分片 PUT →
 * 直达 complete；进度回调有终态且无 NaN（uploadedBytes === totalBytes === 0 即 100%）。
 */
describe('uploadFileChunked 0 字节文件', () => {
  beforeEach(() => {
    postMock.mockReset()
    putMock.mockReset()
    deleteMock.mockReset()
  })

  it('init 报 totalSize=0、零次 chunk 请求、直达 complete 并返回结果', async () => {
    const chunkSize = 8 * 1024 * 1024
    const completeResult = {
      sha256: 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855',
      md5: 'd41d8cd98f00b204e9800998ecf8427e',
      size: 0,
      codec: 'none',
    }
    postMock.mockImplementation((url: string) => {
      if (/\/uploads$/.test(url)) {
        return Promise.resolve({ data: { uploadId: 'u1', chunkSize, chunkCount: 0 } })
      }
      return Promise.resolve({ data: completeResult })
    })

    const progress: Array<[number, number]> = []
    const res = await uploadFileChunked('ch-1', new File([], '.gitkeep'), {
      onProgress: (uploaded, total) => progress.push([uploaded, total]),
    })

    // init 声明 totalSize=0（不再由前端回避空文件）。
    const [initUrl, initBody] = postMock.mock.calls[0] as [string, { totalSize: number; filename: string }]
    expect(initUrl).toBe('/client-channels/ch-1/uploads')
    expect(initBody.totalSize).toBe(0)
    expect(initBody.filename).toBe('.gitkeep')

    // 空文件无分片：零次 chunk PUT。
    expect(putMock).not.toHaveBeenCalled()

    // 直达 complete（第二次 post 即 complete）。
    const [completeUrl] = postMock.mock.calls[1] as [string]
    expect(completeUrl).toBe('/client-channels/ch-1/uploads/u1/complete')

    // 成功路径不弃单。
    expect(deleteMock).not.toHaveBeenCalled()

    // 返回 complete 的内容寻址元数据。
    expect(res).toEqual(completeResult)

    // 进度回调有终态：uploadedBytes === totalBytes（0/0 即已传完）、无 NaN、不为负。
    expect(progress.length).toBeGreaterThan(0)
    const [lastUploaded, lastTotal] = progress[progress.length - 1]
    expect(lastUploaded).toBe(lastTotal)
    for (const [u, t] of progress) {
      expect(Number.isNaN(u)).toBe(false)
      expect(Number.isNaN(t)).toBe(false)
      expect(u).toBeGreaterThanOrEqual(0)
      expect(t).toBeGreaterThanOrEqual(0)
    }
  })

  it('init 被拒（0 字节旧后端 400）时错误上抛且不发 chunk/complete', async () => {
    postMock.mockRejectedValueOnce(new Error('INVALID_UPLOAD_INIT'))
    await expect(uploadFileChunked('ch-1', new File([], '.gitkeep'))).rejects.toThrow('INVALID_UPLOAD_INIT')
    expect(putMock).not.toHaveBeenCalled()
    expect(postMock).toHaveBeenCalledTimes(1) // 仅 init，无 complete
  })
})

describe('progressBytes', () => {
  it('已完成 N 片按整片计', () => {
    expect(progressBytes(0, 10, 25)).toBe(0)
    expect(progressBytes(1, 10, 25)).toBe(10)
    expect(progressBytes(2, 10, 25)).toBe(20)
  })

  it('末片完成不超过总字节（封顶 totalSize）', () => {
    // 3 片 * 10 = 30 > 25 → 封顶 25。
    expect(progressBytes(3, 10, 25)).toBe(25)
  })

  it('整除时末片正好等于总字节', () => {
    expect(progressBytes(3, 10, 30)).toBe(30)
  })
})
