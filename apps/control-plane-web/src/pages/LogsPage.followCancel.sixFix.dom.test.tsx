import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, screen } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { toast } from 'sonner'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import LogsPage from './LogsPage'
import api from '@/api/client'
import { useLogsFederation } from '@/api/logFederation'

const item = { event_id: 'follow-1', log_source_id: 'inst:1/stdout', message: '跟随原始行', event_time_utc: '2026-10-22T00:00:00Z' }
const response = { sourceTag: 'federated', items: [item], exhausted: true, view: { view_id: 'evidence-view' }, coverage: { complete: true, targets: [] }, quality: { duplicate_quality: 'exact', stats_quality: 'exact' } }
const gaps = [{ worker_id: 'worker-1', storage_namespace: 'node:1', from: '9007199254740993', to: '9007199254740998', reason_code: 'operator_abandoned', operator: 'admin', at_utc: '2026-10-22T00:00:00Z', log_source_id: 'inst:1/stdout', source_generation: 'g-1' }]

beforeEach(() => {
  loginMockUser()
  vi.useFakeTimers()
  HTMLElement.prototype.hasPointerCapture ??= () => false
  HTMLElement.prototype.setPointerCapture ??= () => {}
  HTMLElement.prototype.releasePointerCapture ??= () => {}
  HTMLElement.prototype.scrollIntoView ??= () => {}
  server.use(
    http.get(API('/nodes'), () => HttpResponse.json([])),
    http.get(API('/instances'), () => HttpResponse.json([])),
    http.get(API('/logs/federation'), () => HttpResponse.json({ ...response, ingest_position_gaps: gaps })),
    http.get(API('/logs/federation/stats'), () => HttpResponse.json({ points: [], coverage: { complete: true } })),
    http.get(API('/logs/federation/facets'), () => HttpResponse.json({ dimensions: [], coverage: { complete: true } })),
  )
})
afterEach(() => { vi.restoreAllMocks(); vi.useRealTimers() })
async function advance(ms: number) {
  await act(async () => { await vi.advanceTimersByTimeAsync(ms) })
}

