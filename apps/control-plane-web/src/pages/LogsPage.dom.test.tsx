import { describe, it, expect, beforeEach, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@jianmanager/devmock/inject'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import LogsPage from './LogsPage'

/**
 * LogsPage 强断言（FR-208）：验种子日志行渲染、关键字/级别筛选联动、错误注入显错误态。
 * LogsPage 还消费 /nodes、/instances 填筛选下拉——非本域 endpoint，本测试用 server.use 就地
 * 提供空数组桩（不在 domains/ 重定义别域 handler），满足 onUnhandledRequest:'error' 覆盖闸。
 *
 * FR-482：页面在节点/实例视图的数据源**只有**联邦 `/logs/federation`（用户裁定：查询服务
 * 不可用时不降级、不拿平台/历史日志冒充本视图的内容）。故本文件不再桩 404，让用例落到
 * devmock 的真实联邦 handler 上——跑的就是生产同款联邦路径；联邦的覆盖/失败态与导出门禁
 * 见 LogsPage.federation.dom.test.tsx。
 */
beforeEach(() => {
  server.use(
    http.get(API('/nodes'), () => HttpResponse.json([])),
    http.get(API('/instances'), () => HttpResponse.json([])),
  )
})

describe('LogsPage（mock 假后端）', () => {
  it('① 渲染出种子日志行', async () => {
    loginMockUser()
    renderWithProviders(<LogsPage />)
    expect(await screen.findByText(/Done \(12\.3s\)!/)).toBeInTheDocument()
    expect(screen.getByText(/failed to dispatch backup: disk full/)).toBeInTheDocument()
    expect(screen.getByText(/heartbeat sent to control-plane/)).toBeInTheDocument()
  })

    it('一年日志数据下只渲染可视窗口', async () => {
      loginMockUser()
      renderWithProviders(<LogsPage />)
      // 默认时间窗为最近 15 分钟（2026-09-30 性能依据）；本用例意图是「大时间跨度数据下的
      // 虚拟化渲染」，故显式切到「全部时间」，不依赖默认值。时间范围是 Select（combobox），非按钮。
      HTMLElement.prototype.hasPointerCapture ??= () => false
      HTMLElement.prototype.setPointerCapture ??= () => {}
      HTMLElement.prototype.releasePointerCapture ??= () => {}
      HTMLElement.prototype.scrollIntoView ??= () => {}
      await userEvent.click(screen.getAllByRole('combobox')[0])
      await userEvent.click(await screen.findByRole('option', { name: '全部时间' }))

    const surface = await screen.findByTestId('logs-virtual')
    // 计数语义：联邦视图没有服务端总数，页脚计数只能是**本页取回的行数**（保守下界，
    // 见 LogsPage 的 total 推导）＝分页大小 100。若要显示「共 12004 条」只能靠经典 /logs
    // 的服务端 total——那正是本视图不再消费的数据源。
    expect(Number(surface.dataset.totalCount)).toBe(100)
    // 视口 460px / 行高 30px ≈ 16 行，加上下各 8 行预渲染仍远少于 100：只渲染可视窗口。
    expect(screen.queryAllByTestId('log-row').length).toBeLessThan(40)
    // 行确实渲染出来（最新一条种子日志在窗口首位附近）。
    expect(await screen.findByText(/Done \(12\.3s\)!/)).toBeInTheDocument()
  })

  it('② 关键字筛选 → 列表联动收敛', async () => {
    loginMockUser()
    renderWithProviders(<LogsPage />)
    await screen.findByText(/Done \(12\.3s\)!/)

    await userEvent.type(screen.getByPlaceholderText('搜索日志内容…'), 'heartbeat')
    // 仅 worker debug 行含 heartbeat，其余消失（DB 侧 keyword 过滤联动）。
    expect(await screen.findByText(/heartbeat sent to control-plane/)).toBeInTheDocument()
    await waitFor(() => expect(screen.queryByText(/Done \(12\.3s\)!/)).not.toBeInTheDocument())
  })

  it('② 级别筛选 error → 仅错误行保留', async () => {
    loginMockUser()
    renderWithProviders(<LogsPage />)
    await screen.findByText(/Done \(12\.3s\)!/)

    // 级别快速 pill「错误」。
    await userEvent.click(screen.getByRole('button', { name: '错误', pressed: false }))
    expect(await screen.findByText(/failed to dispatch backup: disk full/)).toBeInTheDocument()
    await waitFor(() => expect(screen.queryByText(/Done \(12\.3s\)!/)).not.toBeInTheDocument())
  })

  it('FR-345 深链后的搜索、时间筛选与导出始终携带 instanceId', async () => {
    loginMockUser()
    const user = userEvent.setup()
    // 联邦视图下查询与导出都走联邦路径（经典 /logs 在本视图不参与），故这里记录联邦请求。
    const queryRequests: URL[] = []
    let exportedUrl: URL | null = null
    const createObjectURL = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:logs')
    const revokeObjectURL = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => undefined)
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined)
    server.use(
      http.get(API('/instances'), () => HttpResponse.json([
        { id: 2, uuid: 'stopped-2', nodeId: 1, name: '停机实例', type: 'minecraft_java', role: 'backend', processType: 'daemon', status: 'STOPPED', startCommand: 'java -jar server.jar', workDir: 'srv', serverPort: 25565, autoStart: false, autoRestart: true, tags: '', createdAt: '2026-07-18T00:00:00Z' },
      ])),
      http.get(API('/logs/federation'), ({ request }) => {
        queryRequests.push(new URL(request.url))
        return HttpResponse.json({
          ok: true,
          sourceTag: 'federated',
          available: true,
          items: [{ id: 1, source: 'instance', level: 'info', instanceId: 2, instanceUuid: 'stopped-2', nodeId: 1, message: 'deep-linked-log', time: '2026-07-18T12:00:00Z' }],
          total: 1,
          page: 1,
          pageSize: 100,
          quality: { duplicate_quality: 'exact', stats_quality: 'exact' },
          coverage: { complete: true, partial_reasons: [], targets: [], enumeration_state: 'EXHAUSTED' },
          view: { view_id: 'deep-link-view' },
        })
      }),
      // 联邦导出是 POST（物化当前 Query View），参数同样走 query string。
      http.post(API('/logs/federation/export'), ({ request }) => {
        exportedUrl = new URL(request.url)
        return new HttpResponse('{"message":"deep-linked-log"}\n', { headers: { 'Content-Type': 'application/x-ndjson' } })
      }),
    )

    renderWithProviders(<LogsPage />, { route: '/logs?instanceId=2' })
    expect(await screen.findByText('deep-linked-log')).toBeInTheDocument()
    expect(queryRequests.at(-1)?.searchParams.get('instanceId')).toBe('2')

    await user.type(screen.getByPlaceholderText('搜索日志内容…'), 'shutdown')
    await waitFor(() => {
      const latest = queryRequests.at(-1)
      expect(latest?.searchParams.get('keyword')).toBe('shutdown')
      expect(latest?.searchParams.get('instanceId')).toBe('2')
    })

    HTMLElement.prototype.hasPointerCapture ??= () => false
    HTMLElement.prototype.setPointerCapture ??= () => {}
    HTMLElement.prototype.releasePointerCapture ??= () => {}
    HTMLElement.prototype.scrollIntoView ??= () => {}
    await user.click(screen.getAllByRole('combobox')[0])
    await user.click(await screen.findByRole('option', { name: '最近 1 小时' }))
    await waitFor(() => {
      const latest = queryRequests.at(-1)
      expect(latest?.searchParams.get('instanceId')).toBe('2')
      expect(latest?.searchParams.get('from')).toBeTruthy()
      expect(latest?.searchParams.get('to')).toBeTruthy()
    })

    await user.click(screen.getByRole('button', { name: '导出' }))
    await user.click(await screen.findByRole('menuitem', { name: '导出全部匹配' }))
    await waitFor(() => expect(exportedUrl).not.toBeNull())
    expect(exportedUrl?.searchParams.get('instanceId')).toBe('2')
    expect(exportedUrl?.searchParams.get('keyword')).toBe('shutdown')
    expect(exportedUrl?.searchParams.get('from')).toBeTruthy()
    expect(exportedUrl?.searchParams.get('to')).toBeTruthy()

    createObjectURL.mockRestore()
    revokeObjectURL.mockRestore()
    click.mockRestore()
  })

  it('③ 注入 500 → 错误态可见且不得呈空成功', async () => {
    loginMockUser()
    // 本视图的唯一数据源是联邦接口：错误注入须打在它上面（经典 /logs 在此视图已不参与）。
    mockInject('get', '/logs/federation', { kind: 'status', status: 500 })
    renderWithProviders(<LogsPage />)
    // 错误态可见＝横幅明说「服务暂不可用、本视图无数据」，并给出 Legacy 只读的替代入口。
    const banner = await screen.findByTestId('logs-coverage-banner')
    expect(banner.dataset.degraded).toBe('true')
    expect(screen.getByTestId('logs-coverage-title')).toHaveTextContent(
      '实时日志查询服务暂不可用，本视图暂无数据',
    )
    // 查询失败不得显示成「暂无日志」空成功。
    expect(await screen.findByTestId('logs-empty-state')).toHaveAttribute(
      'data-empty-success',
      'false',
    )
    expect(screen.queryByText('暂无日志')).not.toBeInTheDocument()
  })
})
