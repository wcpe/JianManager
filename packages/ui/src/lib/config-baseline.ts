/**
 * 配置基线的 scope 与展示纯函数（FR-458）。
 *
 * 基线键为 `(scopeKey, filePath)`，`scopeKey` 限定「哪些实例应共享该基线」：
 * `all` / `group:<id>` / `network:<id>` / `tag:<tag>` / `instance:<id>`。
 */

/** scope 种类。 */
export type ScopeKind = 'all' | 'group' | 'network' | 'tag' | 'instance'

/** 由种类 + 取值拼出 scopeKey（all 不取值）。 */
export function composeScopeKey(kind: ScopeKind, value: string): string {
  if (kind === 'all') return 'all'
  return `${kind}:${value.trim()}`
}

/** 带取值的 scope 种类（all 无取值）。 */
const SCOPED_KINDS: readonly string[] = ['group', 'network', 'tag', 'instance']

/** 解析 scopeKey 的种类（非法/空回退 all）。 */
export function scopeKindOf(scopeKey: string): ScopeKind {
  const key = scopeKey.trim()
  if (!key || key === 'all') return 'all'
  const kind = key.split(':')[0]
  return SCOPED_KINDS.includes(kind) ? (kind as ScopeKind) : 'all'
}

/** 解析 scopeKey 的取值（all 无值）。 */
export function scopeValueOf(scopeKey: string): string {
  const key = scopeKey.trim()
  if (!key || key === 'all') return ''
  const idx = key.indexOf(':')
  return idx < 0 ? '' : key.slice(idx + 1)
}

/**
 * scopeKey 是否合法：
 * `all` 无取值；其余种类须带非空取值；group/network/instance 取值须为正整数。
 */
export function isValidScopeKey(scopeKey: string): boolean {
  const key = scopeKey.trim()
  if (!key) return false
  if (key === 'all') return true
  const idx = key.indexOf(':')
  if (idx <= 0) return false
  const kind = key.slice(0, idx)
  const value = key.slice(idx + 1).trim()
  if (!value) return false
  if (kind === 'tag') return true
  if (kind === 'group' || kind === 'network' || kind === 'instance') {
    const n = Number(value)
    return Number.isInteger(n) && n > 0
  }
  return false
}

/** 截断长哈希供表格展示。 */
export function shortHash(hash: string, len = 12): string {
  const h = hash.trim()
  return h.length <= len ? h : h.slice(0, len)
}
