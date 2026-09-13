import { describe, it, expect, beforeAll, vi } from 'vitest'
import { Route, Routes } from 'react-router'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { useAuthStore } from '@/stores/auth'
import { mockInject } from '@jianmanager/devmock/inject'
import { server } from '@jianmanager/devmock/server'
import ProtectionCenterPage from './ProtectionCenterPage'
import ClientDistRedirect from './ClientDistRedirect'
import Workspace from '@/components/console/Workspace'

/**
 * 页面 B「客户端分发运维」（`/client-dist-ops`，FR-430 / ADR-088）。
 *
 * 用例迁自旧 `ClientDistMonitoringPage.dom.test.tsx`（8 例）+ 旧 `ProtectionCenterPage` 安全侧行为：
 * - 7 Tab 同页；`data-page="client-dist-ops"`；
 * - 统计/实时监控/全量日志(request)/机器·客户端 各 Tab 数据；
 * - 旧两条路由参数翻译重定向；两页平台管理员守卫（非管理员 → `/`）。
 */
vi.mock('@/pages/OverviewPage', () => ({ default: () => <h1>平台首页</h1> }))

const ADMIN_TOKEN = `mock.${btoa(
  JSON.stringify({ userId: 1, username: 'admin', role: 10, exp: Math.floor(Date.now() / 1000) + 900 }),
)}.sig`
const MEMBER_TOKEN = `mock.${btoa(
  JSON.stringify({ userId: 2, username: 'bob', role: 0, exp: Math.floor(Date.now() / 1000) + 900 }),
)}.sig`

function loginAs(token: string) {
  loginMockUser(token)
  useAuthStore.getState().login(token, 'test-refresh-token')
}

