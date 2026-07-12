import { describe, it, expect } from 'vitest'
import type { ArchiveSummary, DirUsage } from '@/api/storage'
import {
  formatBytes,
  deriveArchive,
  sortDirsByUsage,
  buildCrumbs,
  joinStoragePath,
} from './storage-view'

function dir(p: Partial<DirUsage>): DirUsage {
  return {
    path: 'var/artifacts',
    label: 'artifacts',
    size: 0,
    fileCount: 0,
    exists: true,
    clearable: false,
    ...p,
  }
}

describe('formatBytes', () => {
  it('formats zero and non-positive as 0 B', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(-100)).toBe('0 B')
    expect(formatBytes(Number.NaN)).toBe('0 B')
    expect(formatBytes(Number.POSITIVE_INFINITY)).toBe('0 B')
  })

  it('scales through binary units', () => {
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(1024)).toBe('1 KB')
    expect(formatBytes(1536)).toBe('1.5 KB')
    expect(formatBytes(1024 * 1024)).toBe('1 MB')
    expect(formatBytes(1024 * 1024 * 1024)).toBe('1 GB')
  })

  it('uses integer for values >= 100', () => {
    expect(formatBytes(150 * 1024)).toBe('150 KB')
  })
})

describe('deriveArchive', () => {
  it('aggregates total / cold count / cold size', () => {
    const a: ArchiveSummary = {
      hotCount: 3,
      archivedCount: 2,
      externalCount: 1,
      hotSize: 1000,
      archivedSize: 2000,
      externalSize: 4000,
    }
    expect(deriveArchive(a)).toEqual({ total: 6, cold: 3, coldSize: 6000 })
  })

  it('handles all-zero', () => {
    const a: ArchiveSummary = {
      hotCount: 0,
      archivedCount: 0,
      externalCount: 0,
      hotSize: 0,
      archivedSize: 0,
      externalSize: 0,
    }
    expect(deriveArchive(a)).toEqual({ total: 0, cold: 0, coldSize: 0 })
  })
})

describe('sortDirsByUsage', () => {
  it('puts existing dirs first (by size desc), missing last', () => {
    const dirs = [
      dir({ path: 'cache', size: 10, exists: true }),
      dir({ path: 'bin', size: 0, exists: false }),
      dir({ path: 'var/artifacts', size: 100, exists: true }),
      dir({ path: 'etc', size: 0, exists: false }),
    ]
    const sorted = sortDirsByUsage(dirs)
    expect(sorted.map((d) => d.path)).toEqual(['var/artifacts', 'cache', 'bin', 'etc'])
  })

  it('does not mutate input', () => {
    const dirs = [dir({ path: 'a', size: 1 }), dir({ path: 'b', size: 2 })]
    const before = dirs.map((d) => d.path)
    sortDirsByUsage(dirs)
    expect(dirs.map((d) => d.path)).toEqual(before)
  })
})

describe('buildCrumbs', () => {
  it('returns only root for empty path', () => {
    expect(buildCrumbs('', 'root')).toEqual([{ name: 'root', path: '' }])
  })

  it('accumulates nested segments', () => {
    expect(buildCrumbs('var/artifacts/core', 'root')).toEqual([
      { name: 'root', path: '' },
      { name: 'var', path: 'var' },
      { name: 'artifacts', path: 'var/artifacts' },
      { name: 'core', path: 'var/artifacts/core' },
    ])
  })

  it('trims surrounding and collapses empty segments', () => {
    expect(buildCrumbs('/var//log/', 'root')).toEqual([
      { name: 'root', path: '' },
      { name: 'var', path: 'var' },
      { name: 'log', path: 'var/log' },
    ])
  })
})

describe('joinStoragePath', () => {
  it('returns name when parent is root', () => {
    expect(joinStoragePath('', 'var')).toBe('var')
  })

  it('joins with slash otherwise', () => {
    expect(joinStoragePath('var', 'log')).toBe('var/log')
  })
})
