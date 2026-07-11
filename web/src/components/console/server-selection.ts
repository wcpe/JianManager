import { useSyncExternalStore } from 'react'

import type { InstanceInfo } from '@/api/instances'

/** 「最近打开」localStorage 键（沿用 FR-240 服务器选择器原键，FR-293 互通前提）。 */
export const RECENT_KEY = 'server-selector.recent'
/** 「收藏」localStorage 键（沿用 FR-240 服务器选择器原键）。 */
export const FAVORITES_KEY = 'server-selector.favorites'
/** 最近打开 LRU 容量（FR-293：≤8 条）。 */
export const RECENT_LIMIT = 8
/** 收藏容量上限（沿用 FR-240 选择器原值）。 */
export const FAVORITES_LIMIT = 32

/** 收藏/最近条目的本地快照：只存路由与展示所需字段；status 仅作离线兜底，展示以查询缓存为准。 */
export interface StoredInstance {
  id: number
  uuid: string
  nodeId: number
  name: string
  status: string
}

/*
 * 收藏/最近的共享外部 store（FR-293）。
 * localStorage 写入不会触发 React 重渲，而选择器弹窗与侧栏常驻列分属两棵组件子树，
 * 故用模块级订阅 + useSyncExternalStore 做轻量互通：任一处写入即广播给所有订阅组件。
 * 快照按 raw 字符串比对缓存，保证 getSnapshot 引用稳定（否则每渲染新数组会死循环），
 * 同时外部直写 localStorage（如测试种子）也能在下次读取时被感知。
 */
const cache = new Map<string, { raw: string | null; items: StoredInstance[] }>()
const listeners = new Set<() => void>()

function isStoredInstance(value: unknown): value is StoredInstance {
  if (!value || typeof value !== 'object') return false
  const item = value as Partial<StoredInstance>
  return (
    typeof item.id === 'number' &&
    typeof item.name === 'string' &&
    typeof item.uuid === 'string' &&
    typeof item.nodeId === 'number' &&
    typeof item.status === 'string'
  )
}

function parseStored(raw: string | null): StoredInstance[] {
  if (!raw) return []
  try {
    const value: unknown = JSON.parse(raw)
    if (!Array.isArray(value)) return []
    return value.filter(isStoredInstance)
  } catch {
    return []
  }
}

function readStored(key: string): StoredInstance[] {
  const raw = typeof localStorage === 'undefined' ? null : localStorage.getItem(key)
  const cached = cache.get(key)
  if (cached && cached.raw === raw) return cached.items
  const items = parseStored(raw)
  cache.set(key, { raw, items })
  return items
}

function writeStored(key: string, items: StoredInstance[]): void {
  const raw = JSON.stringify(items)
  const current = typeof localStorage === 'undefined' ? null : localStorage.getItem(key)
  // 内容未变则不落盘不广播：控制台页轮询期间反复 upsert 同一条目时避免订阅方空转重渲。
  if (current === raw) {
    const cached = cache.get(key)
    if (!cached || cached.raw !== raw) cache.set(key, { raw, items })
    return
  }
  if (typeof localStorage !== 'undefined') localStorage.setItem(key, raw)
  cache.set(key, { raw, items })
  for (const listener of listeners) listener()
}

function upsertStored(list: StoredInstance[], item: StoredInstance, limit: number): StoredInstance[] {
  return [item, ...list.filter((x) => x.id !== item.id)].slice(0, limit)
}

/** 提取实例的本地快照字段（InstanceInfo 与 StoredInstance 两形态通吃）。 */
export function toStored(instance: InstanceInfo | StoredInstance): StoredInstance {
  return { id: instance.id, uuid: instance.uuid, nodeId: instance.nodeId, name: instance.name, status: instance.status }
}

/** 订阅收藏/最近变更（任一键写入即通知）；返回退订函数。 */
export function subscribeServerSelection(listener: () => void): () => void {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

/** 读取当前收藏列表（新→旧）。 */
export function getFavoriteServers(): StoredInstance[] {
  return readStored(FAVORITES_KEY)
}

/** 读取当前最近打开列表（LRU，新→旧）。 */
export function getRecentServers(): StoredInstance[] {
  return readStored(RECENT_KEY)
}

/** 把实例记入「最近打开」LRU 头部：弹窗选择、常驻列点击与直接路由进入统一走这里。 */
export function recordRecentServer(instance: InstanceInfo | StoredInstance): void {
  writeStored(RECENT_KEY, upsertStored(getRecentServers(), toStored(instance), RECENT_LIMIT))
}

/** 收藏/取消收藏实例，弹窗与常驻列共用同一入口保证互通。 */
export function toggleFavoriteServer(instance: InstanceInfo | StoredInstance): void {
  const favorites = getFavoriteServers()
  const exists = favorites.some((item) => item.id === instance.id)
  const next = exists
    ? favorites.filter((item) => item.id !== instance.id)
    : upsertStored(favorites, toStored(instance), FAVORITES_LIMIT)
  writeStored(FAVORITES_KEY, next)
}

/** 收藏列表的响应式订阅（useSyncExternalStore，跨组件互通）。 */
export function useFavoriteServers(): StoredInstance[] {
  return useSyncExternalStore(subscribeServerSelection, getFavoriteServers)
}

/** 最近打开列表的响应式订阅（useSyncExternalStore，跨组件互通）。 */
export function useRecentServers(): StoredInstance[] {
  return useSyncExternalStore(subscribeServerSelection, getRecentServers)
}