beforeAll(() => {
  if (!('ResizeObserver' in globalThis)) {
    class RO {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
    ;(globalThis as { ResizeObserver?: unknown }).ResizeObserver = RO
  }
  if (!Element.prototype.scrollIntoView) Element.prototype.scrollIntoView = () => {}
  if (!Element.prototype.hasPointerCapture) Element.prototype.hasPointerCapture = () => false
  if (!Element.prototype.setPointerCapture) Element.prototype.setPointerCapture = () => {}
})

describe('ClientDistOpsPage 外壳（7 Tab / data-page / 守卫）', () => {
  it('平台管理员默认落「总览」：标题、data-page、7 Tab 齐备', async () => {
    loginAs(ADMIN_TOKEN)
    const { container } = renderWithProviders(<ProtectionCenterPage />, { route: '/client-dist-ops' })

    expect(container.firstElementChild).toHaveAttribute('data-page', 'client-dist-ops')
    expect(container.firstElementChild).toHaveClass('jm-page-stack')
    expect(await screen.findByRole('heading', { name: '客户端分发运维' })).toBeInTheDocument()

    for (const name of ['总览', '统计', '实时监控', '全量日志', '机器 / 客户端', '画像', '处置']) {
      expect(screen.getByRole('tab', { name })).toBeInTheDocument()
    }
    // 总览默认内容：分发健康 + 安全态势 + 分区齐备；无 FR 徽章与副标题。
    expect(await screen.findByTestId('ops-overview')).toBeInTheDocument()
    expect(await screen.findByText('活跃下载')).toBeInTheDocument()
    expect(await screen.findByText('分发健康')).toBeInTheDocument()
    expect(await screen.findByText('安全态势')).toBeInTheDocument()
    // InsightCards 独占「更新成功率」（速览区已去重）。
    expect(await screen.findByText('更新成功率')).toBeInTheDocument()
    expect(await screen.findByText('请求成功率')).toBeInTheDocument()
    expect(await screen.findByText('拉取趋势')).toBeInTheDocument()
    expect(await screen.findByText('版本滞后分布')).toBeInTheDocument()
    expect(await screen.findByText('安全排行')).toBeInTheDocument()
    expect(screen.queryByText('FR-430')).not.toBeInTheDocument()
  })

  it('旧别名 tab（ip）直达页面 B 落到「画像 / IP 剖析」分档', async () => {
    loginAs(ADMIN_TOKEN)
    renderWithProviders(<ProtectionCenterPage />, { route: '/client-dist-ops?tab=ip' })

    expect(await screen.findByRole('tab', { name: '画像' })).toHaveAttribute('data-state', 'active')
    expect(await screen.findByRole('tab', { name: 'IP 剖析' })).toHaveAttribute('data-state', 'active')
  })
})

describe('ClientDistOpsPage 区块（迁自旧监控页用例）', () => {
  it('统计 Tab：跨频道口径 + 请求侧指标，无更新成功率（CAS）', async () => {
    loginAs(ADMIN_TOKEN)
    renderWithProviders(<ProtectionCenterPage />, { route: '/client-dist-ops?tab=statistics' })

    expect(await screen.findByText('请求成功率')).toBeInTheDocument()
    expect(screen.getByText('请求结果分布')).toBeInTheDocument()
    expect(screen.getByTestId('ops-granularity')).toHaveTextContent('跨频道口径')
    expect(screen.queryByText('更新成功率')).not.toBeInTheDocument()
    expect(screen.queryByText(/CAS/i)).not.toBeInTheDocument()
  })

  it('实时监控 Tab（seg=live）：近实时聚合出数', async () => {
    loginAs(ADMIN_TOKEN)
    renderWithProviders(<ProtectionCenterPage />, { route: '/client-dist-ops?tab=monitor' })

    expect(await screen.findByText('近 1h 错误')).toBeInTheDocument()
    expect(screen.getByText('近 24h 请求速率')).toBeInTheDocument()
    expect(screen.getByText('最近错误请求')).toBeInTheDocument()
    // 错误码 TopN / 最近错误可能同屏出现多次。
    expect((await screen.findAllByText('INVALID_CLIENT_KEY')).length).toBeGreaterThanOrEqual(1)
  })

  it('全量日志 Tab（缺省 type=request）：加厚列 + 脱敏详情，密钥仅 present', async () => {
    loginAs(ADMIN_TOKEN)
    const user = userEvent.setup()
    renderWithProviders(<ProtectionCenterPage />, { route: '/client-dist-ops?tab=logs' })

    expect(await screen.findByText('分发请求日志')).toBeInTheDocument()
    expect(await screen.findByRole('columnheader', { name: '错误码' })).toBeInTheDocument()
    expect(screen.getByText('INVALID_CLIENT_KEY')).toBeInTheDocument()

    const detailButtons = await screen.findAllByRole('button', { name: '详情' })
    await user.click(detailButtons[0])

    expect(await screen.findByText('请求脱敏详情')).toBeInTheDocument()
    expect(screen.getByText('请求头（已脱敏）')).toBeInTheDocument()
    expect(screen.getByText('X-Client-Key')).toBeInTheDocument()
    expect(screen.getByText('present')).toBeInTheDocument()
    expect(screen.queryByText('secret')).not.toBeInTheDocument()
  })

  it('全量日志 Tab（type=all）：聚合多类型表出 hello / telemetry 等', async () => {
    loginAs(ADMIN_TOKEN)
    renderWithProviders(<ProtectionCenterPage />, { route: '/client-dist-ops?tab=logs&type=all' })

    expect(await screen.findByText('全量日志详情')).toBeInTheDocument()
    const table = await screen.findByRole('table')
    expect(within(table).getByText('Security Hello')).toBeInTheDocument()
    expect(within(table).getAllByText('更新遥测').length).toBeGreaterThan(0)
  })

  it('错误码 TopN 点击 → 页内切「全量日志(request)」并按错误码过滤', async () => {
    loginAs(ADMIN_TOKEN)
    const user = userEvent.setup()
    renderWithProviders(<ProtectionCenterPage />, { route: '/client-dist-ops?tab=monitor' })
    await screen.findByText('近 1h 错误')

    const topErrors = await screen.findByText('错误码 Top 10')
    const panel = topErrors.closest('[data-slot="panel"]') ?? topErrors.parentElement
    expect(panel).not.toBeNull()
    await user.click(within(panel as HTMLElement).getByRole('button', { name: /INVALID_CLIENT_KEY/ }))

    expect(await screen.findByText('已联动过滤日志：')).toBeInTheDocument()
    expect(screen.getByText('errCode=INVALID_CLIENT_KEY')).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: '玩家名' })).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: 'Core 版本' })).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: '字节' })).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: '耗时' })).toBeInTheDocument()
    expect(screen.getAllByText('INVALID_CLIENT_KEY').length).toBeGreaterThanOrEqual(1)
    expect(screen.queryByText('ARTIFACT_NOT_FOUND')).not.toBeInTheDocument()
  })

  it('机器 / 客户端 Tab：运行态 KPI + 机器行「看日志」联动请求日志过滤', async () => {
    loginAs(ADMIN_TOKEN)
    const user = userEvent.setup()
    renderWithProviders(<ProtectionCenterPage />, { route: '/client-dist-ops?tab=clients' })

    expect(await screen.findByText('近 5 分钟启动')).toBeInTheDocument()
    expect(screen.getByText('今日启动')).toBeInTheDocument()
    expect(screen.getByText('客户端运行态')).toBeInTheDocument()

    const matches = await screen.findAllByText('m-aaaa')
    const row = matches
      .map((el) => el.closest('tr'))
      .find((tr) => tr && within(tr).queryByRole('button', { name: '看日志' }))
    expect(row).not.toBeNull()
    await user.click(within(row as HTMLElement).getByRole('button', { name: '看日志' }))

    expect(await screen.findByText('已联动过滤日志：')).toBeInTheDocument()
    expect(screen.getByText('machine=m-aaaa')).toBeInTheDocument()
    expect(screen.queryByText('m-bbbb')).not.toBeInTheDocument()
  })

  it('处置 Tab（seg=actions）：三块处置表单 + 动作表齐备', async () => {
    loginAs(ADMIN_TOKEN)
    renderWithProviders(<ProtectionCenterPage />, { route: '/client-dist-ops?tab=actions' })

    expect(await screen.findByRole('tab', { name: '处置' })).toHaveAttribute('data-state', 'active')
    expect(await screen.findByRole('tab', { name: '封禁与降级' })).toHaveAttribute('data-state', 'active')
    // S2.1：常驻表单改为顶部动作按钮 + 模态；主区为动作流水表。
    expect(await screen.findByTestId('action-open-block-ip')).toBeInTheDocument()
    expect(screen.getByTestId('action-open-key-state')).toBeInTheDocument()
    expect(screen.getByTestId('action-open-protection')).toBeInTheDocument()
    expect(await screen.findByText('动作流水')).toBeInTheDocument()
  })

  it('带 query 打开全量日志（type=all）预填筛选；无「打开分发监控」，跨页链接指向新运维页', async () => {
    loginAs(ADMIN_TOKEN)
    renderWithProviders(<ProtectionCenterPage />, {
      route: '/client-dist-ops?channelId=skyblock-s1&ip=192.0.2.9&machineId=machine-1&errCode=RATE_SPIKE&version=3&tab=logs&type=all',
    })

    expect(await screen.findByDisplayValue('skyblock-s1')).toBeInTheDocument()
    expect(screen.getByDisplayValue('192.0.2.9')).toBeInTheDocument()
    expect(screen.getByDisplayValue('machine-1')).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: '打开分发监控' })).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: '打开频道工作台' })).toHaveAttribute(
      'href',
      '/client-channels?channelId=skyblock-s1&ip=192.0.2.9&machineId=machine-1&errCode=RATE_SPIKE&version=3&tab=stats&type=all',
    )
  })

  it('端点错误：统计 Tab 降级为错误态、不崩溃', async () => {
    loginAs(ADMIN_TOKEN)
    mockInject('get', '/client-dist/stats', { kind: 'status', status: 500 })
    renderWithProviders(<ProtectionCenterPage />, { route: '/client-dist-ops?tab=statistics' })

    expect(await screen.findByRole('heading', { name: '客户端分发运维' })).toBeInTheDocument()
    expect(await screen.findByText('加载分发请求观测失败')).toBeInTheDocument()
    expect(screen.queryByText('请求成功率')).not.toBeInTheDocument()
  })
})