describe('跟随完成后调度与输入去抖', () => {
  it('首屏只有一个Search，连续输入复用250ms debounce', async () => {
    const keywords: string[] = []
    server.use(http.get(API('/logs/federation'), ({ request }) => {
      keywords.push(new URL(request.url).searchParams.get('keyword') ?? '')
      return HttpResponse.json(response)
    }))
    renderWithProviders(<LogsPage />)
    await advance(300)
    expect(keywords).toEqual([''])
    fireEvent.change(screen.getByPlaceholderText('搜索日志内容…'), { target: { value: 'a' } })
    await advance(100)
    fireEvent.change(screen.getByPlaceholderText('搜索日志内容…'), { target: { value: 'ab' } })
    await advance(100)
    fireEvent.change(screen.getByPlaceholderText('搜索日志内容…'), { target: { value: '  abc  ' } })
    await advance(249)
    expect(keywords).toEqual([''])
    await advance(100)
    expect(keywords).toEqual(['', 'abc'])
  })

  it('全部时间下退出follow必须真abort同key证据Query，不取消其它scope观察者', async () => {
    const toastError = vi.spyOn(toast, 'error')
    const get = vi.spyOn(api, 'get')
    const abort = vi.spyOn(XMLHttpRequest.prototype, 'abort')
    const releases: (() => void)[] = []
    server.use(
      http.get(API('/logs/federation'), async ({ request }) => {
        const params = new URL(request.url).searchParams
        if (params.get('limit') === '1' || params.get('keyword') === 'independent-proof') {
          await new Promise<void>((done) => releases.push(done))
        }
        return HttpResponse.json({ ...response, ingest_position_gaps: gaps })
      }),
      http.get(API('/logs/federation/tail'), () => HttpResponse.json({ ...response, view: undefined })),
    )
    function IndependentProbe() { useLogsFederation({ keyword: 'independent-proof' }); return null }
    const view = renderWithProviders(<><LogsPage /><IndependentProbe /></>)
    try {
      await advance(300)
      const select = screen.getByTestId('logs-range-select')
      fireEvent.pointerDown(select, { button: 0, ctrlKey: false }); fireEvent.click(select)
      await advance(50)
      fireEvent.click(screen.getByRole('option', { name: '全部时间', exact: true }))
      await advance(200)
      fireEvent.click(screen.getByRole('button', { name: '实时跟随' }))
      await advance(300)
      const evidenceCall = get.mock.calls.find(([path, config]) => path === '/logs/federation' && config?.params?.limit === 1)
      const independent = get.mock.calls.find(([path, config]) => path === '/logs/federation' && config?.params?.keyword === 'independent-proof')
      expect(evidenceCall?.[1]?.signal).toBeDefined()
      expect(independent?.[1]?.signal?.aborted).toBe(false)
      await advance(35000)
      const before = abort.mock.calls.length
      fireEvent.click(screen.getByRole('button', { name: '实时跟随' }))
      await advance(100)
      expect(evidenceCall?.[1]?.signal?.aborted).toBe(true)
      expect(abort.mock.calls.length).toBeGreaterThan(before)
      expect(independent?.[1]?.signal?.aborted).toBe(false)
      expect(get.mock.calls.filter(([path, config]) => path === '/logs/federation' && config?.params?.limit === 1)).toHaveLength(1)
      expect(get.mock.calls.some(([path, config]) => path === '/logs/federation' && config?.params?.limit === 100 && config.params.from === undefined && config.params.to === undefined)).toBe(true)
      expect(get.mock.calls.some(([path]) => path === '/logs' || path === '/logs/legacy')).toBe(false)
      expect(toastError).not.toHaveBeenCalled()
      expect(screen.getByTestId('logs-ingest-position-gaps')).toHaveTextContent('9007199254740993')
    } finally {
      view.unmount()
      releases.forEach((release) => release())
    }
  })

  it('慢位置证据也与Tail串行，跨多个周期仍最多一条数据面请求且不丢已有凭据', async () => {
    let active = 0
    let maximum = 0
    let tailCalls = 0
    let release!: () => void
    server.use(
      http.get(API('/logs/federation'), async ({ request }) => {
        if (new URL(request.url).searchParams.get('limit') === '1') {
          active++; maximum = Math.max(maximum, active)
          await new Promise<void>((done) => { release = done })
          active--
        }
        return HttpResponse.json({ ...response, ingest_position_gaps: gaps })
      }),
      http.get(API('/logs/federation/tail'), () => {
        active++; maximum = Math.max(maximum, active); tailCalls++; active--
        return HttpResponse.json({ ...response, view: undefined })
      }),
    )
    const view = renderWithProviders(<LogsPage />)
    await advance(300)
    fireEvent.click(screen.getByRole('button', { name: '实时跟随' }))
    await advance(300)
    expect(tailCalls).toBe(1)
    expect(release).toBeTypeOf('function')
    await advance(12000)
    expect(tailCalls).toBe(1)
    expect(maximum).toBe(1)
    expect(screen.getByTestId('logs-ingest-position-gaps')).toHaveTextContent('9007199254740993')
    await act(async () => release())
    await advance(100)
    await advance(2800)
    expect(tailCalls).toBe(1)
    await advance(500)
    expect(tailCalls).toBe(2)
    expect(maximum).toBe(1)
    view.unmount()
  })

  it('慢follow跨多个桶仍单在飞，完成后3s才再取；永久位置凭据独立保留', async () => {
    let calls = 0
    let inFlight = 0
    let maximum = 0
    const releases: (() => void)[] = []
    server.use(http.get(API('/logs/federation/tail'), async () => {
      calls++; inFlight++; maximum = Math.max(maximum, inFlight)
      await new Promise<void>((resolve) => releases.push(resolve))
      inFlight--
      return HttpResponse.json({ ...response, view: undefined })
    }))
    const view = renderWithProviders(<LogsPage />)
    await advance(300)
    fireEvent.click(screen.getByRole('button', { name: '实时跟随' }))
    await advance(300)
    expect(calls).toBe(1)
    expect(screen.getByTestId('logs-ingest-position-gaps')).toHaveTextContent('9007199254740993')
    await advance(15000)
    expect(calls).toBe(1)
    expect(maximum).toBe(1)
    await act(async () => releases[0]())
    await advance(50)
    await advance(2800)
    expect(calls).toBe(1)
    await advance(500)
    expect(calls).toBe(2)
    expect(maximum).toBe(1)
    view.unmount()
    await act(async () => releases[1]())
  })
})
