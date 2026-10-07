/**
 * 配置收藏（书签）存储（FR-071）。
 *
 * 收藏是「常用配置快速访问」的 UI 便利项，按实例存于 localStorage，不入后端 DB
 * （见 docs/specs/config-explorer/api.md 决策）。storage 经参数注入，便于纯函数单测
 * 与 SSR/无 storage 环境兜底。
 */

/** 最小 storage 接口（localStorage 子集）。 */
export interface FavoritesStorage {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
}

/** 收藏存储键（按实例隔离）。 */
export function favoritesKey(instanceId: number): string {
  return `jm:config-favorites:${instanceId}`
}

/** 浏览器 localStorage（无则返回 null，调用方降级为内存无操作）。 */
export function browserStorage(): FavoritesStorage | null {
  try {
    if (typeof localStorage === 'undefined') return null
    return localStorage
  } catch {
    // 隐私模式 / 禁用存储时访问 localStorage 抛错。
    return null
  }
}

/** 读取某实例收藏的配置相对路径列表（去重、过滤空、容错坏 JSON）。 */
export function loadFavorites(storage: FavoritesStorage | null, instanceId: number): string[] {
  if (!storage) return []
  let raw: string | null
  try {
    raw = storage.getItem(favoritesKey(instanceId))
  } catch {
    return []
  }
  if (!raw) return []
  try {
    const parsed = JSON.parse(raw)
    if (!Array.isArray(parsed)) return []
    const seen = new Set<string>()
    const out: string[] = []
    for (const item of parsed) {
      if (typeof item !== 'string') continue
      const p = item.trim()
      if (!p || seen.has(p)) continue
      seen.add(p)
      out.push(p)
    }
    return out
  } catch {
    return []
  }
}

/** 持久化收藏列表（去重后写回）。 */
export function saveFavorites(
  storage: FavoritesStorage | null,
  instanceId: number,
  paths: string[],
): void {
  if (!storage) return
  const seen = new Set<string>()
  const out: string[] = []
  for (const p of paths) {
    const t = p.trim()
    if (!t || seen.has(t)) continue
    seen.add(t)
    out.push(t)
  }
  try {
    storage.setItem(favoritesKey(instanceId), JSON.stringify(out))
  } catch {
    // 配额满/禁用：静默失败（收藏为便利项，丢失不致命）。
  }
}

/** 是否已收藏某路径。 */
export function isFavorite(favorites: string[], path: string): boolean {
  return favorites.includes(path)
}

/** 切换收藏状态，返回新列表（不可变；已存在则移除，否则追加到末尾）。 */
export function toggleFavorite(favorites: string[], path: string): string[] {
  const p = path.trim()
  if (!p) return favorites
  if (favorites.includes(p)) return favorites.filter((x) => x !== p)
  return [...favorites, p]
}

/** 移除某收藏，返回新列表。 */
export function removeFavorite(favorites: string[], path: string): string[] {
  return favorites.filter((x) => x !== path)
}
