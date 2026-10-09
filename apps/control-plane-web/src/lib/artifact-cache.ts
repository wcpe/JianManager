/**
 * 节点制品缓存的契约类型与纯逻辑工具（FR-178）。
 *
 * 归属说明（ADR-097）：类型由两侧共用——应用 API 层 `@/api/nodeRuntime` 的查询 hook
 * 返回 `ArtifactCacheView`，受控视图 `components/views/nodes/NodeArtifactCachePanel`
 * 经 props 接收它。类型随组件归包，避免包内组件为取类型而反向依赖 `@/api`。
 * 纯函数（字节格式化、容量上限 GB ↔ 字节换算）一并归包，便于单测。
 */

/** 一条节点制品缓存项。name/version 由 CP 用 asset 表按 sha256 补全（可能为空）。 */
export interface ArtifactCacheItem {
  sha256: string
  name: string
  type: string
  version: string
  size: number
  /** 首次存入时间（Unix 秒）。 */
  cachedAt: number
  /** 最近命中/存入时间（Unix 秒，LRU 依据）。 */
  lastUsedAt: number
}

/** 节点制品缓存视图：列表 + 总占用 + 当前上限。 */
export interface ArtifactCacheView {
  items: ArtifactCacheItem[]
  totalBytes: number
  /** 容量上限（字节，0=不限）。 */
  capBytes: number
}

/** 把字节数格式化为人类可读大小（B/KB/MB/GB/TB），0 或非有限值回 "0 B"。 */
export function formatCacheBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  const value = bytes / Math.pow(1024, i)
  return `${value.toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

/** 把 GB 输入（用户在上限框填的数）换算为字节；空/非法/<=0 视为 0（不限）。 */
export function capGiBToBytes(gib: string | number): number {
  const n = typeof gib === 'number' ? gib : parseFloat(gib)
  if (!Number.isFinite(n) || n <= 0) return 0
  return Math.round(n * 1024 * 1024 * 1024)
}

/** 把字节上限换算回 GB（用于回显输入框）；0 或非法回空串（表示不限）。 */
export function capBytesToGiB(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return ''
  const gib = bytes / (1024 * 1024 * 1024)
  // 去掉无意义的尾随 0（1.00 → 1，1.50 → 1.5）。
  return String(Math.round(gib * 100) / 100)
}

/** 上限的人类可读描述：0/空=不限，否则格式化字节。 */
export function describeCap(capBytes: number): string {
  if (!Number.isFinite(capBytes) || capBytes <= 0) return '不限'
  return formatCacheBytes(capBytes)
}
