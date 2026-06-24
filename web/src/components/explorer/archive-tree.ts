import type { ArchiveEntry } from '@/api/archive'

/** 归档内部条目构成的树节点（懒构建，仅展示用）。 */
export interface EntryNode {
  /** 显示名（路径最后一段）。 */
  label: string
  /** 归档内完整条目名（目录以「/」结尾；文件无尾斜杠）。 */
  fullPath: string
  isDir: boolean
  size: number
  children: EntryNode[]
}

/**
 * 把扁平条目列表按「/」重建为树（目录隐式补全）。
 *
 * zip 常同时包含目录条目（`io/`）与其下文件条目（`io/papermc/...`）。两者
 * 必须复用同一目录节点，否则同名顶级文件夹会重复（BUG-010）。故 `dirCache`
 * 一律以**去尾斜杠的路径段**为键：目录条目与文件路径派生的中间目录都归一到
 * 同一个键，保证每个路径只建一个节点。
 *
 * 抽到独立模块（非组件文件）以满足 react-refresh/only-export-components。
 */
export function buildEntryTree(entries: ArchiveEntry[]): EntryNode[] {
  const root: EntryNode = { label: '', fullPath: '', isDir: true, size: 0, children: [] }
  const dirCache = new Map<string, EntryNode>([['', root]])

  // dirPath 为去尾斜杠的目录路径（'' 表示根）。按此归一键查/建节点，避免
  // 'io/' 与 'io' 被当作两个不同目录。
  const ensureDir = (dirPath: string): EntryNode => {
    const key = dirPath.replace(/\/$/, '')
    const cached = dirCache.get(key)
    if (cached) return cached
    const slash = key.lastIndexOf('/')
    const parentPath = slash >= 0 ? key.slice(0, slash) : ''
    const label = slash >= 0 ? key.slice(slash + 1) : key
    const parent = ensureDir(parentPath)
    const node: EntryNode = { label, fullPath: key + '/', isDir: true, size: 0, children: [] }
    parent.children.push(node)
    dirCache.set(key, node)
    return node
  }

  for (const e of entries) {
    const norm = e.name.replace(/\/$/, '')
    if (norm === '') continue
    if (e.isDir) {
      ensureDir(norm)
      continue
    }
    const slash = norm.lastIndexOf('/')
    const parentPath = slash >= 0 ? norm.slice(0, slash) : ''
    const label = slash >= 0 ? norm.slice(slash + 1) : norm
    const parent = ensureDir(parentPath)
    parent.children.push({ label, fullPath: norm, isDir: false, size: e.size, children: [] })
  }

  const sortRec = (nodes: EntryNode[]) => {
    nodes.sort((a, b) => {
      if (a.isDir !== b.isDir) return a.isDir ? -1 : 1
      return a.label.localeCompare(b.label)
    })
    nodes.forEach((n) => sortRec(n.children))
  }
  sortRec(root.children)
  return root.children
}
