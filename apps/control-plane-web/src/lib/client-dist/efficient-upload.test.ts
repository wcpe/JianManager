import { describe, it, expect, vi, beforeEach } from 'vitest'
import { uploadFilesEfficient } from './efficient-upload'
import type {
  EfficientPrecheckEntry,
  EfficientPrecheckResult,
  EfficientUploadDeps,
  EfficientUploadProgress,
} from './efficient-upload'
import type { ChunkUploadResult } from './chunked-upload'
import { AGGREGATE_MAX_FILE_BYTES, HASH_MAX_FILE_BYTES } from './client-upload-plan'

/**
 * FR-346 上传编排器（`efficient-upload.ts` 纯逻辑）：预查命中跳过上传 / miss 小文件聚合 /
 * 大文件分块 / 超大免预查 / 预查失败降级 / fail-fast / 进度单调。
 *
 * 三个取数依赖经 `EfficientUploadDeps` 直接注入替身，**不 mock `@/api/*`**——被测的就是
 * 编排本身。api 接线的正确性（真实 precheckClientFiles / uploadClientFilesBatch /
 * uploadFileChunked 是否被注入位）由 `client-upload-api.test.ts` 覆盖。
 */

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

function mkResult(sha256: string, size: number): ChunkUploadResult {
  return { sha256, md5: 'md5-' + sha256.slice(0, 8), size, codec: 'none' }
}

/** 按请求回显全命中的预查结果。 */
function allHit(files: EfficientPrecheckEntry[]): EfficientPrecheckResult[] {
  return files.map((f) => ({ sha256: f.sha256, hit: true, result: mkResult(f.sha256, f.size) }))
}

/** 按请求回显全未命中的预查结果。 */
function allMiss(files: EfficientPrecheckEntry[]): EfficientPrecheckResult[] {
  return files.map((f) => ({ sha256: f.sha256, hit: false }))
}

// 依赖替身：签名取自 EfficientUploadDeps，参数显式标注以保类型（不得用 any）。
// 默认实现按入参回显，用例内可再覆盖。
type ChunkedOpts = Parameters<EfficientUploadDeps['uploadChunked']>[2]

const mockPrecheck = vi.fn(async (_channelId: string, query: EfficientPrecheckEntry[]) =>
  // 默认全未命中（要让某个文件命中，用例内覆盖实现）。
  query.map<EfficientPrecheckResult>(() => ({ hit: false })),
)
// 第 3 位 options 本替身用不上，但必须留在这个元组签名里——用例要经 `mock.calls`
// 观察 onUploadProgress / signal 是如何被编排器传下去的。
const mockBatch = vi.fn(async (...args: Parameters<EfficientUploadDeps['uploadBatch']>) =>
  args[1].map((e) => mkResult(e.sha256, e.size)),
)
const mockChunked = vi.fn(async (_channelId: string, file: File, opts?: ChunkedOpts) =>
  // 默认回显编排器透传的 expectedSha256（无则给个固定值），便于断言强校验链路。
  mkResult(opts?.expectedSha256 ?? 'e'.repeat(64), file.size),
)

/** 每次调用现取替身，避免用例间互相污染。 */
function deps(): EfficientUploadDeps {
  return { precheck: mockPrecheck, uploadBatch: mockBatch, uploadChunked: mockChunked }
}

