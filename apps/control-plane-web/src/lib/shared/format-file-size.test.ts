import { describe, expect, it } from 'vitest'
import { formatFileSize } from './format-file-size'

describe('formatFileSize：字节档与各档进位', () => {
  it('小于 1KB 直接输出字节数', () => {
    expect(formatFileSize(1)).toBe('1 B')
    expect(formatFileSize(512)).toBe('512 B')
    expect(formatFileSize(1023)).toBe('1023 B')
  })

  it('非整数字节保留 1 位小数', () => {
    expect(formatFileSize(0.5)).toBe('0.5 B')
    expect(formatFileSize(1023.5)).toBe('1023.5 B')
  })

  it('各档进位边界（1024 的整数次幂起跳，不四舍五入到下一档）', () => {
    expect(formatFileSize(1024)).toBe('1.0 KB')
    expect(formatFileSize(1024 * 1024 - 1)).toBe('1024.0 KB')
    expect(formatFileSize(1024 * 1024)).toBe('1.0 MB')
    expect(formatFileSize(1024 ** 3 - 1)).toBe('1024.0 MB')
    expect(formatFileSize(1024 ** 3)).toBe('1.0 GB')
    expect(formatFileSize(1024 ** 4 - 1)).toBe('1024.0 GB')
    expect(formatFileSize(1024 ** 4)).toBe('1.0 TB')
  })

  it('2 GiB 走 GB 档而非退化成 2048.0 MB（本次收敛修掉的真实缺陷）', () => {
    expect(formatFileSize(2 * 1024 ** 3)).toBe('2.0 GB')
    expect(formatFileSize(2.5 * 1024 ** 3)).toBe('2.5 GB')
    expect(formatFileSize(1536 * 1024)).toBe('1.5 MB')
  })

  it('超过 TB 停在 TB 档，不退化成裸数字', () => {
    expect(formatFileSize(2048 * 1024 ** 4)).toBe('2048.0 TB')
  })

  it('整数值也保留 1 位小数（原 storage-view / runtime-assets 的「整数省 .0」写法已收敛）', () => {
    expect(formatFileSize(150 * 1024)).toBe('150.0 KB')
    expect(formatFileSize(5 * 1024 * 1024)).toBe('5.0 MB')
    expect(formatFileSize(8 * 1024 ** 3)).toBe('8.0 GB')
  })
})

describe('formatFileSize：0 / 负值 / 空值 / 非有限数', () => {
  it('缺省占位为 0 B', () => {
    expect(formatFileSize(0)).toBe('0 B')
    expect(formatFileSize(-1)).toBe('0 B')
    expect(formatFileSize(-(1024 ** 3))).toBe('0 B')
    expect(formatFileSize(null)).toBe('0 B')
    expect(formatFileSize(undefined)).toBe('0 B')
    expect(formatFileSize(Number.NaN)).toBe('0 B')
    expect(formatFileSize(Number.POSITIVE_INFINITY)).toBe('0 B')
    expect(formatFileSize(Number.NEGATIVE_INFINITY)).toBe('0 B')
  })

  it('fallback 覆盖占位文案，且不影响有效值的输出', () => {
    expect(formatFileSize(0, { fallback: '--' })).toBe('--')
    expect(formatFileSize(undefined, { fallback: '—' })).toBe('—')
    expect(formatFileSize(Number.NaN, { fallback: '0' })).toBe('0')
    expect(formatFileSize(-5, { fallback: '—' })).toBe('—')
    // 有效值不受 fallback 影响
    expect(formatFileSize(1024, { fallback: '--' })).toBe('1.0 KB')
    expect(formatFileSize(512, { fallback: '--' })).toBe('512 B')
  })
})

describe('formatFileSize：单位后缀统一为 KB/MB/GB/TB', () => {
  it('不使用 KiB/MiB/GiB 后缀', () => {
    const out = [1024, 1024 ** 2, 1024 ** 3, 1024 ** 4].map((v) => formatFileSize(v))
    expect(out).toEqual(['1.0 KB', '1.0 MB', '1.0 GB', '1.0 TB'])
    expect(out.some((s) => /iB\b/.test(s))).toBe(false)
  })
})
