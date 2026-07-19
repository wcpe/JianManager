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

  it('1000+ 一年日志数据下只渲染可视窗口', async () => {
    loginMockUser()
    renderWithProviders(<LogsPage />)

    const surface = await screen.findByTestId('logs-virtual')
    expect(Number(surface.dataset.totalCount)).toBeGreaterThanOrEqual(1000)
    expect(await screen.findByText(/12004/)).toBeInTheDocument()
    expect(screen.queryAllByTestId('log-row').length).toBeLessThan(40)
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
    const logRequests: URL[] = []
    let exportedUrl: URL | null = null
    const createObjectURL = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:logs')
    const revokeObjectURL = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => undefined)
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined)
    server.use(
      http.get(API('/instances'), () => HttpResponse.json([
        { id: 2, uuid: 'stopped-2', nodeId: 1, name: '停机实例', type: 'minecraft_java', role: 'backend', processType: 'daemon', status: 'STOPPED', startCommand: 'java -jar server.jar', workDir: 'srv', serverPort: 25565, autoStart: false, autoRestart: true, tags: '', createdAt: '2026-07-18T00:00:00Z' },
      ])),
      http.get(API('/logs'), ({ request }) => {
        logRequests.push(new URL(request.url))
        return HttpResponse.json({
          items: [{ id: 1, source: 'instance', level: 'info', instanceId: 2, instanceUuid: 'stopped-2', nodeId: 1, message: 'deep-linked-log', time: '2026-07-18T12:00:00Z' }],
          total: 1,
          page: 1,
          pageSize: 100,
        })
      }),
      http.get(API('/logs/export'), ({ request }) => {
        exportedUrl = new URL(request.url)
        return new HttpResponse('{"message":"deep-linked-log"}\n', { headers: { 'Content-Type': 'application/x-ndjson' } })
      }),
    )

    renderWithProviders(<LogsPage />, { route: '/logs?instanceId=2' })
    expect(await screen.findByText('deep-linked-log')).toBeInTheDocument()
    expect(logRequests.at(-1)?.searchParams.get('instanceId')).toBe('2')

    await user.type(screen.getByPlaceholderText('搜索日志内容…'), 'shutdown')
    await waitFor(() => {
      const latest = logRequests.at(-1)
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
      const latest = logRequests.at(-1)
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

  it('③ 注入 500 → 显示加载日志失败错误态', async () => {
    loginMockUser()
    mockInject('get', '/logs', { kind: 'status', status: 500 })
    renderWithProviders(<LogsPage />)
    expect(await screen.findByText('加载日志失败')).toBeInTheDocument()
  })
})
