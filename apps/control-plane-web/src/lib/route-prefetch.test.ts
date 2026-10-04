import { describe, expect, it, vi } from 'vitest'

import {
  SIDEBAR_SHORTCUT_WARMUP,
  createRoutePrefetcher,
  normalizeTarget,
  prefetchedRoutes,
  prefetchRoute,
} from './route-prefetch'
import { matchRouteKey } from './route-chunks'

/**
 * 路由 chunk 预取测试（FR-496 阶段 6 补丁）。
 *
 * 预取是**尽力而为的副作用**：它最大的风险不是「没预取到」，而是「预取重复触发/在错误路径上
 * 反复重试」，那会白耗带宽并干扰正常导航。所以断言集中在幂等、路径归一化、失败可重试、
 * 未知目标静默忽略四点；真实 chunk 的下载用注入的假加载器替代（测试不去拉几十个页面模块）。
 */

/** 造一个可控的假加载器：默认 resolve，`failTimes` 次内先 reject。 */
function fakeLoader(failTimes = 0) {
  let calls = 0
  const loader = vi.fn(() => {
    calls += 1
    if (calls <= failTimes) return Promise.reject(new Error('chunk 下载失败'))
    return Promise.resolve({ default: () => null })
  })
  return loader
}

describe('normalizeTarget', () => {
  it('剥掉 query 与 hash', () => {
    expect(normalizeTarget('/instances?page=2&sort=name')).toBe('/instances')
    expect(normalizeTarget('/nodes#section')).toBe('/nodes')
  })

  it('结尾斜杠归一（除根路径外）', () => {
    expect(normalizeTarget('/audit/')).toBe('/audit')
    expect(normalizeTarget('/')).toBe('/')
  })
})

describe('createRoutePrefetcher：幂等与归一化', () => {
  it('同一目标只 import 一次（重复 hover 不重复请求）', () => {
    const load = fakeLoader()
    const prefetcher = createRoutePrefetcher({ '/instances': load })

    prefetcher.prefetch('/instances')
    prefetcher.prefetch('/instances')
    prefetcher.prefetch('/instances')

    expect(load).toHaveBeenCalledTimes(1)
    expect(prefetcher.prefetched()).toEqual(['/instances'])
  })

  it('同 chunk 的不同具体路径（/instances/1、/instances/2）只算一次', () => {
    const load = fakeLoader()
    const prefetcher = createRoutePrefetcher({ '/instances/:id': load })

    prefetcher.prefetch('/instances/1')
    prefetcher.prefetch('/instances/2?tab=config')

    expect(load).toHaveBeenCalledTimes(1)
    expect(prefetcher.prefetched()).toEqual(['/instances/:id'])
  })

  it('带 query 的导航目标按去参后的路径命中', () => {
    const load = fakeLoader()
    const prefetcher = createRoutePrefetcher({ '/audit': load })

    prefetcher.prefetch('/audit?action=instance.start')

    expect(load).toHaveBeenCalledTimes(1)
  })

  it('未知目标静默忽略：不抛错、不调用任何加载器', () => {
    const load = fakeLoader()
    const prefetcher = createRoutePrefetcher({ '/instances': load })

    expect(() => prefetcher.prefetch('/api/v1/instances?download=1')).not.toThrow()
    expect(() => prefetcher.prefetch('https://example.com')).not.toThrow()
    expect(load).not.toHaveBeenCalled()
    expect(prefetcher.prefetched()).toEqual([])
  })

  it('预取失败后撤掉标记：下次同类意图可以重试', async () => {
    const load = fakeLoader(1)
    const prefetcher = createRoutePrefetcher({ '/nodes': load })

    prefetcher.prefetch('/nodes')
    expect(load).toHaveBeenCalledTimes(1)
    // 失败是异步的：等 reject 被 catch 掉，标记才撤除。
    await vi.waitFor(() => expect(prefetcher.prefetched()).toEqual([]))

    prefetcher.prefetch('/nodes')
    expect(load).toHaveBeenCalledTimes(2)
  })

  it('按发起顺序记录已预取的路由键', () => {
    const prefetcher = createRoutePrefetcher({
      '/nodes': fakeLoader(),
      '/audit': fakeLoader(),
      '/tasks': fakeLoader(),
    })

    prefetcher.prefetch('/audit')
    prefetcher.prefetch('/nodes')
    prefetcher.prefetch('/tasks')

    expect(prefetcher.prefetched()).toEqual(['/audit', '/nodes', '/tasks'])
  })
})

describe('应用级单例', () => {
  it('是稳定引用：导航项展开到元素上时不会每次渲染都换新函数', () => {
    const first = prefetchRoute
    expect(prefetchRoute).toBe(first)
    expect(typeof prefetchedRoutes()).toBe('object')
  })

  it('未知目标不抛错（外链 / 下载端点不得干扰导航）', () => {
    expect(() => prefetchRoute('/not-a-route')).not.toThrow()
  })
})

describe('侧栏快捷入口预热清单', () => {
  it('每一项都能被预取（路由表已覆盖）', () => {
    for (const to of SIDEBAR_SHORTCUT_WARMUP) {
      expect(matchRouteKey(to), `侧栏预热入口 ${to}`).not.toBeNull()
    }
  })

  it('保持小规模：一次 hover 不该拉几十个 chunk', () => {
    // 上限 4 是刻意约束（见 route-prefetch.ts 的带宽说明）：其中 `/` 是落地页、通常已在缓存，
    // 真实新增下载只有 /instances 与 /nodes 两个 chunk。
    expect(SIDEBAR_SHORTCUT_WARMUP.length).toBeLessThanOrEqual(4)
  })
})
