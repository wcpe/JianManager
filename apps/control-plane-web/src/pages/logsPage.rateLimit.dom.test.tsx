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
import { screen, fireEvent, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import LogsPage from './LogsPage'
import { RATE_LIMIT_QUERY_RETRIES } from '@/api/logFederation'

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

  it('切筛选后持续 429：不得把旧筛选的行冒充成新筛选的结果', async () => {
    loginMockUser()
    let rateLimited = false
    stubHandlers({ rateLimited: () => rateLimited })
    // 非跟随态：主查询走 /logs/federation。筛选 A（默认 15m）先给一份真实行；
    // 限流开始后的请求（含切筛选后的新查询）一律 429。
    server.use(
      http.get(API('/logs/federation'), () => {
        federationCalls++
        if (rateLimited) {
          return HttpResponse.json(
            { error: 'RATE_LIMITED', message: '请求过于频繁，请稍后再试' },
            { status: 429 },
          )
        }
        return HttpResponse.json({
          sourceTag: 'federated',
          items: [liveItem],
          view: { view_id: 'filter-a-view' },
          exhausted: true,
          coverage: { complete: true, targets: [], enumeration_state: 'EXHAUSTED' },
        })
      }),
    )
    renderWithProviders(<LogsPage />)
    // 假时钟下推进一小段让首屏联邦查询落地（比照下方 stats/facets 用例）。
    await vi.advanceTimersByTimeAsync(300)
    // 筛选 A 先拿到真实行。
    expect(screen.getByText('federation-kept-row')).toBeInTheDocument()

    // 切到筛选 B（最近 1 小时）的同时服务端开始持续限流：新查询重试耗尽后必须落地为
    // 「限流 + 空结果（非成功空态）」，而不是继续渲染筛选 A 的行——那会把 A 节点/实例的
    // 日志读成 B 筛选的结果（生产 429 占比 23.8%，这条路径并不罕见）。
    HTMLElement.prototype.hasPointerCapture ??= () => false
    HTMLElement.prototype.setPointerCapture ??= () => {}
    HTMLElement.prototype.releasePointerCapture ??= () => {}
    HTMLElement.prototype.scrollIntoView ??= () => {}
    rateLimited = true
    // 假时钟下 userEvent 的交互序列会挂起，改用 fireEvent 直接驱动 Radix Select
    // （trigger 在 pointerdown 开、option 在 click 选，均为同步派发）。
    const trigger = screen.getAllByRole('combobox')[0]!
    fireEvent.pointerDown(trigger, { button: 0, ctrlKey: false })
    fireEvent.click(trigger)
    await vi.advanceTimersByTimeAsync(50)
    fireEvent.click(screen.getByRole('option', { name: '最近 1 小时' }))
    await vi.advanceTimersByTimeAsync(50)
    // 等两层重试耗尽（内层 250/600ms + react-query 500/1000ms，合计约 4s）落定为错误终态。
    await vi.advanceTimersByTimeAsync(6000)

    // ① 旧筛选的行不得继续可见：保留项与筛选身份绑定，filterKey 变了即判失效。
    expect(screen.queryByText('federation-kept-row')).not.toBeInTheDocument()
    // ② 限流横幅必须可见（说明「这次没查到」）。
    expect(screen.getByTestId('logs-coverage-banner')).toHaveAttribute('data-state', 'rate_limited')
    // ③ 不得把限流伪装成「查到了 0 条」空成功。
    expect(screen.queryByText('暂无日志')).not.toBeInTheDocument()
    const emptyState = screen.getByTestId('logs-empty-state')
    expect(emptyState).toHaveAttribute('data-empty-success', 'false')
    // ④ 切筛选后确实发出了带新筛选的请求（证明 ④ 的空白源于新查询失败，而非没发请求）。
    expect(federationCalls).toBeGreaterThan(1)
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

// 接线级回归（2026-10-12，终审补）：**主查询的 429 重试必须真的接在 queryFn 上**。
//
// 为什么必须补这一条：既有的 `federation-rate-limit-retry.test.ts` 直接单测
// `getWithRateLimitRetry` 这个 helper 本体——它证明「helper 能重试」，但**不证明
// queryFn 真的调用了它**。实测：把 `logFederation.ts` 里的
// `getWithRateLimitRetry<...>(path, …)` 换回裸 `api.get<...>(path, …)`，
// 跑全部联邦相关测试（5 个文件 / 61 条）**无一转红**。
//
// 而该重试正是生产实测 636 次 429（占该端点 23.8%）的头号对策：日后重构误删接线，
// CI 全绿、线上退回「传输失败/列表清空」，且无任何信号。
//
// **判据设计（关键）**：直接用「最终是否成功」判不出来——react-query 自己还会重试
// （`rateLimitRetry` 配 `RATE_LIMIT_QUERY_RETRIES=2`），会掩盖 helper 是否被调用。
// 故改用**请求次数下界**：让假后端**持续 429**，数主查询被打了多少次。
//   · 接线在 ⇒ 单次 queryFn 内 helper 会发 1+len([250,600])=3 次；
//   · 接线被拆 ⇒ 单次 queryFn 只发 1 次。
// 断言「远多于 react-query 自身能产生的次数」即可区分两层。
//
// 变异验证（实测）：把 `getWithRateLimitRetry` 换回裸 `api.get` ⇒ 本用例红（次数骤降）。
describe('联邦主查询的 429 重试接线（端到端）', () => {
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    loginMockUser()
    federationCalls = 0
    classicCalls = 0
    tailCalls = 0
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('持续 429 时主查询的重试次数远多于 react-query 单层重试（拆掉 helper 接线即红）', async () => {
    let fedAttempts = 0
    server.use(
      http.get(API('/nodes'), () => HttpResponse.json([{ id: 1, name: 'n1', status: 'online' }])),
      http.get(API('/instances'), () => HttpResponse.json([])),
      http.get(API('/logs/federation/tail'), () =>
        HttpResponse.json({
          items: [],
          coverage: { complete: true, targets: [], enumeration_state: 'OPEN' },
          exhausted: false,
        }),
      ),
      http.get(API('/logs/federation/stats'), () =>
        HttpResponse.json({ points: [], coverage: { complete: true, targets: [] } }),
      ),
      http.get(API('/logs/federation/facets'), () =>
        HttpResponse.json({ dimensions: [], coverage: { complete: true, targets: [] } }),
      ),
      // 主查询：**持续 429**（生产形态的 RATE_LIMITED 响应体）。
      http.get(API('/logs/federation'), () => {
        fedAttempts++
        federationCalls++
        return HttpResponse.json(
          { error: 'RATE_LIMITED', message: '请求过于频繁，请稍后再试' },
          { status: 429 },
        )
      }),
    )

    renderWithProviders(<LogsPage />)
    // 推进足够时长，让两层退避（helper 250/600ms + react-query 500/1000ms）都跑完。
    await vi.advanceTimersByTimeAsync(500)
    await vi.advanceTimersByTimeAsync(5000)
    await vi.advanceTimersByTimeAsync(5000)

    // 核心：单层重试最多 3 次（1 首次 + 2 重试）；两层叠加才会显著更多。
    // helper 接线在 ⇒ 至少 1×3；被拆 ⇒ 只有 1×3 但其中「同一 queryFn 内」不会连发。
    // 用「明显超过单层上限」的下界即可区分。
    expect(fedAttempts).toBeGreaterThan(RATE_LIMIT_QUERY_RETRIES + 1 + 2)
  })
})

// 筛选身份键的**维度完整性**回归（2026-10-12 终审补）。
//
// `filterKey` 是「限流期间能否复用上一份列表」的唯一归属判据（见 LogsPage 中
// retainedFederation 的复用条件）。它必须覆盖**每个真正参与查询的筛选维度**：
// 少一个维度 ⇒ 在该维度上切筛选时「键不变」，保留项被判为仍然有效，
// 新查询一旦 429（生产该端点 23.8%），页面就把上一个筛选的行渲染成新筛选的结果。
//
// 既有覆盖：本文件的「切筛选后持续 429」（`range` 维度）与
// `LogsPage.federation.dom.test.tsx` 的视图切换用例（`view`/`nodeId`/`instanceId`）。
// 但 `source` 与 `onlineOnly` 两个维度**无任何覆盖**——而它们都进 federationParams
// （`view === 'all' && source` 与 `onlineOnly`）。本用例补 `source`。
//
// 事件的驱动方式沿用 `LogsPage.federation.dom.test.tsx` 的既有写法（`userEvent`）：
// 该 tab 是 Radix 驱动的 Button，`fireEvent.click` 在假时钟下不足以完成选择。
// 故本用例用**真实时钟** + `findBy*` 等待（代价是等待两次重试退避余量）。
describe('筛选身份键必须覆盖 source 维度（限流保留项的归属判据）', () => {
  beforeEach(() => {
    federationCalls = 0
    classicCalls = 0
    tailCalls = 0
  })

  it('切来源（全部日志视图）后持续 429：不得把上一个来源的行冒充成新来源的结果', async () => {
    // 「全部日志」视图只对平台管理员渲染（LOG_VIEWS.filter 里的 isPlatformAdmin 门禁），
    // 故必须带 role=10 的 JWT payload 登录——与 LogsPage.federation.dom.test.tsx 同法。
    const payload = btoa(JSON.stringify({ userId: 1, username: 'admin', role: 10, exp: Math.floor(Date.now() / 1000) + 900 }))
    loginMockUser(`mock.${payload}.sig`)
    // 记录服务端实际收到的 source（证明它是独立维度、真的参与查询）。
    const seenSources: string[] = []
    let rateLimited = false
    server.use(
      http.get(API('/nodes'), () => HttpResponse.json([])),
      http.get(API('/instances'), () => HttpResponse.json([])),
      http.get(API('/logs/federation/tail'), () =>
        HttpResponse.json({
          items: [],
          coverage: { complete: true, targets: [], enumeration_state: 'OPEN' },
          exhausted: false,
        }),
      ),
      http.get(API('/logs/federation/stats'), () =>
        HttpResponse.json({ points: [], coverage: { complete: true, targets: [] } }),
      ),
      http.get(API('/logs/federation/facets'), () =>
        HttpResponse.json({ dimensions: [], coverage: { complete: true, targets: [] } }),
      ),
      http.get(API('/logs/federation'), ({ request }) => {
        federationCalls++
        const src = new URL(request.url).searchParams.get('source')
        seenSources.push(src ?? '(none)')
        if (rateLimited) {
          return HttpResponse.json(
            { error: 'RATE_LIMITED', message: '请求过于频繁，请稍后再试' },
            { status: 429 },
          )
        }
        return HttpResponse.json({
          sourceTag: 'federated',
          items: [liveItem],
          view: { view_id: 'source-a-view' },
          exhausted: true,
          coverage: { complete: true, targets: [], enumeration_state: 'EXHAUSTED' },
        })
      }),
    )

    renderWithProviders(<LogsPage />)
    // 筛选 A（来源 = 全部）：先拿到一份真实行。
    await screen.findByText('federation-kept-row')
    const user = userEvent.setup()

    // 切到「全部日志」视图（source 选择器只在该视图下渲染）。
    await user.click(screen.getByRole('tab', { name: '全部日志' }))
    const sourceTrigger = await screen.findByTestId('logs-source-select')
    expect(seenSources).toContain('(none)')

    // 切到筛选 B（来源 = 控制面，即 `source=control_plane`，UI 文案「平台」）的同时服务端开始持续限流。
    rateLimited = true
    Element.prototype.scrollIntoView = vi.fn()
    // Radix Select 用键盘激活（与 LogsPage.federation.dom.test.tsx 同一写法）。
    sourceTrigger.focus()
    await user.keyboard('{Enter}')
    const option = await screen.findByRole('option', { name: '平台' })
    await user.click(option)

    // ① 新筛选确实发出了带 source 的请求（证明这是独立筛选维度，而非纯前端展示开关）。
    await waitFor(() =>
      expect(seenSources).toContain('control_plane'),
      { timeout: 8000 },
    )
    // 等两层重试退避退到错误终态（helper 250/600ms + react-query 500/1000ms）。
    await waitFor(
      () => expect(screen.getByTestId('logs-coverage-banner')).toHaveAttribute('data-state', 'rate_limited'),
      { timeout: 15000 },
    )

    // ② 旧筛选的行不得继续可见：`source` 变了，保留项即判失效。
    //    filterKey 漏含 source 时本断言会红（旧行被当作新筛选的结果继续渲染）。
    expect(screen.queryByText('federation-kept-row')).not.toBeInTheDocument()
    // ③ 不得把限流伪装成「查到了 0 条」空成功。
    expect(screen.queryByText('暂无日志')).not.toBeInTheDocument()
    const emptyState = screen.getByTestId('logs-empty-state')
    expect(emptyState).toHaveAttribute('data-empty-success', 'false')
  }, 60000)
})
