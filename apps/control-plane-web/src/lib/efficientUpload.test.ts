import { describe, it, expect, vi, beforeEach } from 'vitest'
import { uploadFilesEfficient, type EfficientUploadProgress } from './efficientUpload'
import { AGGREGATE_MAX_FILE_BYTES, HASH_MAX_FILE_BYTES } from './clientUploadPlan'
import {
  precheckClientFiles,
  uploadClientFilesBatch,
  type ClientFileResult,
  type PrecheckFileResult,
} from '@/api/clientVersions'
import { uploadFileChunked } from './chunkedUpload'

/**
 * FR-346 上传编排器：预查命中跳过上传 / miss 小文件聚合 / 大文件分块 /
 * 超大免预查 / 预查失败降级 / fail-fast / 进度单调。
 * 网络层（api 封装与分块客户端）全 mock，编排逻辑真跑。
 */

vi.mock('@/api/clientVersions', () => ({
  precheckClientFiles: vi.fn(),
  uploadClientFilesBatch: vi.fn(),
}))
vi.mock('./chunkedUpload', () => ({
  uploadFileChunked: vi.fn(),
}))

const mockPrecheck = vi.mocked(precheckClientFiles)
const mockBatch = vi.mocked(uploadClientFilesBatch)
const mockChunked = vi.mocked(uploadFileChunked)

/** 构造内容可控的小文件。 */
function smallFile(name: string, content: string): File {
  return new File([content], name)
}

/** 构造声明大小虚高的文件（不真实分配内存；hash 只在 ≤256MiB 时才会读内容）。 */
function fakeSizeFile(name: string, size: number): File {
  const f = new File(['x'], name)
  Object.defineProperty(f, 'size', { value: size })
  return f
}

function mkResult(sha256: string, size: number): ClientFileResult {
  return { sha256, md5: 'md5-' + sha256.slice(0, 8), size, codec: 'none' }
}

/** 按请求回显全命中的预查结果。 */
function allHit(files: { sha256: string; size: number }[]): PrecheckFileResult[] {
  return files.map((f) => ({ sha256: f.sha256, hit: true, result: mkResult(f.sha256, f.size) }))
}

