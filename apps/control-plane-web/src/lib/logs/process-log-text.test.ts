import { describe, expect, it } from 'vitest'
import { filterProcessLogs, processLogEntries } from './process-log-text'

describe('process log text snapshots', () => {
  it('keeps text, parses both slog formats, and leaves missing metadata unknown', () => {
    const rows = processLogEntries('time=2026-11-11T12:00:00Z level=WARN msg="slow"\n{"time":"2026-11-11T12:00:01Z","level":"ERROR","msg":"failed"}\n  at Example.run(Example.java:1)\n', 'worker', 3)
    expect(rows.map((row) => row.level)).toEqual(['unknown', 'error', 'warn'])
    expect(rows[0].time).toBe('')
    expect(rows[0].message).toBe('  at Example.run(Example.java:1)')
    expect(rows.every((row) => row.nodeId === 3 && row.source === 'worker')).toBe(true)
  })
  it('searches only the loaded window with literal text, not LogsQL', () => {
    const rows = processLogEntries('level=INFO msg="a OR b"\nlevel=ERROR msg="warning"', 'control_plane')
    expect(filterProcessLogs(rows, 'A OR B', '')).toHaveLength(1)
    expect(filterProcessLogs(rows, '', 'warn')).toHaveLength(0)
    expect(filterProcessLogs(rows, 'warn', 'error')).toHaveLength(1)
  })
})
