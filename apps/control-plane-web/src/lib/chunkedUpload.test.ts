import { describe, it, expect } from 'vitest'
import { sliceRanges, progressBytes, type ChunkRange } from './chunkedUpload'

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