beforeEach(() => {
  // 只清调用记录与实例，保留上面各 mock 的默认实现（用例内的实现覆盖在用例内设置）。
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
    const out = await uploadFilesEfficient(deps(), 'ch-1', entries, {
      onProgress: (p) => events.push({ ...p }),
    })

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

    const entries = [
      { key: 'a', file: smallFile('a.txt', 'aaa'), label: 'a.txt' },
      { key: 'b', file: smallFile('b.txt', 'bb'), label: 'b.txt' },
    ]
    const out = await uploadFilesEfficient(deps(), 'ch-1', entries, {})

    expect(mockBatch).toHaveBeenCalledTimes(1)
    const sent = mockBatch.mock.calls[0][1]
    expect(sent.map((e) => e.filename)).toEqual(['a.txt', 'b.txt'])
    expect(sent[0].sha256).toMatch(/^[0-9a-f]{64}$/)
    expect(mockChunked).not.toHaveBeenCalled()
    expect(out.size).toBe(2)
  })

  it('HTTP 非安全上下文无 WebCrypto：小文件用 JS hash 兜底，仍走预查与聚合', async () => {
    const originalCrypto = globalThis.crypto
    vi.stubGlobal('crypto', {})
    mockPrecheck.mockImplementation(async (_ch, files) => allMiss(files))

    try {
      const out = await uploadFilesEfficient(
        deps(),
        'ch-1',
        [
          { key: 'a', file: smallFile('a.txt', 'aaa'), label: 'a.txt' },
          { key: 'b', file: smallFile('b.txt', 'bb'), label: 'b.txt' },
        ],
        {},
      )

      expect(mockPrecheck).toHaveBeenCalledTimes(1)
      expect(mockPrecheck.mock.calls[0][1].every((entry) => /^[0-9a-f]{64}$/.test(entry.sha256))).toBe(true)
      expect(mockBatch).toHaveBeenCalledTimes(1)
      expect(mockChunked).not.toHaveBeenCalled()
      expect(out.size).toBe(2)
    } finally {
      vi.stubGlobal('crypto', originalCrypto)
    }
  })

  it('大文件（>8MiB）走分块并携带 expectedSha256；不入聚合', async () => {
    mockPrecheck.mockImplementation(async (_ch, files) => allMiss(files))

    const big = fakeSizeFile('big.jar', AGGREGATE_MAX_FILE_BYTES + 1)
    const out = await uploadFilesEfficient(deps(), 'ch-1', [{ key: 'big', file: big, label: 'big.jar' }], {})

    expect(mockBatch).not.toHaveBeenCalled()
    expect(mockChunked).toHaveBeenCalledTimes(1)
    // ≤256MiB 的大文件仍算 hash：complete 顺带 expectedSha256 强校验。
    expect(mockChunked.mock.calls[0][2]?.expectedSha256).toMatch(/^[0-9a-f]{64}$/)
    expect(out.get('big')).toBeDefined()
  })

  it('超大文件（>256MiB）不 hash、不进预查，直接分块（无 expectedSha256）', async () => {
    mockPrecheck.mockImplementation(async (_ch, files) => allMiss(files))

    const huge = fakeSizeFile('huge.bin', HASH_MAX_FILE_BYTES + 1)
    const small = smallFile('s.txt', 'ss')

    await uploadFilesEfficient(
      deps(),
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

    const out = await uploadFilesEfficient(
      deps(),
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
      uploadFilesEfficient(
        deps(),
        'ch-1',
        [{ key: 'a', file: smallFile('a.txt', 'x'), label: 'a.txt' }],
        {},
      ),
    ).rejects.toBe(boom)
  })

  it('取消（signal.aborted）在 hash 阶段即抛 AbortError，不发任何请求', async () => {
    const ac = new AbortController()
    ac.abort()
    await expect(
      uploadFilesEfficient(
        deps(),
        'ch-1',
        [{ key: 'a', file: smallFile('a.txt', 'x'), label: 'a.txt' }],
        { signal: ac.signal },
      ),
    ).rejects.toMatchObject({ name: 'AbortError' })
    expect(mockPrecheck).not.toHaveBeenCalled()
    expect(mockBatch).not.toHaveBeenCalled()
  })

  it('混合场景进度单调不倒退、终值等于总字节', async () => {
    // a 命中；b miss 小文件；c 大文件分块。
    mockPrecheck.mockImplementation(async (_ch, files) =>
      files.map((f, i) =>
        i === 0
          ? { sha256: f.sha256, hit: true, result: mkResult(f.sha256, f.size) }
          : { sha256: f.sha256, hit: false },
      ),
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
    const out = await uploadFilesEfficient(deps(), 'ch-1', entries, {
      onProgress: (p) => seen.push(p.uploadedBytes),
    })

    expect(out.size).toBe(3)
    for (let i = 1; i < seen.length; i++) expect(seen[i]).toBeGreaterThanOrEqual(seen[i - 1])
    expect(seen[seen.length - 1]).toBe(4 + 2 + big.size)
  })

  it('空入参：直接返回空映射、不发请求', async () => {
    const out = await uploadFilesEfficient(deps(), 'ch-1', [], {})
    expect(out.size).toBe(0)
    expect(mockPrecheck).not.toHaveBeenCalled()
  })
})
