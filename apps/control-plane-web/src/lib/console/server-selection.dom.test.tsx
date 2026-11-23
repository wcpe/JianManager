import { describe, expect, it, beforeEach } from 'vitest'
import {
  FAVORITES_LIMIT,
  RECENT_LIMIT,
  toggleFavoriteServer,
  getFavoriteServers,
  getRecentServers,
  recordRecentServer,
} from './server-selection'

/**
 * 侧栏服务器列表的容量边界。
 *
 * 侧栏常驻列只展示「收藏 + 最近」两类，规模由两个常量封顶：
 *   收藏 FAVORITES_LIMIT = 32，最近 RECENT_LIMIT = 8
 * 即侧栏最多 40 条，且全在 localStorage 里同步读，不随实例总数增长。
 *
 * 所以它**不需要虚拟化**：虚拟化是为「条目数无上界且可能上千」的列表服务的，
 * 应用到这里只会增加复杂度而无收益。下面用真实写入验证这两个上限确实生效——
 * 如果将来有人把上限改大或去掉，这组用例会红，提醒重新评估要不要虚拟化。
 */
describe('侧栏服务器列表容量边界', () => {
  const mk = (i: number) => ({
    id: i,
    uuid: `uuid-${i}`,
    nodeId: 1,
    name: `server-${i}`,
    status: 'RUNNING',
  })

  beforeEach(() => {
    localStorage.clear()
  })

  it('最近列表按 RECENT_LIMIT 截断（默认 8）', () => {
    for (let i = 1; i <= RECENT_LIMIT + 5; i++) recordRecentServer(mk(i))
    expect(getRecentServers()).toHaveLength(RECENT_LIMIT)
  })

  it('最近列表保留最新的，淘汰最旧的', () => {
    for (let i = 1; i <= RECENT_LIMIT + 3; i++) recordRecentServer(mk(i))
    const recent = getRecentServers()
    expect(recent[0].id).toBe(RECENT_LIMIT + 3)
    expect(recent.map((item) => item.id)).not.toContain(1)
  })

  it('收藏按 FAVORITES_LIMIT 截断（默认 32）', () => {
    for (let i = 1; i <= FAVORITES_LIMIT + 5; i++) toggleFavoriteServer(mk(i))
    expect(getFavoriteServers()).toHaveLength(FAVORITES_LIMIT)
  })

  it('侧栏最大条目数 = 收藏上限 + 最近上限，与实例总数无关', () => {
    // 即使实例总数远超上限，侧栏也只可能渲染这么多行
    const maxRows = FAVORITES_LIMIT + RECENT_LIMIT
    expect(maxRows).toBeLessThanOrEqual(40)
  })
})
