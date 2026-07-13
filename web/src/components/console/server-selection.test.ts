// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from 'vitest'

import {
  FAVORITES_KEY,
  RECENT_KEY,
  getFavoriteServers,
  getRecentServers,
  recordRecentServer,
  removeServer,
  subscribeServerSelection,
  toggleFavoriteServer,
  type StoredInstance,
} from './server-selection'

const a: StoredInstance = { id: 1, uuid: 'i-a', nodeId: 1, name: 'a', status: 'RUNNING' }
const b: StoredInstance = { id: 2, uuid: 'i-b', nodeId: 1, name: 'b', status: 'STOPPED' }
const c: StoredInstance = { id: 3, uuid: 'i-c', nodeId: 2, name: 'c', status: 'CRASHED' }

describe('server-selection removeServer（侧栏死链剔除，BUG 修复）', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('从最近与收藏双列剔除指定实例（删除后不再残留死链）', () => {
    recordRecentServer(a)
    recordRecentServer(b)
    toggleFavoriteServer(a) // a 同时进收藏

    removeServer(a.id)

    expect(getRecentServers().some((x) => x.id === a.id)).toBe(false)
    expect(getFavoriteServers().some((x) => x.id === a.id)).toBe(false)
    // 其它条目不受影响
    expect(getRecentServers().some((x) => x.id === b.id)).toBe(true)
  })

  it('剔除后广播订阅者（侧栏/选择器即时刷新）', () => {
    recordRecentServer(a)
    const listener = vi.fn()
    const unsub = subscribeServerSelection(listener)
    removeServer(a.id)
    expect(listener).toHaveBeenCalled()
    unsub()
  })

  it('剔除不存在的 id 是空操作，不误伤其它条目、不广播', () => {
    recordRecentServer(b)
    toggleFavoriteServer(c)
    const listener = vi.fn()
    const unsub = subscribeServerSelection(listener)

    removeServer(999)

    expect(getRecentServers().some((x) => x.id === b.id)).toBe(true)
    expect(getFavoriteServers().some((x) => x.id === c.id)).toBe(true)
    expect(listener).not.toHaveBeenCalled()
    unsub()
  })

  it('localStorage 键沿用 FR-240/293 原键，剔除后落盘一致', () => {
    recordRecentServer(a)
    toggleFavoriteServer(a)
    removeServer(a.id)
    expect(JSON.parse(localStorage.getItem(RECENT_KEY) ?? '[]')).toEqual([])
    expect(JSON.parse(localStorage.getItem(FAVORITES_KEY) ?? '[]')).toEqual([])
  })
})
