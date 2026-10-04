import { beforeEach, describe, expect, it, vi } from 'vitest'
import { QueryClient, type Query } from '@tanstack/react-query'
import {
  MOCK_POLL_FOLLOW_ORIGINAL,
  setMockPollInterval,
} from '@jianmanager/devmock/runtime-control'
import {
  installMockPollingControl,
  listPollingQueryLabels,
  pollQueryLabel,
  refreshMockPolling,
  togglePollingQuery,
} from './mock-polling'

/**
 * 全局轮询控制（FR-496 阶段 6 补丁）。
 * 断言的是「改写后的最终选项」——这才是 QueryObserver 排期时真正读的值，
 * 只断言内部状态的话，把 `defaultQueryOptions` 装饰写错（比如漏掉函数形式）也不会红。
 */

/** 读某查询解析后的最终 refetchInterval（与 QueryObserver 走同一条路径）。 */
const resolvedInterval = (client: QueryClient, queryKey: unknown[], refetchInterval?: unknown) =>
  client.defaultQueryOptions({ queryKey, refetchInterval } as never).refetchInterval

/** 解析成实际间隔：接管后一律是函数形式（排期时现算），测试里用假 query 调一下即可。 */
const resolve = (interval: unknown, data?: unknown): number | false | undefined =>
  typeof interval === 'function' ? (interval as (q: Query) => number | false)(fakeQuery(data)) : (interval as number | false | undefined)

/** 假 query 对象：函数形式的 refetchInterval 只读 `state.data`，不必造真 Query。 */
const fakeQuery = (data: unknown) => ({ state: { data } }) as Query

/** 这些用例验的是「显式覆盖档位」的语义，统一先切到 1s；跟随原始档单独成组验证。 */
beforeEach(() => {
  setMockPollInterval(1000)
})

describe('pollQueryLabel', () => {
  it('取 key 前两段可读部分作为标签', () => {
    expect(pollQueryLabel(['instances', 'search', { page: 1 }])).toBe('instances/search')
    expect(pollQueryLabel(['nodes'])).toBe('nodes')
    expect(pollQueryLabel(undefined)).toBe('(无 key)')
  })
})

describe('installMockPollingControl', () => {
  it('把静态 refetchInterval 统一改成面板档位（默认 1s）', () => {
    const client = new QueryClient()
    installMockPollingControl(client)

    expect(resolve(resolvedInterval(client, ['instances', 'list'], 30_000))).toBe(1000)
    expect(resolve(resolvedInterval(client, ['nodes', 'list'], 10_000))).toBe(1000)
  })

  it('关闭档位时不轮询（false），而不是保留原间隔', () => {
    const client = new QueryClient()
    installMockPollingControl(client)
    setMockPollInterval(0)

    expect(resolve(resolvedInterval(client, ['instances', 'list'], 30_000))).toBe(false)
  })

  it('不碰一次性查询：未配置 / false 的保持原样', () => {
    const client = new QueryClient()
    installMockPollingControl(client)

    expect(resolvedInterval(client, ['setup', 'status'])).toBeUndefined()
    expect(resolvedInterval(client, ['audit', 'list'], false)).toBe(false)
  })

  it('条件轮询保留原判断，只替换间隔', () => {
    const client = new QueryClient()
    installMockPollingControl(client)
    const conditional = (query: Query) => ((query.state.data as { running?: boolean } | undefined)?.running ? 2000 : false)

    const interval = resolvedInterval(client, ['tasks', 'detail'], conditional)
    expect(typeof interval).toBe('function')
    expect(resolve(interval, { running: true })).toBe(1000)
    // 条件不成立时必须仍然 false——否则「只在运行时轮询」会变成一直轮询
    expect(resolve(interval, { running: false })).toBe(false)
    expect(resolve(interval, undefined)).toBe(false)
  })

  it('黑名单里的查询保持自己的间隔', () => {
    const client = new QueryClient()
    installMockPollingControl(client, { exclude: ['instances'] })

    expect(resolve(resolvedInterval(client, ['instances', 'search'], 30_000))).toBe(30_000)
    expect(resolve(resolvedInterval(client, ['nodes', 'list'], 30_000))).toBe(1000)
  })

  it('白名单非空时只接管名单内的查询', () => {
    const client = new QueryClient()
    installMockPollingControl(client, { include: ['nodes'] })

    expect(resolve(resolvedInterval(client, ['nodes', 'list'], 30_000))).toBe(1000)
    expect(resolve(resolvedInterval(client, ['instances', 'search'], 30_000))).toBe(30_000)
  })

  it('改档位后同一次解析出的函数即时跟随（排期时现算，不必重新解析）', () => {
    const client = new QueryClient()
    installMockPollingControl(client)

    const interval = resolvedInterval(client, ['instances', 'list'], 30_000)
    expect(resolve(interval)).toBe(1000)

    setMockPollInterval(15_000)
    expect(resolve(interval)).toBe(15_000)

    // 关闭档位必须真的停：静态档位若被写成「解析时算好的值」，这里就会露出真面目
    setMockPollInterval(0)
    expect(resolve(interval)).toBe(false)
  })

  it('排除项也在排期时重新判断（点掉芯片后立刻回到自己的间隔）', () => {
    const client = new QueryClient()
    installMockPollingControl(client)

    const interval = resolvedInterval(client, ['instances', 'list'], 30_000)
    expect(resolve(interval)).toBe(1000)

    togglePollingQuery('instances/list')
    expect(resolve(interval)).toBe(30_000)
  })
})

