/**
 * 列顺序的通用实现（**多页共用**）。
 *
 * 【为什么抽到共享层】实例列表用 `jm.instances.columnOrder`，节点列表需要 `jm.nodes.columnOrder`。
 * 两边逻辑一字不差（读、写、移动、钉住列、旧存储自愈），各自实现必然漂移——
 * 实例页那边已经因为「三处各算一遍」连续踩过三次坑（见 computeVisibleColumns 的注释）。
 * 这里做成泛型，行为只写一份。
 */

/**
 * 读取列顺序。
 *
 * 补全规则：存储里**只保留已知 key**，再把缺失的已知 key 按默认顺序追加。
 * 这样新增列不会因为旧存储而被永久隐藏——旧用户升级后自动看到新列，
 * 且他此前拖过的相对顺序仍被保留。
 */
export function readStoredOrder<T extends string>(
  storageKey: string,
  allKeys: readonly T[],
  defaultOrder: readonly T[],
): T[] {
  const fallback = [...defaultOrder]
  try {
    const raw = localStorage.getItem(storageKey)
    if (!raw) return fallback
    const parsed: unknown = JSON.parse(raw)
    if (!Array.isArray(parsed)) return fallback
    const known = new Set<string>(allKeys)
    const kept = parsed.filter((k): k is T => typeof k === 'string' && known.has(k))
    // 去重：存储被手工改坏时可能出现重复 key，重复会导致 React key 冲突与列重复渲染。
    const seen = new Set<T>()
    const deduped = kept.filter((k) => (seen.has(k) ? false : (seen.add(k), true)))
    for (const k of defaultOrder) if (!seen.has(k)) deduped.push(k)
    return deduped
  } catch {
    return fallback
  }
}

/** 写入列顺序（失败静默：隐私模式下 localStorage 不可写不应影响页面）。 */
export function writeStoredOrder<T extends string>(storageKey: string, order: readonly T[]): void {
  try {
    localStorage.setItem(storageKey, JSON.stringify(order))
  } catch {
    /* 忽略：无存储能力时退化为「本次会话内有效」 */
  }
}

/**
 * 把 `from` 列移动到 `to` 列的位置。
 *
 * 语义：按拖动方向决定插入点在目标之前还是之后，让 `from` 恰好占据 `to` 原来的那一格。
 * - 向前拖（from 在 to 左侧）→ 落到目标之后；
 * - 向后拖（from 在 to 右侧）→ 落到目标之前。
 *
 * 【为什么不能写成「先摘掉 from 再插到 to 之前」】那样对向前拖完全无效：
 * 摘掉 from 后 to 的下标左移一位，插回 to 之前等于插回原位（实例页实测「拖了没反应」）。
 */
export function moveColumnKey<T extends string>(
  order: readonly T[],
  from: T,
  to: T,
  pinned: readonly T[] = [],
): T[] {
  if (from === to) return [...order]
  // 钉住的列不参与换位：拖不动，也不能被别人挤开（保持首/尾语义）
  if (pinned.includes(from) || pinned.includes(to)) return [...order]

  const fromIndex = order.indexOf(from)
  const toIndex = order.indexOf(to)
  if (fromIndex < 0 || toIndex < 0) return [...order]

  const next = [...order]
  next.splice(fromIndex, 1)
  next.splice(toIndex, 0, from)
  return next
}
