import { describe, expect, it } from 'vitest'
import type { LogEntry } from '@/lib/console/console-log-types'
import { mergeLogEntries } from './AllLogsWorkbench.merge'

const row = (id: number, source: string, time: string): LogEntry => ({
  id, source, time, level: 'info', nodeId: 1, instanceId: source === 'instance' ? 2 : 0,
  instanceUuid: '', message: 'identical text',
})

describe('mergeLogEntries', () => {
  it('sorts known times newest first, keeps unknown timestamps last and preserves same text across sources', () => {
    const items = mergeLogEntries([
      [row(1, 'worker', ''), row(2, 'worker', '2026-11-12T08:00:00Z')],
      [row(1, 'control_plane', '2026-11-12T09:00:00Z')],
      [row(1, 'instance', '2026-11-12T08:00:00Z'), row(2, 'instance', 'invalid-time')],
    ])
    expect(items).toHaveLength(5)
    expect(items.map((item) => item.source)).toEqual(['control_plane', 'worker', 'instance', 'worker', 'instance'])
    expect(items.map((item) => item.time).slice(-2)).toEqual(['', 'invalid-time'])
    expect(new Set(items.map((item) => item.id)).size).toBe(5)
  })
  it('does not mutate the source entries or arrays while resolving colliding row ids', () => {
    const sources = [[row(1, 'worker', '')], [row(1, 'instance', '')]]
    const before = structuredClone(sources)
    expect(mergeLogEntries(sources).map((item) => item.id)).toEqual([1, 2])
    expect(sources).toEqual(before)
  })
})
