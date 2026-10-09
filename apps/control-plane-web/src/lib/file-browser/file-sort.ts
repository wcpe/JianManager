/**
 * 文件列表排序（FR-375）：目录优先 + 列头键升/降序。
 */

export type FileSortKey = 'name' | 'modTime' | 'size' | 'type' | 'perm'

export interface FileSortState {
  key: FileSortKey
  /** true=升序 */
  asc: boolean
}

export interface SortableFile {
  name: string
  isDir: boolean
  size: number
  modTime: number
  modeString?: string
  writable?: boolean
}

export const DEFAULT_FILE_SORT: FileSortState = { key: 'name', asc: true }

function typeLabel(f: SortableFile): string {
  if (f.isDir) return ''
  const i = f.name.lastIndexOf('.')
  return i >= 0 ? f.name.slice(i + 1).toLowerCase() : ''
}

function permKey(f: SortableFile): string {
  return f.modeString || (f.writable === false ? '0' : '1')
}

/** 稳定排序：目录始终在前，再按 key。 */
export function sortFiles<T extends SortableFile>(files: T[], sort: FileSortState): T[] {
  const mul = sort.asc ? 1 : -1
  return [...files].sort((a, b) => {
    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1
    let cmp = 0
    switch (sort.key) {
      case 'name':
        cmp = a.name.localeCompare(b.name, undefined, { sensitivity: 'base' })
        break
      case 'modTime':
        cmp = a.modTime - b.modTime
        break
      case 'size':
        cmp = a.size - b.size
        break
      case 'type':
        cmp = typeLabel(a).localeCompare(typeLabel(b))
        break
      case 'perm':
        cmp = permKey(a).localeCompare(permKey(b))
        break
    }
    if (cmp !== 0) return cmp * mul
    return a.name.localeCompare(b.name) * mul
  })
}

/** 点击列头：同键翻转方向，否则新键默认升序。 */
export function toggleSort(prev: FileSortState, key: FileSortKey): FileSortState {
  if (prev.key === key) return { key, asc: !prev.asc }
  return { key, asc: true }
}

const VIEW_KEY = 'jm.files.view'
const SORT_KEY = 'jm.files.sort'

export type FileViewMode = 'details' | 'list' | 'icons'

export function loadViewMode(): FileViewMode {
  try {
    const v = localStorage.getItem(VIEW_KEY)
    if (v === 'list' || v === 'icons' || v === 'details') return v
  } catch {
    /* ignore */
  }
  return 'details'
}

export function saveViewMode(mode: FileViewMode): void {
  try {
    localStorage.setItem(VIEW_KEY, mode)
  } catch {
    /* ignore */
  }
}

export function loadSortState(): FileSortState {
  try {
    const raw = localStorage.getItem(SORT_KEY)
    if (!raw) return DEFAULT_FILE_SORT
    const p = JSON.parse(raw) as FileSortState
    if (p && typeof p.key === 'string' && typeof p.asc === 'boolean') return p
  } catch {
    /* ignore */
  }
  return DEFAULT_FILE_SORT
}

export function saveSortState(sort: FileSortState): void {
  try {
    localStorage.setItem(SORT_KEY, JSON.stringify(sort))
  } catch {
    /* ignore */
  }
}
