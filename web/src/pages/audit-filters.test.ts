import { describe, it, expect } from 'vitest'
import {
  DEFAULT_AUDIT_FILTER,
  toAuditParams,
  toRFC3339,
  type AuditFilterState,
} from './audit-filters'

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
    // datetime-local is local time; toISOString yields a Z-suffixed RFC3339 string.
    expect(out).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z$/)
    // Round-trips back to the same instant the local string referenced.
    expect(new Date(out as string).getTime()).toBe(new Date('2026-06-22T10:30').getTime())
  })
})

describe('toAuditParams', () => {
  it('omits every dimension for the default (empty) filter', () => {
    expect(toAuditParams(DEFAULT_AUDIT_FILTER)).toEqual({ limit: 100 })
  })

  it('omits blank text fields', () => {
    expect(toAuditParams(state({ action: '   ', targetType: '' }))).toEqual({ limit: 100 })
  })

  it('keeps set text fields trimmed', () => {
    expect(toAuditParams(state({ action: ' instance.start ', targetType: 'instance' }))).toEqual({
      action: 'instance.start',
      targetType: 'instance',
      limit: 100,
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

  it('omits the limit when non-positive', () => {
    expect(toAuditParams(state({ limit: 0 }))).toEqual({})
  })

  it('carries an enlarged limit through (load-more)', () => {
    expect(toAuditParams(state({ limit: 300 })).limit).toBe(300)
  })
})