describe('默认档「跟随原始」= 不干预（FR-496 阶段 6 补丁）', () => {
  it('静态间隔原样返回，不被统一覆盖', () => {
    const client = new QueryClient()
    installMockPollingControl(client)
    setMockPollInterval(MOCK_POLL_FOLLOW_ORIGINAL)

    expect(resolve(resolvedInterval(client, ['alerts', 'events'], 30_000))).toBe(30_000)
    expect(resolve(resolvedInterval(client, ['agentObservability', 'series'], 10_000))).toBe(10_000)
  })

  it('条件函数原样转调（跟随原始档下不改写判断）', () => {
    const client = new QueryClient()
    installMockPollingControl(client)
    setMockPollInterval(MOCK_POLL_FOLLOW_ORIGINAL)
    const conditional = (query: Query) => ((query.state.data as { running?: boolean } | undefined)?.running ? 5000 : false)

    const interval = resolvedInterval(client, ['artifactReconcile', 'run'], conditional)
    expect(resolve(interval, { running: true })).toBe(5000)
    expect(resolve(interval, { running: false })).toBe(false)
  })

  it('从跟随原始切到具体档位后立刻生效（直通包装不冻值）', () => {
    const client = new QueryClient()
    installMockPollingControl(client)
    setMockPollInterval(MOCK_POLL_FOLLOW_ORIGINAL)

    const interval = resolvedInterval(client, ['alerts', 'events'], 30_000)
    expect(resolve(interval)).toBe(30_000)

    setMockPollInterval(5000)
    expect(resolve(interval)).toBe(5000)
  })
})

describe('轮询查询范围（黑白名单 UI 的数据源）', () => {
  it('只列「本来在轮询」的查询，且点一下即可排除', () => {
    const client = new QueryClient()
    installMockPollingControl(client)
    // 建进缓存（真实路径：QueryObserver 挂载时走 QueryCache.build）
    const polling = client.getQueryCache().build(client, { queryKey: ['instances', 'search'], refetchInterval: 30_000 } as never)
    client.getQueryCache().build(client, { queryKey: ['setup', 'status'] } as never)

    expect(listPollingQueryLabels()).toEqual(['instances/search'])

    togglePollingQuery('instances/search')
    expect(listPollingQueryLabels()).toEqual(['instances/search']) // 仍在列表里，只是被排除
    expect(refreshMockPollingCalls(client)).toBeGreaterThan(0)
    // 排除后该查询回到自己的 30s
    expect(resolve(resolvedInterval(client, ['instances', 'search'], 30_000))).toBe(30_000)
    // 缓存里的查询对象确实被接了管（面板列的是它）
    expect(resolve(polling.options.refetchInterval)).toBe(30_000)
  })
})

/** 记录 refreshMockPolling 触发了几次 invalidateQueries（只重取轮询查询）。 */
function refreshMockPollingCalls(client: QueryClient): number {
  const spy = vi.spyOn(client, 'invalidateQueries')
  refreshMockPolling()
  const calls = spy.mock.calls.length
  spy.mockRestore()
  // 谓词必须只挑「本来在轮询」的查询，否则改档位会顺带打一堆无关请求
  return calls
}
