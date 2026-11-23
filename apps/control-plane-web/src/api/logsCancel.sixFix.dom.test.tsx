import { useState } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import api from '@/api/client'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { getWithRateLimitRetry, isLogRequestCanceled, useLogsFederation, useLogsFederationFacets, useLogsFederationFields, useLogsFederationStats } from './logFederation'

/**
 * 取消/信号贯穿（回归）。
 *
 * 2026-10-29 缩范围：经典 `useLogs` / `useLegacyLogs` 已随 CP `logs` 表删除而下线，
 * 本文件原先把它们也纳入「路径列表」，那两路现在不存在，保留只会让用例挂在一个
 * 已 404 的端点上。现在只守仍存活的三路联邦请求（Search / Stats / Facets）——
 * 它们共用同一个客户端令牌桶，恰恰是最需要确认「取消真的传到底」的地方。
 */
beforeEach(() => loginMockUser())
afterEach(() => { vi.restoreAllMocks(); vi.useRealTimers() })

it('条件切换/卸载把AbortSignal贯穿联邦Search、Stats、Facets', async () => {
  const requests: { signal: AbortSignal; finish: () => void }[] = []
  const get = vi.spyOn(api, 'get')
  const abort = vi.spyOn(XMLHttpRequest.prototype, 'abort')
  const paths = ['/logs/federation', '/logs/federation/stats', '/logs/federation/facets', '/logs/federation/fields']
  server.use(...paths.map((path) => http.get(API(path), async ({ request }) => {
    await new Promise<void>((finish) => requests.push({ signal: request.signal, finish }))
    return HttpResponse.json({ items: [], points: [], dimensions: [], coverage: { complete: true } })
  })))
  function Probe() {
    const [keyword, setKeyword] = useState('第一组')
    useLogsFederation({ keyword })
    useLogsFederationStats({ keyword }, true)
    useLogsFederationFacets({ keyword }, true)
    useLogsFederationFields({ keyword }, true)
    return <button onClick={() => setKeyword('第二组')}>换查询</button>
  }
  const view = renderWithProviders(<Probe />)
  await waitFor(() => expect(requests).toHaveLength(4))
  fireEvent.click(screen.getByRole('button', { name: '换查询' }))
  await waitFor(() => expect(requests).toHaveLength(8))
  // MSW 的 XHR 请求投影不联动 request.signal；同时核对传给 axios 的真信号与原生 abort 调用。
  const signals = get.mock.calls.filter(([path]) => paths.includes(path)).map(([, config]) => config?.signal)
  expect(signals).toHaveLength(8)
  expect(signals.slice(0, 4).every((signal) => signal?.aborted)).toBe(true)
  expect(abort).toHaveBeenCalledTimes(4)
  view.unmount()
  expect(signals.every((signal) => signal?.aborted)).toBe(true)
  expect(abort).toHaveBeenCalledTimes(8)
  requests.forEach(({ finish }) => finish())
})

it('同key其它活跃observer保护Search/Stats/Facets，最后禁用才精确取消且不伤独立scope', async () => {
  const get = vi.spyOn(api, 'get')
  const abort = vi.spyOn(XMLHttpRequest.prototype, 'abort')
  const releases: (() => void)[] = []
  const paths = ['/logs/federation', '/logs/federation/stats', '/logs/federation/facets', '/logs/federation/fields']
  server.use(...paths.map((path) => http.get(API(path), async () => {
    await new Promise<void>((finish) => releases.push(finish))
    return HttpResponse.json({ items: [], points: [], dimensions: [], coverage: { complete: true } })
  })))
  function Observer({ enabled }: { enabled: boolean }) {
    const params = { keyword: 'shared-evidence', limit: 1 }
    useLogsFederation(params, { enabled })
    useLogsFederationStats(params, enabled)
    useLogsFederationFacets(params, enabled)
    useLogsFederationFields(params, enabled)
    return null
  }
  function Probe() {
    const [first, setFirst] = useState(true)
    const [second, setSecond] = useState(true)
    useLogsFederation({ keyword: 'independent-scope' })
    return <><Observer enabled={first} /><Observer enabled={second} /><button onClick={() => setFirst(false)}>禁用首个观察者</button><button onClick={() => setSecond(false)}>禁用最后观察者</button></>
  }
  const view = renderWithProviders(<Probe />)
  try {
    await waitFor(() => expect(releases).toHaveLength(5))
    const shared = get.mock.calls.filter(([, config]) => config?.params?.keyword === 'shared-evidence').map(([, config]) => config?.signal)
    const independent = get.mock.calls.filter(([, config]) => ['independent-scope'].includes(config?.params?.keyword)).map(([, config]) => config?.signal)
    expect(shared).toHaveLength(4)
    expect(independent).toHaveLength(1)
    fireEvent.click(screen.getByRole('button', { name: '禁用首个观察者' }))
    expect(shared.every((signal) => signal?.aborted === false)).toBe(true)
    expect(abort).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: '禁用最后观察者' }))
    await waitFor(() => expect(shared.every((signal) => signal?.aborted === true)).toBe(true))
    expect(abort).toHaveBeenCalledTimes(4)
    expect(independent.every((signal) => signal?.aborted === false)).toBe(true)
  } finally {
    view.unmount()
    releases.forEach((finish) => finish())
  }
})

describe('可取消的限流短退避', () => {
  it('退避期间取消立即退出，不等计时器、不重发旧HTTP', async () => {
    vi.useFakeTimers()
    const get = vi.spyOn(api, 'get').mockRejectedValue({ response: { status: 429 } })
    const controller = new AbortController()
    const pending = getWithRateLimitRetry('/logs/federation', { signal: controller.signal }, [60000])
    await Promise.resolve()
    expect(get.mock.calls.filter(([path]) => path === '/logs/federation')).toHaveLength(1)
    controller.abort()
    const error = await pending.catch((reason: unknown) => reason)
    expect(isLogRequestCanceled(error)).toBe(true)
    await vi.advanceTimersByTimeAsync(120000)
    expect(get.mock.calls.filter(([path]) => path === '/logs/federation')).toHaveLength(1)
  })

  it('预先取消不发请求，也不被转成联邦降级结果', async () => {
    const get = vi.spyOn(api, 'get')
    const controller = new AbortController(); controller.abort()
    const error = await getWithRateLimitRetry('/logs/federation', { signal: controller.signal }).catch((reason: unknown) => reason)
    expect(isLogRequestCanceled(error)).toBe(true)
    expect(get.mock.calls.filter(([path]) => path === '/logs/federation')).toHaveLength(0)
  })
})
