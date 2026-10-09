import { describe, it, expect } from 'vitest'
import { sortFiles, toggleSort, DEFAULT_FILE_SORT, type SortableFile } from './file-sort'

const sample: SortableFile[] = [
  { name: 'b.txt', isDir: false, size: 20, modTime: 2, modeString: 'rw-r--r--', writable: true },
  { name: 'plugins', isDir: true, size: 0, modTime: 1 },
  { name: 'a.txt', isDir: false, size: 10, modTime: 3, modeString: 'r--r--r--', writable: false },
]

describe('file-sort（FR-375）', () => {
  it('目录优先再按名', () => {
    const out = sortFiles(sample, { key: 'name', asc: true })
    expect(out.map((f) => f.name)).toEqual(['plugins', 'a.txt', 'b.txt'])
  })

  it('按 size 降序', () => {
    const out = sortFiles(sample, { key: 'size', asc: false })
    expect(out[0].name).toBe('plugins')
    expect(out[1].name).toBe('b.txt')
    expect(out[2].name).toBe('a.txt')
  })

  it('toggleSort 同键翻转', () => {
    const s = toggleSort(DEFAULT_FILE_SORT, 'name')
    expect(s).toEqual({ key: 'name', asc: false })
    expect(toggleSort(s, 'size')).toEqual({ key: 'size', asc: true })
  })
})
