import { describe, expect, it, vi } from 'vitest'
import type { LogEntry } from '@/lib/console/console-log-types'
import { buildLogSnapshot, downloadLogSnapshot, type LogSnapshotWindow } from './log-snapshot-export'

const windows: LogSnapshotWindow[] = [
  {
    key: 'worker:7',
    label: 'Node 7',
    source: 'worker',
    status: 'ready',
    loadedRows: 2,
    limit: 100,
    capturedAt: '2026-11-12T10:00:00Z',
    truncated: true,
    note: 'bounded tail',
  },
  {
    key: 'instance:3',
    label: 'Instance 3',
    source: 'instance',
    status: 'failed',
    loadedRows: 0,
    limit: 100,
    from: '2026-11-12T09:00:00Z',
    to: '2026-11-12T10:00:00Z',
    note: '429 rate limited; retry available',
  },
]

const item = (overrides: Partial<LogEntry>): LogEntry => ({
  id: 1,
  source: 'worker',
  level: 'info',
  instanceId: 0,
  instanceUuid: '',
  nodeId: 7,
  message: 'same text',
  time: '',
  ...overrides,
})

describe('buildLogSnapshot', () => {
  it('writes bounded metadata first and preserves unknown time and same text from different sources', () => {
    const text = buildLogSnapshot([
      item({ source: 'worker', nodeId: 7 }),
      item({ source: 'instance', nodeId: 7, instanceId: 3, fields: { trace: 'a' } }),
    ], windows, '2026-11-12T10:01:00Z')
    const lines = text.trimEnd().split('\n').map((line) => JSON.parse(line) as Record<string, unknown>)

    expect(lines[0]).toMatchObject({
      type: 'window_metadata',
      scope: 'loaded_window_only',
      complete_history: false,
      exportedAt: '2026-11-12T10:01:00Z',
    })
    expect(lines[0].windows).toEqual(windows)
    expect(lines.slice(1)).toEqual([
      expect.objectContaining({ type: 'log', source: 'worker', message: 'same text', time: '' }),
      expect.objectContaining({ type: 'log', source: 'instance', message: 'same text', time: '', fields: { trace: 'a' } }),
    ])
    expect(lines).toHaveLength(3)
  })
})

describe('downloadLogSnapshot', () => {
  it('does not download empty snapshots and revokes the object URL', () => {
    const anchor = { href: '', download: '', click: vi.fn() }
    vi.stubGlobal('document', { createElement: () => anchor })
    const createObjectURL = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:test')
    const revokeObjectURL = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => undefined)

    downloadLogSnapshot([], windows)
    expect(createObjectURL).not.toHaveBeenCalled()
    expect(anchor.click).not.toHaveBeenCalled()

    downloadLogSnapshot([item({})], windows, 'current')
    expect(createObjectURL).toHaveBeenCalledOnce()
    expect(anchor.click).toHaveBeenCalledOnce()
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:test')
    vi.unstubAllGlobals()
  })
})
