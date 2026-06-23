import type {
  AssetType,
  AssetTypeGroup,
  JDKMatrixItem,
} from '@/api/runtimeAssets'

/**
 * 「运行时与制品」全局页（FR-082）的纯展示逻辑：字节格式化、JDK 节点×版本引用矩阵、
 * 制品筛选。抽成无 React 依赖的模块以便 vitest 单测（参照 bots-overview.ts 约定）。
 */

/** 人类可读字节（1024 进制）。负数/非有限按 0 处理。 */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = bytes
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  // 整数位用整数，否则保留一位小数。
  const text = v >= 100 || Number.isInteger(v) ? Math.round(v).toString() : v.toFixed(1)
  return `${text} ${units[i]}`
}

/** 矩阵一格：某节点 × 某「vendor-major」列上的 JDK 实例与引用数（可能多条同 vendor-major）。 */
export interface JDKMatrixCell {
  /** 该格命中的 JDK 矩阵项（同节点同 vendor-major 可有多条不同小版本）。 */
  items: JDKMatrixItem[]
  /** 该格引用实例总数（跨多条 JDK 累加）。 */
  refCount: number
}

/** 节点×版本引用矩阵（可视化用）：行=节点，列=vendor-major，格=JDK+引用。 */
export interface JDKMatrix {
  /** 列键（如 `Temurin-21`），按 major 降序、再 vendor 升序排列。 */
  columns: JDKMatrixColumn[]
  /** 行（节点）。 */
  rows: JDKMatrixRow[]
}

export interface JDKMatrixColumn {
  key: string
  vendor: string
  majorVersion: number
}

export interface JDKMatrixRow {
  nodeId: number
  nodeName: string
  nodeOnline: boolean
  /** 列键 → 格。缺失列表示该节点无此 vendor-major 的 JDK。 */
  cells: Record<string, JDKMatrixCell>
}

/** vendor-major 列键。 */
function columnKey(vendor: string, major: number): string {
  return `${vendor}-${major}`
}

/**
 * 由 JDK 矩阵项构建节点×版本引用矩阵：
 * 行按 nodeName 升序；列按 majorVersion 降序、同 major 再 vendor 升序；
 * 同节点同 vendor-major 的多条（不同小版本）合并进一格并累加引用数。
 */
export function buildJDKMatrix(items: JDKMatrixItem[]): JDKMatrix {
  const columnMap = new Map<string, JDKMatrixColumn>()
  const rowMap = new Map<number, JDKMatrixRow>()

  for (const it of items) {
    const key = columnKey(it.vendor, it.majorVersion)
    if (!columnMap.has(key)) {
      columnMap.set(key, { key, vendor: it.vendor, majorVersion: it.majorVersion })
    }
    let row = rowMap.get(it.nodeId)
    if (!row) {
      row = { nodeId: it.nodeId, nodeName: it.nodeName, nodeOnline: it.nodeOnline, cells: {} }
      rowMap.set(it.nodeId, row)
    }
    const cell = row.cells[key] ?? { items: [], refCount: 0 }
    cell.items.push(it)
    cell.refCount += it.refCount
    row.cells[key] = cell
  }

  const columns = Array.from(columnMap.values()).sort((a, b) => {
    if (a.majorVersion !== b.majorVersion) return b.majorVersion - a.majorVersion
    return a.vendor.localeCompare(b.vendor)
  })
  const rows = Array.from(rowMap.values()).sort((a, b) => a.nodeName.localeCompare(b.nodeName))
  return { columns, rows }
}

/** 制品筛选条件。 */
export interface AssetFilter {
  /** 类型筛选；'all' 表示不限。 */
  type: AssetType | 'all'
  /** 仅显示被引用（refCount>0）。 */
  onlyReferenced: boolean
  /** 名称/版本/sha256 子串（忽略大小写）；空串不过滤。 */
  search: string
}

/** 默认筛选：全部类型、不限引用、无搜索。 */
export const DEFAULT_ASSET_FILTER: AssetFilter = { type: 'all', onlyReferenced: false, search: '' }

/**
 * 对制品分组应用筛选，返回过滤后的分组（保留分组结构，组内 items 过滤；空组剔除）。
 * type 筛选作用于分组；onlyReferenced/search 作用于组内每条资产。
 */
export function filterAssetGroups(groups: AssetTypeGroup[], filter: AssetFilter): AssetTypeGroup[] {
  const needle = filter.search.trim().toLowerCase()
  const out: AssetTypeGroup[] = []
  for (const g of groups) {
    if (filter.type !== 'all' && g.type !== filter.type) continue
    const items = g.items.filter((a) => {
      if (filter.onlyReferenced && a.refCount <= 0) return false
      if (needle) {
        const hay = `${a.name} ${a.version} ${a.sha256} ${a.filename}`.toLowerCase()
        if (!hay.includes(needle)) return false
      }
      return true
    })
    if (items.length === 0) continue
    out.push({ ...g, items })
  }
  return out
}

/** 短 sha 展示（前 12 位）。 */
export function shortSha(sha: string): string {
  return sha ? sha.slice(0, 12) : ''
}