describe('页面 B 路由重定向与守卫（Workspace）', () => {
  it('/client-dist-security?tab=ip&channelId=x → 透传 query 重定向到 /client-dist-ops?tab=profiles&seg=ip', async () => {
    loginAs(ADMIN_TOKEN)
    renderWithProviders(<Workspace />, { route: '/client-dist-security?tab=ip&channelId=skyblock-s1' })

    await waitFor(() => expect(window.location.pathname).toBe('/client-dist-ops'))
    expect(window.location.search).toBe('?channelId=skyblock-s1&tab=profiles&seg=ip')
    expect(await screen.findByRole('heading', { name: '客户端分发运维' })).toBeInTheDocument()
  })

  it('/client-dist-monitor?tab=logs&channelId=x → 补 type=request 重定向', async () => {
    loginAs(ADMIN_TOKEN)
    renderWithProviders(<Workspace />, { route: '/client-dist-monitor?tab=logs&channelId=skyblock-s1' })

    await waitFor(() => expect(window.location.pathname).toBe('/client-dist-ops'))
    expect(window.location.search).toBe('?channelId=skyblock-s1&tab=logs&type=request')
  })

  it('重定向透传全部冻结 query（channelId/ip/machineId/errCode/version/from/to）', async () => {
    loginAs(ADMIN_TOKEN)
    renderWithProviders(<Workspace />, {
      route:
        '/client-dist-monitor?tab=logs&channelId=c1&ip=10.0.0.1&machineId=m1&errCode=E1&version=4&from=2026-09-01T00%3A00%3A00Z&to=2026-09-03T00%3A00%3A00Z',
    })

    await waitFor(() => expect(window.location.pathname).toBe('/client-dist-ops'))
    const params = new URLSearchParams(window.location.search)
    expect(params.get('tab')).toBe('logs')
    expect(params.get('type')).toBe('request')
    expect(params.get('channelId')).toBe('c1')
    expect(params.get('ip')).toBe('10.0.0.1')
    expect(params.get('machineId')).toBe('m1')
    expect(params.get('errCode')).toBe('E1')
    expect(params.get('version')).toBe('4')
    expect(params.get('from')).toBe('2026-09-01T00:00:00Z')
    expect(params.get('to')).toBe('2026-09-03T00:00:00Z')
  })

  it('旧安全页别名 tab 全量覆盖（events/players/groups → 对应新 Tab/分档）', async () => {
    loginAs(ADMIN_TOKEN)
    renderWithProviders(<Workspace />, { route: '/client-dist-security?tab=groups' })
    await waitFor(() => expect(window.location.pathname).toBe('/client-dist-ops'))
    expect(window.location.search).toBe('?tab=actions&seg=groups')
  })

  it('非平台管理员直达 /client-dist-ops → 重定向首页（守卫）', async () => {
    loginAs(MEMBER_TOKEN)
    renderWithProviders(<Workspace />, { route: '/client-dist-ops' })

    expect(await screen.findByRole('heading', { name: '平台首页' })).toBeInTheDocument()
    expect(window.location.pathname).toBe('/')
    expect(screen.queryByRole('heading', { name: '客户端分发运维' })).not.toBeInTheDocument()
  })

  it('非平台管理员直达 /client-channels → 重定向首页（守卫）', async () => {
    loginAs(MEMBER_TOKEN)
    renderWithProviders(<Workspace />, { route: '/client-channels' })

    expect(await screen.findByRole('heading', { name: '平台首页' })).toBeInTheDocument()
    expect(window.location.pathname).toBe('/')
  })

  it('非平台管理员直达旧安全中心路由 → 重定向路由不拦截、最终由目标守卫落到首页', async () => {
    loginAs(MEMBER_TOKEN)
    renderWithProviders(<Workspace />, { route: '/client-dist-security?tab=ip&channelId=skyblock-s1' })

    expect(await screen.findByRole('heading', { name: '平台首页' })).toBeInTheDocument()
    expect(window.location.pathname).toBe('/')
  })
})

