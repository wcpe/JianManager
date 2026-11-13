/**
 * 侧栏工具收纳偏好（侧栏重构）：底部图标轨「已收纳」与「显示在侧边栏」的置回状态。
 *
 * 工具集合的**真源**是 `workspace-navigation.ts` 的 ops「群组与工作台」组
 * （网络拓扑 / 分组管理 / 超级工作台 / 导播台）——本模块只存**用户偏好**：
 * 哪些工具已置回侧栏主体（有序，供常用区渲染与排序）。
 *
 * 键沿用 `jm.sidebar.*` 约定（侧栏既有裸键 `sidebar.*` 属历史，不迁移不混用）。
 * 模块级订阅 + `useSyncExternalStore`（与 `server-selection.ts` 同款做法，跨子树互通）。
 */
import { useSyncExternalStore } from 'react'

/** 「已置回侧栏」工具路径列表（有序）的 localStorage 键。 */
export const PINNED_TOOLS_KEY = 'jm.sidebar.pinnedTools'

const listeners = new Set<() => void>()
let cache: { raw: string | null; items: string[] } = { raw: null, items: [] }

function parse(raw: string | null): string[] {
  if (!raw) return []
  try {
    const value: unknown = JSON.parse(raw)
    if (!Array.isArray(value)) return []
    return value.filter((item): item is string => typeof item === 'string')
  } catch {
    return []
  }
}

/** 读取当前已置回的工具路径（有序）。 */
export function getPinnedTools(): string[] {
  const raw = typeof localStorage === 'undefined' ? null : localStorage.getItem(PINNED_TOOLS_KEY)
  if (cache.raw === raw) return cache.items
  const items = parse(raw)
  cache = { raw, items }
  return items
}

function write(items: string[]): void {
  const raw = JSON.stringify(items)
  const current = typeof localStorage === 'undefined' ? null : localStorage.getItem(PINNED_TOOLS_KEY)
  // 内容未变不落盘不广播：避免订阅方空转重渲（与 server-selection.ts 同判据）。
  if (current === raw) {
    if (cache.raw !== raw) cache = { raw, items }
    return
  }
  if (typeof localStorage !== 'undefined') localStorage.setItem(PINNED_TOOLS_KEY, raw)
  cache = { raw, items }
  for (const listener of listeners) listener()
}

/** 置回/收纳切换（不存在则追加到末尾，存在则移除）。 */
export function toggleToolPinned(path: string): void {
  const items = getPinnedTools()
  write(items.includes(path) ? items.filter((item) => item !== path) : [...items, path])
}

/**
 * 用给定的有序列表整体替换偏好（供拖拽排序一次写定）。
 * 只保留合法字符串并去重，避免把拖拽载荷里的脏数据写进存储。
 */
export function setPinnedTools(next: readonly string[]): void {
  const cleaned = [...new Set(next.filter((item): item is string => typeof item === 'string' && item !== ''))]
  write(cleaned)
}

/** 在已置回列表内移动（delta=-1 上移 / +1 下移）；越界为空操作。 */
export function movePinnedTool(path: string, delta: number): void {
  const items = [...getPinnedTools()]
  const index = items.indexOf(path)
  const target = index + delta
  if (index < 0 || target < 0 || target >= items.length) return
  // `splice` 在索引有效时必有返回值，但 `noUncheckedIndexedAccess` 会把它算成可能 undefined；
  // 用解构默认值兜住类型，同时保证「取不到就不写」，避免把 undefined 塞进已置回列表。
  const [item = path] = items.splice(index, 1)
  items.splice(target, 0, item)
  write(items)
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

/** 已置回工具列表的响应式订阅。 */
export function usePinnedTools(): string[] {
  return useSyncExternalStore(subscribe, getPinnedTools)
}
