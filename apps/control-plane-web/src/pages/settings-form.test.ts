import { describe, it, expect } from 'vitest'
import { diffSettings, hasUnsavedChanges, type DraftDiffItem } from './settings-form'

const items: DraftDiffItem[] = [
  { key: 'log.level', value: 'info' },
  { key: 'backup.retention_days', value: '7' },
]

describe('diffSettings', () => {
  it('returns no changes for an empty draft', () => {
    expect(diffSettings(items, {})).toEqual({})
  })

  it('ignores a draft value equal to the current value', () => {
    expect(diffSettings(items, { 'log.level': 'info' })).toEqual({})
  })

  it('keeps only draft values that differ from current', () => {
    expect(diffSettings(items, { 'log.level': 'warn', 'backup.retention_days': '7' })).toEqual({
      'log.level': 'warn',
    })
  })

  it('ignores draft keys not present in the editable item set', () => {
    expect(diffSettings(items, { 'unknown.key': 'x' })).toEqual({})
  })

  it('ignores undefined draft entries', () => {
    expect(diffSettings(items, { 'log.level': undefined as unknown as string })).toEqual({})
  })
})

describe('hasUnsavedChanges', () => {
  it('is false when nothing differs', () => {
    expect(hasUnsavedChanges(items, {})).toBe(false)
    expect(hasUnsavedChanges(items, { 'log.level': 'info' })).toBe(false)
  })

  it('is true when at least one value differs', () => {
    expect(hasUnsavedChanges(items, { 'log.level': 'debug' })).toBe(true)
  })
})