/**
 * QA 独立补充（FR-430 证伪式复核）：旧路由重定向穷举 + 分档渲染补齐。
 * 隔离渲染两条重定向路由 + 目标桩，逐条核对 §2 映射表与冻结 query 透传（不依赖整页守卫）。
 */
function RedirectProbe() {
  return (
    <Routes>
      <Route path="/client-dist-security" element={<ClientDistRedirect source="security" />} />
      <Route path="/client-dist-monitor" element={<ClientDistRedirect source="monitor" />} />
      <Route path="/client-dist-ops" element={<div data-testid="ops-landing">OPS</div>} />
    </Routes>
  )
}

describe('页面 B 补充：旧路由重定向穷举（QA 独立补充）', () => {
  const redirectCases: { route: string; search: string }[] = [
    // —— 旧安全中心 /client-dist-security ——
    { route: '/client-dist-security?tab=overview', search: '?tab=overview' },
    { route: '/client-dist-security?tab=logs', search: '?tab=logs' },
    { route: '/client-dist-security?tab=events', search: '?tab=monitor&seg=events' },
    { route: '/client-dist-security?tab=profiles', search: '?tab=profiles&seg=client' },
    { route: '/client-dist-security?tab=ip', search: '?tab=profiles&seg=ip' },
    { route: '/client-dist-security?tab=players', search: '?tab=profiles&seg=player' },
    { route: '/client-dist-security?tab=actions', search: '?tab=actions&seg=actions' },
    { route: '/client-dist-security?tab=groups', search: '?tab=actions&seg=groups' },
    { route: '/client-dist-security?tab=bogus', search: '?tab=overview' },
    { route: '/client-dist-security', search: '?tab=overview' },
    // 旧安全页带 type（聚合视图）须保真透传，不被 request 覆盖
    { route: '/client-dist-security?tab=logs&type=all', search: '?tab=logs&type=all' },
    // seg 与 type 同时出现：logs 归一化不派生 seg/type，两者原样透传
    { route: '/client-dist-security?tab=logs&type=all&seg=groups', search: '?tab=logs&type=all&seg=groups' },
    // —— 旧监控 /client-dist-monitor ——
    { route: '/client-dist-monitor?tab=statistics', search: '?tab=statistics' },
    { route: '/client-dist-monitor?tab=monitor', search: '?tab=monitor' },
    { route: '/client-dist-monitor?tab=logs', search: '?tab=logs&type=request' },
    { route: '/client-dist-monitor?tab=clients', search: '?tab=clients' },
    { route: '/client-dist-monitor?tab=bogus', search: '?tab=statistics' },
    { route: '/client-dist-monitor', search: '?tab=statistics' },
    // 历史 channel 键 → channelId；冻结键顺序 channelId,ip,machineId,errCode,version,tab,type,seg,from,to
    { route: '/client-dist-monitor?tab=clients&channel=legacy&ip=1.2.3.4', search: '?channelId=legacy&ip=1.2.3.4&tab=clients' },
  ]

  it.each(redirectCases)('$route → $search', async ({ route, search }) => {
    renderWithProviders(<RedirectProbe />, { route })
    await waitFor(() => expect(window.location.pathname).toBe('/client-dist-ops'))
    expect(window.location.search).toBe(search)
    expect(await screen.findByTestId('ops-landing')).toBeInTheDocument()
  })
})

