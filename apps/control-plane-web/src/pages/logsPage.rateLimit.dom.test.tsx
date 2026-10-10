/**
 * 日志中心「限流不清空 + 每 tick 只发一次」DOM 回归（生产 429 根因，FR-482）。
 *
 * 生产证据（2026-10 `/api/v1/logs/federation`，近 20MB cp.out、9199 条请求）：
 * 该端点 2676 次请求中 683 次非 200，其中 429 限流 636 次（23.8%）；同一批参数还会成对
 * 出现、时间戳仅差约 6ms。两条根因与对策都在本文件被固定下来：
 *
 * ① `isFederationUnavailable` 原只认 404/401/403/5xx，429 被当成「传输失败」且
 *    degraded=false ⇒ federationActive=true、items=null ⇒ 列表被清空。
 *    现要求：限流期间保留上一份已渲染行 + 给出可见限流提示，且**不得**呈「暂无日志」空成功。
 * ② follow 态的时间桶在渲染期重算，跨桶时 queryKey 变化触发一次请求，
 *    同时 refetchInterval 到点又触发一次 ⇒ 同一份数据成对双发；联邦视图下 useLogs
 *    还额外每 tick 打一条数据面请求。
 *    现要求：每个 3 秒 tick 恰好一次联邦请求，且联邦视图下不再有并行的经典请求。
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, fireEvent } from '@testing-library/react'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import LogsPage from './LogsPage'

const FOLLOW_INTERVAL = 3000

/** 联邦主查询与经典 `/logs` 的请求计数（每例重置）。 */
let tailCalls = 0
let federationCalls = 0
let classicCalls = 0
/** 聚合请求计数（验证 stats/facets 的限流重试确实发生）。 */
let statsCalls = 0
let facetsCalls = 0

/** 跟随态返回的唯一行：用于验证「限流后这一行仍在」。 */
const liveItem = {
  event_id: 'live-keep',
  log_source_id: 'node:1',
  message: 'federation-kept-row',
  event_time_utc: new Date().toISOString(),
}

function stubHandlers({ rateLimited }: { rateLimited: () => boolean }) {
  server.use(
    http.get(API('/nodes'), () => HttpResponse.json([])),
    http.get(API('/instances'), () => HttpResponse.json([])),
    http.get(API('/logs'), () => {
      classicCalls++
      return HttpResponse.json({ items: [], total: 0, page: 1, pageSize: 100 })
    }),
    http.get(API('/logs/federation/stats'), () => {
      statsCalls++
      return HttpResponse.json({ points: [], coverage: { complete: true, targets: [] } })
    }),
    http.get(API('/logs/federation/facets'), () => {
      facetsCalls++
      return HttpResponse.json({ dimensions: [], coverage: { complete: true, targets: [] } })
    }),
    http.get(API('/logs/federation/tail'), () => {
      if (rateLimited()) {
        // 生产形态：429 + RATE_LIMITED 码（`middleware/ratelimit.go` 的实际响应体）。
        return HttpResponse.json(
          { error: 'RATE_LIMITED', message: '请求过于频繁，请稍后再试' },
          { status: 429 },
        )
      }
      tailCalls++
      return HttpResponse.json({
        items: [liveItem],
        coverage: { complete: true, targets: [], enumeration_state: 'OPEN' },
        exhausted: false,
      })
    }),
    http.get(API('/logs/federation'), () => {
      federationCalls++
      return HttpResponse.json({
        sourceTag: 'federated',
        items: [],
        exhausted: true,
        coverage: { complete: true, targets: [], enumeration_state: 'EXHAUSTED' },
      })
    }),
  )
}

/** 渲染页面并开启「实时跟随」，返回后续可继续推进的假时钟控制点。 */
async function renderFollowing() {
  renderWithProviders(<LogsPage />)
  // 让首屏探测（/nodes、/instances、联邦）落地后再开跟随，避免把启动请求算进 tick 统计。
  await vi.advanceTimersByTimeAsync(300)
  fireEvent.click(screen.getByRole('button', { name: '实时跟随' }))
  await vi.advanceTimersByTimeAsync(300)
}