/** 按请求回显全未命中的预查结果。 */
function allMiss(files: { sha256: string; size: number }[]): PrecheckFileResult[] {
  return files.map((f) => ({ sha256: f.sha256, hit: false }))
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('uploadFilesEfficient', () => {
  it('全部预查命中：零上传请求，结果直取、字节即刻计满', async () => {
    mockPrecheck.mockImplementation(async (_ch, files) => allHit(files))

    const entries = [
      { key: 'a', file: smallFile('a.txt', 'aaa'), label: 'mods/a.txt' },
      { key: 'b', file: smallFile('b.txt', 'bbbb'), label: 'mods/b.txt' },
    ]
    const events: EfficientUploadProgress[] = []
    const out = await uploadFilesEfficient('ch-1', entries, { onProgress: (p) => events.push({ ...p }) })

    expect(out.size).toBe(2)
    expect(out.get('a')?.codec).toBe('none')
    expect(mockBatch).not.toHaveBeenCalled()
    expect(mockChunked).not.toHaveBeenCalled()

    const last = events[events.length - 1]
    expect(last.reusedFiles).toBe(2)
    expect(last.completedFiles).toBe(2)
    expect(last.uploadedBytes).toBe(3 + 4)
  })

  it('未命中的小文件进聚合批（meta 与内容同序），不走分块', async () => {
    mockPrecheck.mockImplementation(async (_ch, files) => allMiss(files))
    mockBatch.mockImplementation(async (_ch, entries) =>
      entries.map((e) => mkResult(e.sha256, e.size)),
    )

    const entries = [
      { key: 'a', file: smallFile('a.txt', 'aaa'), label: 'a.txt' },
      { key: 'b', file: smallFile('b.txt', 'bb'), label: 'b.txt' },
    ]
    const out = await uploadFilesEfficient('ch-1', entries, {})

    expect(mockBatch).toHaveBeenCalledTimes(1)
    const sent = mockBatch.mock.calls[0][1]
    expect(sent.map((e) => e.filename)).toEqual(['a.txt', 'b.txt'])
    expect(sent[0].sha256).toMatch(/^[0-9a-f]{64}$/)
    expect(mockChunked).not.toHaveBeenCalled()
    expect(out.size).toBe(2)
  })

  it('大文件（>8MiB）走分块并携带 expectedSha256；不入聚合', async () => {
    mockPrecheck.mockImplementation(async (_ch, files) => allMiss(files))
    mockChunked.mockImplementation(async (_ch, file, opts) =>
      mkResult(opts?.expectedSha256 ?? 'no-sha', file.size),
    )

    const big = fakeSizeFile('big.jar', AGGREGATE_MAX_FILE_BYTES + 1)
    const out = await uploadFilesEfficient('ch-1', [{ key: 'big', file: big, label: 'big.jar' }], {})

    expect(mockBatch).not.toHaveBeenCalled()
    expect(mockChunked).toHaveBeenCalledTimes(1)
    // ≤256MiB 的大文件仍算 hash：complete 顺带 expectedSha256 强校验。
    expect(mockChunked.mock.calls[0][2]?.expectedSha256).toMatch(/^[0-9a-f]{64}$/)
    expect(out.get('big')).toBeDefined()
  })

  it('超大文件（>256MiB）不 hash、不进预查，直接分块（无 expectedSha256）', async () => {
    mockPrecheck.mockImplementation(async (_ch, files) => allMiss(files))
    mockChunked.mockImplementation(async (_ch, file) => mkResult('e'.repeat(64), file.size))

    const huge = fakeSizeFile('huge.bin', HASH_MAX_FILE_BYTES + 1)
    const small = smallFile('s.txt', 'ss')
    mockBatch.mockImplementation(async (_ch, entries) =>
      entries.map((e) => mkResult(e.sha256, e.size)),
    )

    await uploadFilesEfficient(
      'ch-1',
      [
        { key: 'huge', file: huge, label: 'huge.bin' },
        { key: 's', file: small, label: 's.txt' },
      ],
      {},
    )

    // 预查只含小文件（1 项），超大者未被 hash。
    expect(mockPrecheck).toHaveBeenCalledTimes(1)
    expect(mockPrecheck.mock.calls[0][1]).toHaveLength(1)
    expect(mockChunked).toHaveBeenCalledTimes(1)
    expect(mockChunked.mock.calls[0][2]?.expectedSha256).toBeUndefined()
  })

  it('预查请求失败：降级全量上传，不阻断发布', async () => {
    mockPrecheck.mockRejectedValue(new Error('precheck 500'))
    mockBatch.mockImplementation(async (_ch, entries) =>
      entries.map((e) => mkResult(e.sha256, e.size)),
    )

    const out = await uploadFilesEfficient(
      'ch-1',
      [{ key: 'a', file: smallFile('a.txt', 'aaa'), label: 'a.txt' }],
      {},
    )
    expect(out.size).toBe(1)
    expect(mockBatch).toHaveBeenCalledTimes(1)
  })

  it('聚合批失败即 fail-fast 抛错（调用方保草稿重试）', async () => {
    mockPrecheck.mockImplementation(async (_ch, files) => allMiss(files))
    const boom = new Error('batch 500')
    mockBatch.mockRejectedValue(boom)

    await expect(
      uploadFilesEfficient('ch-1', [{ key: 'a', file: smallFile('a.txt', 'x'), label: 'a.txt' }], {}),
    ).rejects.toBe(boom)
  })

  it('取消（signal.aborted）在 hash 阶段即抛 AbortError，不发任何请求', async () => {
    const ac = new AbortController()
    ac.abort()
    await expect(
      uploadFilesEfficient('ch-1', [{ key: 'a', file: smallFile('a.txt', 'x'), label: 'a.txt' }], {
        signal: ac.signal,
      }),
    ).rejects.toMatchObject({ name: 'AbortError' })
    expect(mockPrecheck).not.toHaveBeenCalled()
    expect(mockBatch).not.toHaveBeenCalled()
  })

  it('混合场景进度单调不倒退、终值等于总字节', async () => {
    // a 命中；b miss 小文件；c 大文件分块。
    mockPrecheck.mockImplementation(async (_ch, files) =>
      files.map((f, i) => (i === 0 ? { sha256: f.sha256, hit: true, result: mkResult(f.sha256, f.size) } : { sha256: f.sha256, hit: false })),
    )
    mockBatch.mockImplementation(async (_ch, entries, opts) => {
      opts?.onUploadProgress?.(1) // 在途部分进度
      return entries.map((e) => mkResult(e.sha256, e.size))
    })
    mockChunked.mockImplementation(async (_ch, file, opts) => {
      opts?.onProgress?.(2, file.size)
      return mkResult('f'.repeat(64), file.size)
    })

    const big = fakeSizeFile('c.bin', AGGREGATE_MAX_FILE_BYTES + 5)
    const entries = [
      { key: 'a', file: smallFile('a.txt', 'aaaa'), label: 'a.txt' },
      { key: 'b', file: smallFile('b.txt', 'bb'), label: 'b.txt' },
      { key: 'c', file: big, label: 'c.bin' },
    ]
    const seen: number[] = []
    const out = await uploadFilesEfficient('ch-1', entries, {
      onProgress: (p) => seen.push(p.uploadedBytes),
    })

    expect(out.size).toBe(3)
    for (let i = 1; i < seen.length; i++) expect(seen[i]).toBeGreaterThanOrEqual(seen[i - 1])
    expect(seen[seen.length - 1]).toBe(4 + 2 + big.size)
  })

  it('空入参：直接返回空映射、不发请求', async () => {
    const out = await uploadFilesEfficient('ch-1', [], {})
    expect(out.size).toBe(0)
    expect(mockPrecheck).not.toHaveBeenCalled()
  })
})
