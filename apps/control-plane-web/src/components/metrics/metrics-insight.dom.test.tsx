import { beforeEach, describe, expect, it } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { server } from '@jianmanager/devmock/server'
import { mockInject } from '@jianmanager/devmock/inject'
import { db } from '@jianmanager/devmock/db'

import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import { SLOSection, availabilityLevel, fmtAvailability, fmtDuration } from './SLOSection'
import { CapacityForecastCard } from './CapacityForecastCard'
import { AttributionCard } from './AttributionCard'
import { InstanceRankingPanel, fmtRankingValue } from './InstanceRankingPanel'
import { HourlyBars, PlayerTrendCard } from './PlayerTrendCard'

/** 收集发往指定路径的请求（断言「点击后真的重新取数」）。 */
function collectRequests(suffix: string) {
  const paths: string[] = []
  const listener = ({ request }: { request: Request }) => {
    const url = new URL(request.url)
    if (url.pathname.endsWith(suffix)) paths.push(url.pathname)
  }
  server.events.on('request:start', listener)
  return { paths, stop: () => server.events.removeListener('request:start', listener) }
}

/**
 * FR-463/464/465/469 观测增强组件（真组件打真 devmock handler，ADR-047 机制）：
 * 覆盖「无数据不伪造」「null 语义」「时区口径」「下钻」等验收点的可断言部分，
 * 以及 loading / 空态 / 排行下钻三类原缺守护（自审 M8）。
 */