describe('日志中心限流保留与轮询节流（FR-482）', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    // 无 pointer capture 的 jsdom 下 Radix Select/按钮交互需要的最小补丁。
    HTMLElement.prototype.hasPointerCapture ??= () => false
    HTMLElement.prototype.setPointerCapture ??= () => {}
    HTMLElement.prototype.releasePointerCapture ??= () => {}
    HTMLElement.prototype.scrollIntoView ??= () => {}
    tailCalls = 0
    federationCalls = 0
    classicCalls = 0
    statsCalls = 0
    facetsCalls = 0
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('每个跟随 tick 恰好 1 条联邦请求，且联邦视图下不再并发经典 /logs 请求', async () => {
    loginMockUser()
    stubHandlers({ rateLimited: () => false })
    await renderFollowing()

    // 先把「开跟随」到首个桶边界之间的半拍走完，让采样起点与 3 秒桶边界对齐
    // （否则首个采样窗会被这段残差切开，看起来像少一次请求，而非真实行为差异）。
    await vi.advanceTimersByTimeAsync(FOLLOW_INTERVAL)

    // 逐个 tick 采样：稳态下每 3 秒必须恰好 1 条 tail 请求。
    // 改前的渲染期分桶会让同一 tick 变成 2 条（queryKey 变化 1 条 + refetchInterval 1 条），
    // 联邦视图下 useLogs 还会再补 1 条 ⇒ 本断言即那两处根因的回归闸。
    const tailPerTick: number[] = []
    const federationPerTick: number[] = []
    for (let i = 0; i < 4; i++) {
      const beforeTail = tailCalls
      const beforeFederation = federationCalls
      await vi.advanceTimersByTimeAsync(FOLLOW_INTERVAL)
      tailPerTick.push(tailCalls - beforeTail)
      federationPerTick.push(federationCalls - beforeFederation)
    }
    expect(tailPerTick).toEqual([1, 1, 1, 1])
    // 跟随态只走 tail：非 tail 的列表主查询不得跟着 tick 再发一次（否则每次轮询 2 条）。
    expect(federationPerTick).toEqual([0, 0, 0, 0])

    // 联邦是主数据源，经典 /logs 只作降级回退源：不得被请求
    // （其 queryKey 同样含随桶滚动的 from/to，仅关 refetchInterval 不足以消除）。
    expect(classicCalls).toBe(0)
  })

  it('持久 429：保留上一份列表、给出可见限流提示，且不呈「暂无日志」空成功', async () => {
    loginMockUser()
    let rateLimited = false
    stubHandlers({ rateLimited: () => rateLimited })
    await renderFollowing()

    // 先拿到一份真实行，确认它在限流后仍然可见。
    await vi.advanceTimersByTimeAsync(FOLLOW_INTERVAL)
    expect(screen.getByText('federation-kept-row')).toBeInTheDocument()

    // 服务端开始持续限流：跨越多个 tick（重试耗尽须先发生）。
    rateLimited = true
    await vi.advanceTimersByTimeAsync(FOLLOW_INTERVAL * 2)

    // ① 已有行不得被清空（改前：429 落入 transportFailure 且 degraded=false ⇒ 列表空白）。
    expect(screen.getByText('federation-kept-row')).toBeInTheDocument()
    // ② 必须有可见的限流提示，且文案说明「已保留上一次结果」。
    const banner = screen.getByTestId('logs-coverage-banner')
    expect(banner).toHaveAttribute('data-state', 'rate_limited')
    expect(screen.getByTestId('logs-coverage-title')).toHaveTextContent('请求过于频繁')
    // ③ 不得把限流伪装成「查到了 0 条」。
    expect(screen.queryByText('暂无日志')).not.toBeInTheDocument()
    const emptyState = screen.queryByTestId('logs-empty-state')
    if (emptyState) expect(emptyState).toHaveAttribute('data-empty-success', 'false')
  })

  it('429 恢复后可继续取到新数据，限流提示随之消失（闩锁不粘滞）', async () => {
    loginMockUser()
    let rateLimited = false
    stubHandlers({ rateLimited: () => rateLimited })
    await renderFollowing()

    await vi.advanceTimersByTimeAsync(FOLLOW_INTERVAL)
    rateLimited = true
    await vi.advanceTimersByTimeAsync(FOLLOW_INTERVAL * 2)
    expect(screen.getByTestId('logs-coverage-banner')).toHaveAttribute('data-state', 'rate_limited')

    // 限流解除：提示必须收回，否则会把已恢复正常误报成持续故障。
    rateLimited = false
    await vi.advanceTimersByTimeAsync(FOLLOW_INTERVAL * 2)
    expect(screen.getByText('federation-kept-row')).toBeInTheDocument()
    expect(screen.queryByTestId('logs-coverage-banner')).not.toBeInTheDocument()
  })

  it('stats/facets 持续 429 不得白屏：列表与横幅照常可用（洞察栏只是装饰）', async () => {
    loginMockUser()
    stubHandlers({ rateLimited: () => false })
    // 非跟随态：主查询走 /logs/federation，须同时给出 items 与 view_id ——
    // 聚合启用条件为「联邦可用 && 非跟随 && 有 viewId」。
    server.use(
      http.get(API('/logs/federation'), () => {
        federationCalls++
        return HttpResponse.json({
          sourceTag: 'federated',
          items: [liveItem],
          view: { view_id: 'rate-limit-view' },
          exhausted: true,
          coverage: { complete: true, targets: [], enumeration_state: 'EXHAUSTED' },
        })
      }),
      // 聚合两条与主查询共用同一个 ClientIP 令牌桶，且是生产里最慢的两条
      // （stats p90=6.20s、facets p90=9.99s）⇒ 最容易先被限流。
      http.get(API('/logs/federation/stats'), () => {
        statsCalls++
        return HttpResponse.json({ error: 'RATE_LIMITED' }, { status: 429 })
      }),
      http.get(API('/logs/federation/facets'), () => {
        facetsCalls++
        return HttpResponse.json({ error: 'RATE_LIMITED' }, { status: 429 })
      }),
    )
    renderWithProviders(<LogsPage />)
    // 聚合是延后串行的：facets 要等 stats 落定（成功或失败）才发起，而 stats 自身还要
    // 走完「短退避 250/600ms × react-query 退避 500/1000ms」才落定 ⇒ 需推进足够时长。
    await vi.advanceTimersByTimeAsync(300)
    await vi.advanceTimersByTimeAsync(5000)

    // 行列表不因聚合失败而消失。
    expect(screen.getByText('federation-kept-row')).toBeInTheDocument()
    // 聚合确已发起（说明重试路径真的被走到），且失败绝不升级成页面级错误态。
    expect(statsCalls).toBeGreaterThan(0)
    expect(facetsCalls).toBeGreaterThan(0)
    expect(screen.queryByText('加载日志失败')).not.toBeInTheDocument()
  })
})
