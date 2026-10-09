import { describe, it, expect } from 'vitest'
import { nearestRowIndex, hoverSnapshotAt, type SampleRow } from './chart-hover'

const rows: SampleRow[] = [
  { ts: '2026-06-26T00:00:00Z', cpu: 10, mem: 20 },
  { ts: '2026-06-26T01:00:00Z', cpu: 30, mem: null },
  { ts: '2026-06-26T02:00:00Z', cpu: 50, mem: 60 },
]

describe('nearestRowIndex', () => {
  it('精确命中返回该行', () => {
    expect(nearestRowIndex(rows, '2026-06-26T01:00:00Z')).toBe(1)
  })
  it('落在两点之间取更近一侧', () => {
    // 00:50 离 01:00 更近
    expect(nearestRowIndex(rows, '2026-06-26T00:50:00Z')).toBe(1)
    // 00:20 离 00:00 更近
    expect(nearestRowIndex(rows, '2026-06-26T00:20:00Z')).toBe(0)
  })
  it('等距并列取左', () => {
    // 00:30 距 00:00 与 01:00 等距
    expect(nearestRowIndex(rows, '2026-06-26T00:30:00Z')).toBe(0)
  })
  it('超出范围夹到端点', () => {
    expect(nearestRowIndex(rows, '2026-06-26T09:00:00Z')).toBe(2)
    expect(nearestRowIndex(rows, '2020-01-01T00:00:00Z')).toBe(0)
  })
  it('空数组返回 -1', () => {
    expect(nearestRowIndex([], '2026-06-26T00:00:00Z')).toBe(-1)
  })
})

describe('hoverSnapshotAt', () => {
  const series = [
    { key: 'cpu', name: 'CPU' },
    { key: 'mem', name: '内存' },
  ]
  it('取最近时刻各序列值', () => {
    const snap = hoverSnapshotAt(rows, series, '2026-06-26T02:00:00Z')
    expect(snap).not.toBeNull()
    expect(snap!.ts).toBe('2026-06-26T02:00:00Z')
    expect(snap!.entries).toEqual([
      { key: 'cpu', name: 'CPU', value: 50 },
      { key: 'mem', name: '内存', value: 60 },
    ])
  })
  it('缺测值映射为 null', () => {
    const snap = hoverSnapshotAt(rows, series, '2026-06-26T01:00:00Z')
    expect(snap!.entries.find((e) => e.key === 'mem')!.value).toBeNull()
  })
  it('序列不存在于行中时为 null', () => {
    const snap = hoverSnapshotAt(rows, [{ key: 'ghost', name: '幽灵' }], '2026-06-26T00:00:00Z')
    expect(snap!.entries[0].value).toBeNull()
  })
  it('空行返回 null', () => {
    expect(hoverSnapshotAt([], series, '2026-06-26T00:00:00Z')).toBeNull()
  })
})