describe('页面 B 补充：分档 seg 渲染补齐（QA 独立补充）', () => {
  it('实时监控 seg=events（直达 canonical）：渲染异常请求分析面板', async () => {
    loginAs(ADMIN_TOKEN)
    renderWithProviders(<ProtectionCenterPage />, { route: '/client-dist-ops?tab=monitor&seg=events' })

    expect(await screen.findByRole('tab', { name: '异常请求' })).toHaveAttribute('data-state', 'active')
    expect(await screen.findByText('异常请求分析')).toBeInTheDocument()
  })

  it('画像 seg=player：渲染玩家名剖析面板', async () => {
    loginAs(ADMIN_TOKEN)
    renderWithProviders(<ProtectionCenterPage />, { route: '/client-dist-ops?tab=profiles&seg=player' })

    expect(await screen.findByRole('tab', { name: '画像' })).toHaveAttribute('data-state', 'active')
    expect(await screen.findByRole('tab', { name: '玩家名剖析' })).toHaveAttribute('data-state', 'active')
    expect((await screen.findAllByText('玩家名剖析')).length).toBeGreaterThanOrEqual(1)
  })

  it('画像 seg=client（直达）：渲染客户端画像面板', async () => {
    loginAs(ADMIN_TOKEN)
    renderWithProviders(<ProtectionCenterPage />, { route: '/client-dist-ops?tab=profiles&seg=client' })

    expect(await screen.findByRole('tab', { name: '客户端' })).toHaveAttribute('data-state', 'active')
    expect(await screen.findByText('客户端画像')).toBeInTheDocument()
  })

  it('处置 seg=groups：渲染安全分组（新建按钮 + 列表）', async () => {
    loginAs(ADMIN_TOKEN)
    renderWithProviders(<ProtectionCenterPage />, { route: '/client-dist-ops?tab=actions&seg=groups' })

    expect(await screen.findByRole('tab', { name: '处置' })).toHaveAttribute('data-state', 'active')
    expect(await screen.findByRole('tab', { name: '安全分组' })).toHaveAttribute('data-state', 'active')
    expect(await screen.findByTestId('action-open-create-group')).toBeInTheDocument()
    expect(await screen.findByText('高风险 IP')).toBeInTheDocument()
  })
})

