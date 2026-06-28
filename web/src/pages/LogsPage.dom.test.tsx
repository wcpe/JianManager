import { describe, it, expect, beforeEach } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import { server } from '@/mocks/server'
import { API } from '@/mocks/api'
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

  it('③ 注入 500 → 显示加载日志失败错误态', async () => {
    loginMockUser()
    mockInject('get', '/logs', { kind: 'status', status: 500 })
    renderWithProviders(<LogsPage />)
    expect(await screen.findByText('加载日志失败')).toBeInTheDocument()
  })
})
