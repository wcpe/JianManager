import { describe, it, expect } from 'vitest'
import {
  backupStatusKey,
  backupStatusLevel,
  isActiveStatus,
  hasActiveBackup,
  summarizeBackups,
  countDependents,
  isIncrementalChild,
  formatSizeMb,
  type BackupLike,
} from './backups-view'

const mk = (over: Partial<BackupLike>): BackupLike => ({
  id: 1,
  fileSizeMb: 0,
  mode: 0,
  status: 2,
  createdAt: '2026-06-01T00:00:00Z',
  ...over,
})

describe('backupStatusKey', () => {
  it('maps known codes', () => {
    expect(backupStatusKey(0)).toBe('pending')
    expect(backupStatusKey(1)).toBe('inProgress')
    expect(backupStatusKey(2)).toBe('completed')
    expect(backupStatusKey(3)).toBe('failed')
  })
  it('falls back to pending for unknown', () => {
    expect(backupStatusKey(99)).toBe('pending')
  })
})

describe('backupStatusLevel', () => {
  it('completed=success, failed=danger', () => {
    expect(backupStatusLevel(2)).toBe('success')
    expect(backupStatusLevel(3)).toBe('danger')
  })
  it('in-progress=info, pending=warning', () => {
    expect(backupStatusLevel(1)).toBe('info')
    expect(backupStatusLevel(0)).toBe('warning')
  })
})

describe('isActiveStatus / hasActiveBackup', () => {
  it('pending and in-progress are active', () => {
    expect(isActiveStatus(0)).toBe(true)
    expect(isActiveStatus(1)).toBe(true)
    expect(isActiveStatus(2)).toBe(false)
    expect(isActiveStatus(3)).toBe(false)
  })
  it('detects any active in list', () => {
    expect(hasActiveBackup([mk({ status: 2 }), mk({ status: 3 })])).toBe(false)
    expect(hasActiveBackup([mk({ status: 2 }), mk({ status: 1 })])).toBe(true)
    expect(hasActiveBackup([])).toBe(false)
  })
})

describe('summarizeBackups', () => {
  it('sums size, counts, finds latest success', () => {
    const s = summarizeBackups([
      mk({ id: 1, fileSizeMb: 100, status: 2, createdAt: '2026-06-01T00:00:00Z' }),
      mk({ id: 2, fileSizeMb: 50, status: 2, createdAt: '2026-06-03T00:00:00Z' }),
      mk({ id: 3, fileSizeMb: 20, status: 1, createdAt: '2026-06-05T00:00:00Z' }),
    ])
    expect(s.totalSizeMb).toBe(170)
    expect(s.count).toBe(3)
    expect(s.lastSuccessAt).toBe('2026-06-03T00:00:00Z') // 进行中的不算成功
  })
  it('ignores non-finite sizes; no success → undefined', () => {
    const s = summarizeBackups([
      mk({ fileSizeMb: Number.NaN, status: 3 }),
      mk({ fileSizeMb: 10, status: 1 }),
    ])
    expect(s.totalSizeMb).toBe(10)
    expect(s.lastSuccessAt).toBeUndefined()
  })
  it('empty list', () => {
    expect(summarizeBackups([])).toEqual({ totalSizeMb: 0, count: 0, lastSuccessAt: undefined })
  })
})

describe('countDependents', () => {
  it('counts direct incremental children of a parent', () => {
    const list = [
      mk({ id: 1, mode: 0 }),
      mk({ id: 2, mode: 1, parentId: 1 }),
      mk({ id: 3, mode: 1, parentId: 1 }),
      mk({ id: 4, mode: 1, parentId: 2 }),
    ]
    expect(countDependents(list, 1)).toBe(2)
    expect(countDependents(list, 2)).toBe(1)
    expect(countDependents(list, 4)).toBe(0)
  })
})

describe('isIncrementalChild', () => {
  it('true only for incremental with parent', () => {
    expect(isIncrementalChild(mk({ mode: 1, parentId: 5 }))).toBe(true)
    expect(isIncrementalChild(mk({ mode: 1, parentId: undefined }))).toBe(false)
    expect(isIncrementalChild(mk({ mode: 0, parentId: 5 }))).toBe(false)
  })
})

describe('formatSizeMb', () => {
  it('MB under 1024, GB above', () => {
    expect(formatSizeMb(0)).toBe('0.0 MB')
    expect(formatSizeMb(512.34)).toBe('512.3 MB')
    expect(formatSizeMb(2048)).toBe('2.0 GB')
  })
  it('guards negatives / non-finite', () => {
    expect(formatSizeMb(-5)).toBe('0.0 MB')
    expect(formatSizeMb(Number.NaN)).toBe('0.0 MB')
  })
})