/**
 * QA 独立补充（P0-2 证伪式复核）：查询按 Tab 懒加载——用 MSW 请求生命周期事件**实测**，
 * 而非读代码推断。断言：非 monitor Tab 不发 `/client-dist/realtime`；非 overview/clients Tab
 * 不发 `/client-dist/clients`（运行态）；`/client-dist/stats` 亦按 Tab 懒加载（仅 overview/statistics 发）。
 */
describe('页面 B 补充：查询按 Tab 懒加载（P0-2）', () => {
  function trackRequests(): { hits: string[]; stop: () => void } {
    const hits: string[] = []
    const listener = ({ request }: { request: Request }) => {
      hits.push(new URL(request.url).pathname)
    }
    server.events.on('request:start', listener)
    return { hits, stop: () => server.events.removeListener('request:start', listener) }
  }
  const count = (hits: string[], suffix: string) => hits.filter((p) => p.endsWith(suffix)).length

  it('logs Tab：不发 realtime / clients（运行态），stats 亦按 Tab 懒加载（不发）', async () => {
    const { hits, stop } = trackRequests()
    try {
      loginAs(ADMIN_TOKEN)
      renderWithProviders(<ProtectionCenterPage />, { route: '/client-dist-ops?tab=logs' })
      expect(await screen.findByText('分发请求日志')).toBeInTheDocument()
      // 页面已激活（有请求发出）后断言：logs ∉ {overview, statistics} → stats 不再恒发。
      expect(hits.length).toBeGreaterThan(0)
      await waitFor(() => expect(count(hits, '/client-dist/stats')).toBe(0))
      expect(count(hits, '/client-dist/realtime')).toBe(0)
      expect(count(hits, '/client-dist/clients')).toBe(0)
    } finally {
      stop()
    }
  })

  it('clients Tab：发 /client-dist/clients，但不发 realtime', async () => {
    const { hits, stop } = trackRequests()
    try {
      loginAs(ADMIN_TOKEN)
      renderWithProviders(<ProtectionCenterPage />, { route: '/client-dist-ops?tab=clients' })
      expect(await screen.findByText('客户端运行态')).toBeInTheDocument()
      await waitFor(() => expect(count(hits, '/client-dist/clients')).toBeGreaterThan(0))
      expect(count(hits, '/client-dist/realtime')).toBe(0)
    } finally {
      stop()
    }
  })

  it('monitor Tab：发 /client-dist/realtime', async () => {
    const { hits, stop } = trackRequests()
    try {
      loginAs(ADMIN_TOKEN)
      renderWithProviders(<ProtectionCenterPage />, { route: '/client-dist-ops?tab=monitor' })
      expect(await screen.findByText('近 1h 错误')).toBeInTheDocument()
      await waitFor(() => expect(count(hits, '/client-dist/realtime')).toBeGreaterThan(0))
    } finally {
      stop()
    }
  })

  it('statistics Tab：不误发 realtime（error-summary 另算）', async () => {
    const { hits, stop } = trackRequests()
    try {
      loginAs(ADMIN_TOKEN)
      renderWithProviders(<ProtectionCenterPage />, { route: '/client-dist-ops?tab=statistics' })
      expect(await screen.findByText('请求成功率')).toBeInTheDocument()
      await waitFor(() => expect(count(hits, '/client-dist/error-summary')).toBeGreaterThan(0))
      expect(count(hits, '/client-dist/realtime')).toBe(0)
    } finally {
      stop()
    }
  })

  /**
   * B（statsQuery 收敛）回归锁：`/client-dist/stats` 仅在 overview / statistics 发。
   * 逐 Tab 实测——正例轮询命中；反例在「本页确已发出其它请求」后限定短窗口观察，
   * 确认其从未发出（stats 为挂载期查询，本页任何请求入账即代表启用查询均已发出）。
   */
  const statsByTab: { tab: string; landmark: string; stats: boolean }[] = [
    { tab: 'overview', landmark: '分发健康', stats: true },
    { tab: 'statistics', landmark: '请求成功率', stats: true },
    { tab: 'monitor', landmark: '近 1h 错误', stats: false },
    { tab: 'logs', landmark: '分发请求日志', stats: false },
    { tab: 'clients', landmark: '客户端运行态', stats: false },
    { tab: 'profiles', landmark: '客户端画像', stats: false },
    { tab: 'actions', landmark: '动作流水', stats: false },
  ]

  it.each(statsByTab)(
    '$tab Tab：stats 命中=$stats（仅 overview/statistics 发 /client-dist/stats）',
    async ({ tab, landmark, stats }) => {
      const { hits, stop } = trackRequests()
      try {
        loginAs(ADMIN_TOKEN)
        renderWithProviders(<ProtectionCenterPage />, { route: `/client-dist-ops?tab=${tab}` })
        expect(await screen.findByText(landmark)).toBeInTheDocument()
        // 本页确已发出请求（排除 MSW 监听未生效导致的空跑假阳性）。
        expect(hits.length).toBeGreaterThan(0)
        if (stats) {
          await waitFor(() => expect(count(hits, '/client-dist/stats')).toBeGreaterThan(0))
        } else {
          await new Promise((r) => setTimeout(r, 250))
          expect(count(hits, '/client-dist/stats')).toBe(0)
        }
      } finally {
        stop()
      }
    },
  )
})

