import { describe, it, expect, vi, beforeEach } from 'vitest'

// hoisted mock 暴露 api 方法 spy：既供 uploadFileChunked 的分块协议契约断言
// （BUG：空文件被 init 400 弃单），也供「编排器拿到的是真实 api 实现」的接线断言。
const { postMock, putMock, deleteMock } = vi.hoisted(() => ({
  postMock: vi.fn(),
  putMock: vi.fn(),
  deleteMock: vi.fn(),
}))
vi.mock('@/api/client', () => ({ default: { post: postMock, put: putMock, delete: deleteMock } }))

// 秒传预查 / 聚合批替身：默认分别「全 miss」「回显结果」，
// 这样分块路径能在不触网的前提下真跑到 uploadFileChunked。
const { precheckMock, batchMock } = vi.hoisted(() => ({
  precheckMock: vi.fn(async (_channelId: string, query: { sha256: string; size: number }[]) =>
    query.map((q) => ({ sha256: q.sha256, hit: false })),
  ),
  batchMock: vi.fn(
    async (_channelId: string, entries: { filename: string; size: number; sha256: string; file: File }[]) =>
      entries.map((e) => ({ sha256: e.sha256, md5: 'md5-batch', size: e.size, codec: 'none' })),
  ),
}))
vi.mock('@/api/clientVersions', () => ({
  precheckClientFiles: precheckMock,
  uploadClientFilesBatch: batchMock,
}))

import { uploadFileChunked, uploadFilesEfficient } from './client-upload-api'
import { AGGREGATE_MAX_FILE_BYTES } from './client-upload-plan'

/**
 * api 接线层契约（`client-upload-api.ts`）：这里覆盖 **api 依赖真实接上** 的事实——
 * 分块协议逐请求打到 `@/api/client`，编排器的三个取数依赖确实是对外那两个入口。
 *
 * 纯逻辑与编排分支不在这里测：切片数学/进度归并在 `chunked-upload.test.ts`，
 * 编排分支（命中/聚合/分块/降级）在 `efficient-upload.test.ts`（直接注入替身）。
 */

/** 清空 api spy 的调用记录与实现（各用例自设实现）。 */
function resetApiSpies(): void {
  postMock.mockReset()
  putMock.mockReset()
  deleteMock.mockReset()
}

/** 恢复取数替身的默认行为，避免用例间的 mockImplementationOnce 泄漏。 */
function resetDepMocks(): void {
  precheckMock.mockReset()
  precheckMock.mockImplementation(async (_channelId, query) =>
    query.map((q) => ({ sha256: q.sha256, hit: false })),
  )
  batchMock.mockReset()
  batchMock.mockImplementation(async (_channelId, entries) =>
    entries.map((e) => ({ sha256: e.sha256, md5: 'md5-batch', size: e.size, codec: 'none' })),
  )
}

/** 构造声明大小虚高的文件（不真实分配内存；内容仅 1 字节）。 */
function fakeSizeFile(name: string, size: number): File {
  const f = new File(['x'], name)
  Object.defineProperty(f, 'size', { value: size })
  return f
}

/**
 * 0 字节文件（整合包常见 .gitkeep / 空配置）上传契约：init(totalSize=0) → 零次分片 PUT →
 * 直达 complete；进度回调有终态且无 NaN（uploadedBytes === totalBytes === 0 即 100%）。
 */
describe('uploadFileChunked 0 字节文件', () => {
  beforeEach(resetApiSpies)

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

/**
 * 接线契约：发布页调的 `uploadFilesEfficient` 必须把应用侧实现注入到纯编排器。
 * 三条用例分别钉住三个依赖位（预查 / 聚合批 / 分块）——尤其是分块位注入的
 * 必须是真实 `uploadFileChunked`（会打到 `@/api/client`），而不是漏配的空实现。
 */
describe('uploadFilesEfficient api 接线', () => {
  beforeEach(() => {
    resetApiSpies()
    resetDepMocks()
  })

  it('预查注入：命中者零字节落定，全程不发上传请求', async () => {
    precheckMock.mockImplementationOnce(async (_channelId, query) =>
      query.map((q) => ({
        sha256: q.sha256,
        hit: true,
        result: { sha256: q.sha256, md5: 'md5-hit', size: q.size, codec: 'none' },
      })),
    )

    const out = await uploadFilesEfficient(
      'ch-1',
      [{ key: 'a', file: new File(['aaa'], 'a.txt'), label: 'a.txt' }],
      {},
    )

    // 预查拿到了频道号与真实算出的 sha256。
    expect(precheckMock.mock.calls[0][0]).toBe('ch-1')
    expect(precheckMock.mock.calls[0][1]).toHaveLength(1)
    expect(precheckMock.mock.calls[0][1][0].sha256).toMatch(/^[0-9a-f]{64}$/)

    expect(out.get('a')?.md5).toBe('md5-hit')
    expect(batchMock).not.toHaveBeenCalled()
    expect(postMock).not.toHaveBeenCalled() // 后缀末上传分流：无 init / complete
    expect(putMock).not.toHaveBeenCalled()
  })

  it('聚合批注入：miss 小文件交给 uploadClientFilesBatch，且参数带频道号与文件元信息', async () => {
    const out = await uploadFilesEfficient(
      'ch-7',
      [{ key: 'a', file: new File(['aaa'], 'a.txt'), label: 'mods/a.txt' }],
      {},
    )

    expect(batchMock).toHaveBeenCalledTimes(1)
    expect(batchMock.mock.calls[0][0]).toBe('ch-7')
    expect(batchMock.mock.calls[0][1][0].filename).toBe('a.txt')
    expect(batchMock.mock.calls[0][1][0].sha256).toMatch(/^[0-9a-f]{64}$/)

    expect(out.get('a')?.codec).toBe('none')
  })

  it('分块注入：大文件走真实 uploadFileChunked（init → 逐片 PUT → complete）', async () => {
    const chunkSize = 8 * 1024 * 1024
    const completeResult = { sha256: 'a'.repeat(64), md5: 'b'.repeat(32), size: 0, codec: 'none' }
    postMock.mockImplementation((url: string) => {
      if (/\/uploads$/.test(url)) {
        return Promise.resolve({ data: { uploadId: 'u9', chunkSize, chunkCount: 2 } })
      }
      return Promise.resolve({ data: completeResult })
    })
    putMock.mockResolvedValue({ data: {} })

    const big = fakeSizeFile('big.jar', AGGREGATE_MAX_FILE_BYTES + 1)
    const out = await uploadFilesEfficient('ch-1', [{ key: 'big', file: big, label: 'big.jar' }], {})

    // 分块位注入的确为真实客户端：init 声明总大小 → 按服务端 chunkSize 逐片 PUT → complete。
    const urls = postMock.mock.calls.map((c) => c[0] as string)
    expect(urls[0]).toBe('/client-channels/ch-1/uploads')
    expect(urls[1]).toBe('/client-channels/ch-1/uploads/u9/complete')
    expect(postMock.mock.calls[0][1]).toMatchObject({ totalSize: big.size })

    // 8MiB+1 字节按 8MiB 切片 → 两片，序号 0/1。
    expect(putMock.mock.calls.map((c) => c[0])).toEqual([
      '/client-channels/ch-1/uploads/u9/chunks/0',
      '/client-channels/ch-1/uploads/u9/chunks/1',
    ])

    // 走的是分块而非聚合。
    expect(batchMock).not.toHaveBeenCalled()
    expect(out.get('big')).toBeDefined()
  })
})
