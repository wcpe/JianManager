import { describe, it, expect } from 'vitest'
import { formatCacheBytes, capGiBToBytes, capBytesToGiB, describeCap } from './artifact-cache'

describe('formatCacheBytes', () => {
  it('0 或非有限值回 0 B', () => {
    expect(formatCacheBytes(0)).toBe('0 B')
    expect(formatCacheBytes(-5)).toBe('0 B')
    expect(formatCacheBytes(NaN)).toBe('0 B')
  })
  it('按量级带单位', () => {
    expect(formatCacheBytes(512)).toBe('512 B')
    expect(formatCacheBytes(1024)).toBe('1.0 KB')
    expect(formatCacheBytes(1536)).toBe('1.5 KB')
    expect(formatCacheBytes(1024 * 1024)).toBe('1.0 MB')
    expect(formatCacheBytes(1024 * 1024 * 1024)).toBe('1.0 GB')
  })
})

describe('capGiBToBytes', () => {
  it('GB → 字节', () => {
    expect(capGiBToBytes(1)).toBe(1024 * 1024 * 1024)
    expect(capGiBToBytes('2')).toBe(2 * 1024 * 1024 * 1024)
    expect(capGiBToBytes('1.5')).toBe(Math.round(1.5 * 1024 * 1024 * 1024))
  })
  it('空/非法/<=0 视为不限（0）', () => {
    expect(capGiBToBytes('')).toBe(0)
    expect(capGiBToBytes('abc')).toBe(0)
    expect(capGiBToBytes(0)).toBe(0)
    expect(capGiBToBytes(-3)).toBe(0)
  })
})

describe('capBytesToGiB', () => {
  it('字节 → GB（去尾随 0）', () => {
    expect(capBytesToGiB(1024 * 1024 * 1024)).toBe('1')
    expect(capBytesToGiB(1.5 * 1024 * 1024 * 1024)).toBe('1.5')
  })
  it('0/非法回空串', () => {
    expect(capBytesToGiB(0)).toBe('')
    expect(capBytesToGiB(NaN)).toBe('')
  })
  it('round-trip 稳定', () => {
    const bytes = capGiBToBytes('4')
    expect(capBytesToGiB(bytes)).toBe('4')
  })
})

describe('describeCap', () => {
  it('0 = 不限', () => {
    expect(describeCap(0)).toBe('不限')
  })
  it('非 0 格式化字节', () => {
    expect(describeCap(1024 * 1024 * 1024)).toBe('1.0 GB')
  })
})