/**
 * QA 独立补充：P0-3 主 Tab 列表可访问名称；P0-4 非管理员分支去重标题 + adminOnly 运维口径。
 */
describe('页面 B 补充：a11y 名称与去重标题（P0-3/P0-4）', () => {
  it('主 Tab 列表具有可访问名称（aria-label=视图切换），且含 7 个 Tab', async () => {
    loginAs(ADMIN_TOKEN)
    renderWithProviders(<ProtectionCenterPage />, { route: '/client-dist-ops' })

    const tablist = await screen.findByRole('tablist', { name: '视图切换' })
    expect(within(tablist).getAllByRole('tab')).toHaveLength(7)
  })

  it('非管理员分支：仅一处「客户端分发运维」标题（Panel 不再重复），文案为运维口径，且不渲染 Tabs', async () => {
    loginAs(MEMBER_TOKEN)
    renderWithProviders(<ProtectionCenterPage />, { route: '/client-dist-ops' })

    expect(await screen.findByText('客户端分发运维需平台管理员权限')).toBeInTheDocument()
    // h1 恰好一次；若 Panel 仍带重复 title 会 >1。
    expect(screen.getAllByText('客户端分发运维')).toHaveLength(1)
    expect(screen.queryByRole('tablist')).not.toBeInTheDocument()
  })
})
