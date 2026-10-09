import { describe, expect, it } from 'vitest'

import { ROUTE_CHUNKS, ROUTE_KEYS, matchRouteKey } from './route-chunks'
import { flatNavItems } from '@/lib/nav-config'

/**
 * 路由 chunk 表测试（FR-496 阶段 6 补丁）。
 *
 * 这张表是「路由表 + 预取」的唯一真源，一旦漏项就意味着某个入口 hover 了却什么都没预取到
 * （静默失效，界面上看不出来）。所以这里的重点是**覆盖度**：导航里出现的每个目标都必须能查到；
 * 以及动态段归一化是否把 `/instances/42` 这类具体路径正确折回路由键。
 */
describe('matchRouteKey：具体路径 → 路由表键', () => {
  it('静态路径精确命中', () => {
    expect(matchRouteKey('/instances')).toBe('/instances')
    expect(matchRouteKey('/audit')).toBe('/audit')
    expect(matchRouteKey('/')).toBe('/')
  })

  it('动态段逐个替换后命中（含中段动态段）', () => {
    expect(matchRouteKey('/instances/42')).toBe('/instances/:id')
    expect(matchRouteKey('/instances/42/files')).toBe('/instances/:id/files')
    expect(matchRouteKey('/bots/sessions/7f3a')).toBe('/bots/sessions/:id')
    expect(matchRouteKey('/client-channels/ota-e2e/publish')).toBe('/client-channels/:id/publish')
  })

  it('字面量优先于动态段：/instances/new 不会被吃成 /instances/:id', () => {
    expect(matchRouteKey('/instances/new')).toBe('/instances/new')
  })

  it('查不到就返回 null（未知路径必须静默忽略，不能抛）', () => {
    expect(matchRouteKey('/does-not-exist')).toBeNull()
    expect(matchRouteKey('/api/v1/instances')).toBeNull()
  })
})

describe('导航覆盖度：每个导航入口都预取得动', () => {
  it('平台管理员的全部导航目标都在路由 chunk 表内', () => {
    const missing = flatNavItems(null, true)
      .map((item) => item.to)
      .filter((to) => matchRouteKey(to) === null)
    expect(missing).toEqual([])
  })

  it('按角色种子裁剪后的导航目标同样可预取', () => {
    const missing = flatNavItems(10)
      .map((item) => item.to)
      .filter((to) => matchRouteKey(to) === null)
    expect(missing).toEqual([])
  })

  it('全幅工具页（/super、/director）也在表内：它们同样要等 chunk', () => {
    expect(ROUTE_CHUNKS['/super']).toBeTypeOf('function')
    expect(ROUTE_CHUNKS['/director']).toBeTypeOf('function')
  })

  it('每个取值都是可调用的加载器（表本身不会在 import 时拉起任何页面）', () => {
    expect(ROUTE_KEYS.length).toBeGreaterThan(30)
    for (const key of ROUTE_KEYS) {
      expect(ROUTE_CHUNKS[key as keyof typeof ROUTE_CHUNKS], `路由 ${key} 的加载器`).toBeTypeOf('function')
    }
  })
})