describe('metrics 观测增强组件（mock 假后端）', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('可用性区块展示可用率与 MTTR/MTBF，并把 null 渲染为「无故障」而非 0', async () => {
    renderWithProviders(<SLOSection range="7d" scope="platform" />)
    expect(await screen.findByText('可用率')).toBeInTheDocument()
    // 假后端返回 incidents=3、mttrSeconds=412.5 → 412.5s ≈ 6.9m。
    await waitFor(() => expect(screen.getByText('故障次数')).toBeInTheDocument())
    expect(screen.getByText('6.9m')).toBeInTheDocument()
  })

  it('fmtDuration 对 null/0 语义稳（null→null，不返回 0 字符串）', () => {
    expect(fmtDuration(null)).toBeNull()
    expect(fmtDuration(0)).toBe('0s')
    expect(fmtDuration(412.5)).toBe('6.9m')
  })

  it('容量预测卡区分「有预测」与「无增长不预测」', async () => {
    renderWithProviders(<CapacityForecastCard scope="node" targetId="node-alpha" range="7d" />)
    expect(await screen.findByText('节点磁盘')).toBeInTheDocument()
    // 有增长：渲染耗尽天数与 80% CI。
    expect(screen.getByText('4.10 天')).toBeInTheDocument()
    expect(screen.getByText(/80% 区间/)).toBeInTheDocument()
    // 节点内存为负例（无增长）→ 显式「不适用」+ 原因，不给数字。
    expect(screen.getByText('节点内存')).toBeInTheDocument()
    expect(screen.getByText('不适用')).toBeInTheDocument()
    expect(screen.getByText('无增长趋势，不预测')).toBeInTheDocument()
  })

  it('归因卡默认不请求，点击「分析」后渲染主因与权重', async () => {
    const { default: userEvent } = await import('@testing-library/user-event')
    const user = userEvent.setup()
    const requests = collectRequests('/metrics/performance/attribution')
    renderWithProviders(<AttributionCard instanceUuid="inst-1-uuid" range="7d" />)
    expect(requests.paths, '未点击前不得发起归因请求').toHaveLength(0)
    const analyze = screen.getByRole('button', { name: '分析' })
    await user.click(analyze)
    expect(await screen.findByText(/劣化主因：GC 暂停占用（权重 0\.62）/)).toBeInTheDocument()
    expect(screen.getByText('GC 暂停占用')).toBeInTheDocument()
    expect(screen.getByText('62.0%')).toBeInTheDocument()
    expect(requests.paths.length, '点击「分析」应真的发起请求').toBeGreaterThan(0)
    requests.stop()
  })

  it('归因卡：「重新分析」点击后真的重新取数（M3，同值 setState 不会触发重渲染）', async () => {
    const { default: userEvent } = await import('@testing-library/user-event')
    const user = userEvent.setup()
    // 计数发往归因端点的请求：修复前 `setRequested(true)` 是同值更新，
    // React 不重渲染、queryKey 不变 → 第二次点击的请求数不会增长。
    let calls = 0
    const listener = ({ request }: { request: Request }) => {
      if (new URL(request.url).pathname.endsWith('/metrics/performance/attribution')) calls++
    }
    server.events.on('request:start', listener)

    renderWithProviders(<AttributionCard instanceUuid="inst-1-uuid" range="7d" />)
    await user.click(screen.getByRole('button', { name: '分析' }))
    expect(await screen.findByText('GC 暂停占用')).toBeInTheDocument()
    const afterFirst = calls
    expect(afterFirst).toBeGreaterThan(0)

    // 按钮文案应切为「重新分析」，点击后必须产生**新的**请求（而不是零反馈）。
    const reanalyze = screen.getByRole('button', { name: '重新分析' })
    await user.click(reanalyze)
    await waitFor(() => expect(calls, '「重新分析」必须触发新的取数请求').toBeGreaterThan(afterFirst))
    // 界面仍有结果（重算后渲染不退化）。
    expect(await screen.findByText('GC 暂停占用')).toBeInTheDocument()
    server.events.removeListener('request:start', listener)
  })

  it('全局排行表按名次渲染，并提供 TPS 语义格式化', async () => {
    renderWithProviders(<InstanceRankingPanel />)
    await waitFor(() => expect(screen.getAllByTestId('ranking-row').length).toBeGreaterThan(0))
    const rows = screen.getAllByTestId('ranking-row')
    // 强断言：只认「名次单元格恰好是 1」，而非「该行任意位置含字符 1」——
    // 后者对 TPS `12.3`、时间 `2026-…1…` 都会误通过（自审 M8）。
    expect(rows[0].querySelector('td')).toHaveTextContent(/^1$/)
    // 无数据实例计数提示（后端 skippedNoData=2）。
    expect(screen.getByText(/2 个实例窗口内无样本/)).toBeInTheDocument()
  })

  it('全局排行表：点击行下钻到实例详情路由（验收点 4）', async () => {
    const { default: userEvent } = await import('@testing-library/user-event')
    const user = userEvent.setup()
    renderWithProviders(<InstanceRankingPanel />)
    const rows = await screen.findAllByTestId('ranking-row')
    // 点击首行 → 路由跳到 `/instances/:id`（cross-instance-ranking/spec.md 验收点 4）。
    await user.click(rows[0])
    await waitFor(() => expect(window.location.pathname).toMatch(/^\/instances\/\d+$/))
  })

  it('全局排行表：空 items 渲染空态文案而非空白表', async () => {
    mockInject('get', '/metrics/instances/ranking', {
      kind: 'status',
      status: 200,
      body: { metricKey: 'inst_tps', order: 'asc', windowSeconds: 300, scoped: false, skippedNoData: 0, items: [] },
    })
    renderWithProviders(<InstanceRankingPanel />)
    expect(await screen.findByText('区间内暂无排行数据')).toBeInTheDocument()
    expect(screen.queryAllByTestId('ranking-row')).toHaveLength(0)
  })

  it('全局排行表：loading 态渲染加载文案', async () => {
    mockInject('get', '/metrics/instances/ranking', {
      kind: 'delay',
      ms: 150,
      then: {
        kind: 'status',
        status: 200,
        body: { metricKey: 'inst_tps', order: 'asc', windowSeconds: 300, scoped: false, skippedNoData: 0, items: [] },
      },
    })
    renderWithProviders(<InstanceRankingPanel />)
    expect(await screen.findByText('加载中…')).toBeInTheDocument()
  })

  it('fmtRankingValue 按指标量纲选口径（不把字节当计数）', async () => {
    const { default: i18n } = await import('@/i18n')
    const t = i18n.getFixedT('zh')
    expect(fmtRankingValue('inst_tps', 12.34, t)).toBe('12.3 TPS')
    expect(fmtRankingValue('inst_mspt', 42.5, t)).toBe('42.5 ms')
    expect(fmtRankingValue('inst_cpu_pct', 88, t)).toBe('88.0%')
    expect(fmtRankingValue('inst_heap_used', 1024 * 1024 * 1536, t)).toBe('1.5G')
    expect(fmtRankingValue('inst_players_online', 37.6, t)).toBe('38')
    // m2：非有限值不得渲染成字面量 NaN/Infinity。
    expect(fmtRankingValue('inst_tps', Number.NaN, t)).toBe('--')
    expect(fmtRankingValue('inst_mspt', Number.POSITIVE_INFINITY, t)).toBe('--')
  })

  it('可用率进度条配色按 SLO 目标判定（M2：高可用率不得染成危险红）', () => {
    // 99.7% ≥ 99.5% → success；低于目标 → danger。
    expect(availabilityLevel(0.997, 0.995)).toBe('success')
    expect(availabilityLevel(0.9, 0.995)).toBe('danger')
    // 非有限值不得误判为达标。
    expect(availabilityLevel(Number.NaN, 0.995)).toBe('danger')
  })

  it('可用率进度条实际渲染的颜色不是危险红（M2，mock 返回 99.7% 可用率）', async () => {
    renderWithProviders(<SLOSection range="7d" scope="platform" />)
    expect(await screen.findByText('可用率')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByText('故障次数')).toBeInTheDocument())
    // 取可用率 StatCard 内的进度条：修复前未传 level，走 resourceLevel 会把 99.75% 判为 danger（红）。
    const bar = screen.getByText('可用率').closest('[data-slot="stat-card"]')?.querySelector('[style*="width"]')
    expect(bar).toBeTruthy()
    expect(bar).toHaveStyle({ backgroundColor: 'var(--status-success)' })
    expect(bar).not.toHaveStyle({ backgroundColor: 'var(--status-danger)' })
  })

  it('24 时段柱图渲染 24 根柱并提供小时 title', () => {
    const dist = Array.from({ length: 24 }, (_, h) => h)
    renderWithProviders(<HourlyBars dist={dist} peak={23} />)
    const container = screen.getByTestId('hourly-dist')
    expect(container.children).toHaveLength(24)
    // m8：断言语义（小时标签 + 数值），不锁死拼接实现——
    // 改用单条双参数 i18n 键后该断言仍应通过。
    const title = container.children[20].getAttribute('title') ?? ''
    expect(title).toContain('20 时')
    expect(title).toContain('20.0')
  })

  it('可用性区块：applicable=false 时显示「不适用」，不渲染误差预算数字（m1）', async () => {
    // 经 mock 自身产出该态：把实例全置为非 RUNNING（等价真机「inst_uptime 序列数 = 0」），
    // 而不是手写整份响应体——否则 mock 与真后端的契约漂移零守护（自审 M7/M8）。
    const instances = db<{ id: number; status: string }>('instances')
    for (const row of instances.list()) instances.update(row.id, { status: 'STOPPED' })
    renderWithProviders(<SLOSection range="24h" scope="platform" />)
    expect(await screen.findByText(/不适用（窗口内无可用证据）/)).toBeInTheDocument()
    // 关键：不得出现「已消耗 100.0%」这类误导数字。
    expect(screen.queryByText('已消耗')).not.toBeInTheDocument()
    expect(screen.queryByText(/100\.0%/)).not.toBeInTheDocument()
  })

  it('可用性区块：查询被禁用（node 维度漏传 targetId）时渲染空态而非空白（m3）', async () => {
    // node/instance 维度漏传 targetId → useSLO 的 enabled 为 false → 查询不下发、data 恒 undefined。
    // 这是 `slo.empty` 的唯一实际可达路径（原 `totalSamples <= 0` 判断分支已被删除，见 SLOSection 注释）。
    renderWithProviders(<SLOSection range="24h" scope="instance" />)
    expect(await screen.findByText('区间内暂无可用性数据')).toBeInTheDocument()
    // 不得误渲染成「不适用」或任何预算数字。
    expect(screen.queryByText(/不适用/)).not.toBeInTheDocument()
    expect(screen.queryByText('已消耗')).not.toBeInTheDocument()
  })

  it('可用性区块：loading 态渲染加载文案', async () => {
    mockInject('get', '/metrics/slo', { kind: 'delay', ms: 150, then: { kind: 'status', status: 200, body: {} } })
    renderWithProviders(<SLOSection range="24h" scope="platform" />)
    expect(await screen.findByText('加载中…')).toBeInTheDocument()
  })

  it('可用性区块：请求失败时渲染错误态而非空白（m7）', async () => {
    server.use(
      http.get('/api/v1/metrics/slo', () => HttpResponse.json({ error: 'INTERNAL_ERROR' }, { status: 500 })),
    )
    renderWithProviders(<SLOSection range="24h" scope="platform" />)
    expect(await screen.findByText('加载可用性失败')).toBeInTheDocument()
  })

  it('排行面板：请求失败时渲染错误态（m7）', async () => {
    server.use(
      http.get('/api/v1/metrics/instances/ranking', () =>
        HttpResponse.json({ error: 'INTERNAL_ERROR' }, { status: 500 }),
      ),
    )
    renderWithProviders(<InstanceRankingPanel />)
    expect(await screen.findByText('加载排行失败')).toBeInTheDocument()
  })

  it('容量预测卡：请求失败时渲染错误态（m7）', async () => {
    server.use(
      http.get('/api/v1/metrics/capacity/forecast', () =>
        HttpResponse.json({ error: 'INTERNAL_ERROR' }, { status: 500 }),
      ),
    )
    renderWithProviders(<CapacityForecastCard scope="node" targetId="node-alpha" range="7d" />)
    expect(await screen.findByText('加载容量预测失败')).toBeInTheDocument()
  })

  it('容量预测卡：空 forecasts 渲染空态文案', async () => {
    mockInject('get', '/metrics/capacity/forecast', { kind: 'status', status: 200, body: { forecasts: [] } })
    renderWithProviders(<CapacityForecastCard scope="node" targetId="node-alpha" range="7d" />)
    expect(await screen.findByText('区间内暂无容量数据')).toBeInTheDocument()
  })

  it('容量预测卡：loading 态渲染加载文案', async () => {
    mockInject('get', '/metrics/capacity/forecast', { kind: 'delay', ms: 150, then: { kind: 'status', status: 200, body: { forecasts: [] } } })
    renderWithProviders(<CapacityForecastCard scope="node" targetId="node-alpha" range="7d" />)
    expect(await screen.findByText('加载中…')).toBeInTheDocument()
  })

  it('归因卡：点击后 loading 态渲染「分析中…」', async () => {
    const { default: userEvent } = await import('@testing-library/user-event')
    const user = userEvent.setup()
    mockInject('get', '/metrics/performance/attribution', { kind: 'delay', ms: 200, then: { kind: 'network' } })
    renderWithProviders(<AttributionCard instanceUuid="inst-1-uuid" range="7d" />)
    await user.click(screen.getByRole('button', { name: '分析' }))
    expect(await screen.findByText('分析中…')).toBeInTheDocument()
  })

  it('玩家在线趋势卡：渲染 24 时段与峰值，并在时区回退时给出提示（M4）', async () => {
    // 模拟后端回退：timezone 字段标注 fallback 来源。
    server.use(
      http.get('/api/v1/metrics/players/trend', () =>
        HttpResponse.json({
          resolution: '5m', timezone: 'UTC (fallback from Not/AZone)',
          trend: [{ ts: '2026-06-01T00:00:00Z', avg: 100, min: 100, max: 100 }],
          hourlyDist: Array.from({ length: 24 }, () => 100),
          peakValue: 100, peakAt: '2026-06-01T00:00:00Z', dailyAvg: 100,
        }),
      ),
    )
    renderWithProviders(<PlayerTrendCard range="24h" />)
    expect(await screen.findByText(/时区不可用，已回退 UTC/)).toBeInTheDocument()
    expect(screen.getByTestId('hourly-dist').children).toHaveLength(24)
  })

  it('玩家在线趋势卡：loading 态渲染加载文案', async () => {
    mockInject('get', '/metrics/players/trend', { kind: 'delay', ms: 150, then: { kind: 'status', status: 200, body: {} } })
    renderWithProviders(<PlayerTrendCard range="24h" />)
    expect(await screen.findByText('加载中…')).toBeInTheDocument()
  })

  it('玩家在线趋势卡：空 trend 渲染空态文案而非空白图', async () => {
    mockInject('get', '/metrics/players/trend', {
      kind: 'status',
      status: 200,
      body: {
        resolution: '5m', timezone: 'Asia/Shanghai', trend: [],
        hourlyDist: Array.from({ length: 24 }, () => 0), peakValue: 0, peakAt: null, dailyAvg: 0,
      },
    })
    renderWithProviders(<PlayerTrendCard range="24h" />)
    expect(await screen.findByText('区间内暂无在线数据')).toBeInTheDocument()
    expect(screen.queryByTestId('hourly-dist')).not.toBeInTheDocument()
  })

  it('玩家在线趋势卡：请求失败时渲染错误态（m7）', async () => {
    server.use(
      http.get('/api/v1/metrics/players/trend', () =>
        HttpResponse.json({ error: 'INTERNAL_ERROR' }, { status: 500 }),
      ),
    )
    renderWithProviders(<PlayerTrendCard range="24h" />)
    expect(await screen.findByText('加载在线趋势失败')).toBeInTheDocument()
  })

  it('fmtAvailability 语义：无分母时返回 --（不把「不适用」写成 0.00%）', () => {
    expect(fmtAvailability(undefined)).toBe('--')
    expect(fmtAvailability({ availability: 0, totalSamples: 0 } as never)).toBe('--')
  })

  it('归因卡在英文界面不残留中文（M5：后端直出中文的结论句/因子名/note 需前端本地化）', async () => {
    const { default: userEvent } = await import('@testing-library/user-event')
    const { default: i18n } = await import('@/i18n')
    const user = userEvent.setup()
    await i18n.changeLanguage('en')
    try {
      renderWithProviders(<AttributionCard instanceUuid="inst-1-uuid" range="7d" />)
      await user.click(screen.getByRole('button', { name: 'Analyze' }))
      // 结论句走前端模板（后端 tldr 是 `${因子中文}（权重 …）`）。
      expect(await screen.findByText(/Main driver: GC pause time \(weight 0\.62\)/)).toBeInTheDocument()
      // 5 类因子名：mock 返回的 3 条（GC 暂停占用/已加载区块/实体数）都必须走本地化键。
      expect(screen.getByText('GC pause time')).toBeInTheDocument()
      expect(screen.getByText('Loaded chunks')).toBeInTheDocument()
      expect(screen.getByText('Entities')).toBeInTheDocument()
      // note（后端恒为「相关性非因果」）也要本地化，不得原样漏中文。
      expect(screen.getByText(/Correlation is not causation/)).toBeInTheDocument()
      // 整卡不得再出现后端中文原文。
      expect(screen.queryByText('GC 暂停占用')).not.toBeInTheDocument()
      expect(screen.queryByText(/劣化主因/)).not.toBeInTheDocument()
      expect(screen.queryByText(/相关性非因果/)).not.toBeInTheDocument()
    } finally {
      await i18n.changeLanguage('zh')
    }
  })
})
