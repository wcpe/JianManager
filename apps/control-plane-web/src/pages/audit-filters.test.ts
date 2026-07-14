import { describe, it, expect } from 'vitest'
import {
  DEFAULT_AUDIT_FILTER,
  toAuditParams,
  toRFC3339,
  formatAuditDetail,
  auditRowsToNDJSON,
  type AuditFilterState,
} from './audit-filters'
import type { AuditLogInfo } from '@/api/audit'

function state(overrides: Partial<AuditFilterState> = {}): AuditFilterState {
  return { ...DEFAULT_AUDIT_FILTER, ...overrides }
}

describe('toRFC3339', () => {
  it('returns undefined for empty input', () => {
    expect(toRFC3339('')).toBeUndefined()
  })

  it('returns undefined for an unparseable value', () => {
    expect(toRFC3339('not-a-date')).toBeUndefined()
  })

  it('converts a datetime-local value to RFC3339 with timezone', () => {
    const out = toRFC3339('2026-06-22T10:30')
    // datetime-local 是本地时间；toISOString 会输出 Z 结尾的 RFC3339 字符串。
    expect(out).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z$/)
    // 再解析时应回到同一个时间点。
    expect(new Date(out as string).getTime()).toBe(new Date('2026-06-22T10:30').getTime())
  })
})

describe('toAuditParams', () => {
  it('omits every dimension for the default (empty) filter', () => {
    expect(toAuditParams(DEFAULT_AUDIT_FILTER)).toEqual({})
  })

  it('omits blank text fields', () => {
    expect(toAuditParams(state({ action: '   ', targetType: '' }))).toEqual({})
  })

  it('keeps set text fields trimmed', () => {
    expect(toAuditParams(state({ action: ' instance.start ', targetType: 'instance' }))).toEqual({
      action: 'instance.start',
      targetType: 'instance',
    })
  })

  it('parses a positive integer userId and drops invalid ones', () => {
    expect(toAuditParams(state({ userId: '7' })).userId).toBe(7)
    expect(toAuditParams(state({ userId: '0' })).userId).toBeUndefined()
    expect(toAuditParams(state({ userId: 'abc' })).userId).toBeUndefined()
    expect(toAuditParams(state({ userId: '-3' })).userId).toBeUndefined()
  })

  it('converts from/to into RFC3339 query values', () => {
    const params = toAuditParams(state({ from: '2026-06-01T00:00', to: '2026-06-22T23:59' }))
    expect(params.from).toBe(new Date('2026-06-01T00:00').toISOString())
    expect(params.to).toBe(new Date('2026-06-22T23:59').toISOString())
  })
})

describe('formatAuditDetail', () => {
  it('returns empty string for blank detail', () => {
    expect(formatAuditDetail('')).toBe('')
    expect(formatAuditDetail('   ')).toBe('')
    expect(formatAuditDetail(undefined)).toBe('')
  })

  it('pretty-prints valid JSON with 2-space indent', () => {
    expect(formatAuditDetail('{"a":1,"b":"x"}')).toBe('{\n  "a": 1,\n  "b": "x"\n}')
  })

  it('returns the raw string verbatim when not JSON', () => {
    expect(formatAuditDetail('plain text reason')).toBe('plain text reason')
  })
})

describe('auditRowsToNDJSON', () => {
  const rows: AuditLogInfo[] = [
    {
      id: 1,
      uuid: 'u1',
      userId: 7,
      action: 'instance.start',
      targetType: 'instance',
      targetId: '12',
      detail: '{"name":"srv"}',
      ip: '10.0.0.1',
      createdAt: '2026-06-26T12:00:00Z',
      user: { id: 7, username: 'admin' },
    },
    {
      id: 2,
      uuid: 'u2',
      userId: 7,
      action: 'instance.stop',
      targetType: 'instance',
      targetId: '12',
      detail: '',
      ip: '10.0.0.1',
      createdAt: '2026-06-26T12:01:00Z',
    },
  ]

  it('emits one JSON object per line', () => {
    const out = auditRowsToNDJSON(rows)
    const lines = out.split('\n')
    expect(lines).toHaveLength(2)
    expect(JSON.parse(lines[0]).action).toBe('instance.start')
    expect(JSON.parse(lines[1]).action).toBe('instance.stop')
  })

  it('flattens username and keeps core fields', () => {
    const first = JSON.parse(auditRowsToNDJSON(rows).split('\n')[0])
    expect(first.username).toBe('admin')
    expect(first.id).toBe(1)
    expect(first.targetType).toBe('instance')
    expect(first.ip).toBe('10.0.0.1')
  })

  it('returns empty string for no rows', () => {
    expect(auditRowsToNDJSON([])).toBe('')
  })
})
