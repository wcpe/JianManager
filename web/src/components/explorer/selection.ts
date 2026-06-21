/**
 * 资源管理器多选模型（FR-070）。
 * 纯函数 + 不可变更新，便于 vitest（node 环境）单测。
 *
 * 选中态由「选中集合 + 锚点」构成：
 * - 普通点击：单选该项，锚点设为该项；
 * - Ctrl/Cmd 点击：切换该项是否选中，锚点设为该项；
 * - Shift 点击：选中锚点到该项的连续范围（基于当前有序列表），锚点不变；
 * - 全选 / 清空。
 */

export interface SelectionState {
  /** 选中的项 key 集合。 */
  selected: Set<string>
  /** 锚点 key（shift 范围选择的起点）；无则 null。 */
  anchor: string | null
}

/** 空选中态。 */
export function emptySelection(): SelectionState {
  return { selected: new Set(), anchor: null }
}

/** 点击修饰键。 */
export interface ClickModifiers {
  /** Shift（范围选择）。 */
  shift?: boolean
  /** Ctrl 或 Cmd（切换选择）。 */
  ctrlOrMeta?: boolean
}

/**
 * 处理一次点击，返回新的选中态。
 * @param state 当前选中态
 * @param key 被点击项的 key
 * @param orderedKeys 当前列表的有序 key（用于 shift 范围）
 * @param mods 修饰键
 */
export function clickSelect(
  state: SelectionState,
  key: string,
  orderedKeys: string[],
  mods: ClickModifiers = {},
): SelectionState {
  // Shift：从锚点到当前的范围（锚点缺失时退化为单选并设锚点）。
  if (mods.shift) {
    const anchor = state.anchor ?? key
    const i = orderedKeys.indexOf(anchor)
    const j = orderedKeys.indexOf(key)
    if (i === -1 || j === -1) {
      return { selected: new Set([key]), anchor: key }
    }
    const [lo, hi] = i <= j ? [i, j] : [j, i]
    const range = orderedKeys.slice(lo, hi + 1)
    return { selected: new Set(range), anchor } // 锚点保持不变
  }

  // Ctrl/Cmd：切换该项，锚点移到该项。
  if (mods.ctrlOrMeta) {
    const next = new Set(state.selected)
    if (next.has(key)) {
      next.delete(key)
    } else {
      next.add(key)
    }
    return { selected: next, anchor: key }
  }

  // 普通点击：单选 + 设锚点。
  return { selected: new Set([key]), anchor: key }
}

/** 全选（锚点设为第一项）。 */
export function selectAll(orderedKeys: string[]): SelectionState {
  return { selected: new Set(orderedKeys), anchor: orderedKeys[0] ?? null }
}

/** 清空选择。 */
export function clearSelection(): SelectionState {
  return emptySelection()
}

/** 是否选中某项。 */
export function isSelected(state: SelectionState, key: string): boolean {
  return state.selected.has(key)
}

/**
 * 从选中集合中剔除已不存在的 key（列表刷新后调用，避免悬挂选择）。
 * 锚点若失效则置 null。
 */
export function pruneSelection(state: SelectionState, validKeys: string[]): SelectionState {
  const valid = new Set(validKeys)
  const selected = new Set<string>()
  for (const k of state.selected) {
    if (valid.has(k)) selected.add(k)
  }
  const anchor = state.anchor && valid.has(state.anchor) ? state.anchor : null
  return { selected, anchor }
}
