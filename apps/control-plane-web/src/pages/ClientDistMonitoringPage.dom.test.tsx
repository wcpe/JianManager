import { describe, it, expect, beforeAll } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { db } from '@jianmanager/devmock/db'
import type { Session } from '@jianmanager/devmock/handlers/domains/auth'
import { useAuthStore } from '@/stores/auth'
import { mockInject } from '@jianmanager/devmock/inject'
import { server } from '@jianmanager/devmock/server'
import ClientDistMonitoringPage from './ClientDistMonitoringPage'

const ADMIN_TOKEN = `mock.${btoa(
  JSON.stringify({ userId: 1, username: 'admin', role: 10, exp: Math.floor(Date.now() / 1000) + 900 }),
)}.sig`
const MEMBER_TOKEN = `mock.${btoa(
  JSON.stringify({ userId: 2, username: 'bob', role: 0, exp: Math.floor(Date.now() / 1000) + 900 }),
)}.sig`

function loginAs(token: string, userId: number) {
  db<Session>('sessions').insert({ accessToken: token, refreshToken: 'r', userId })
  useAuthStore.getState().login(token, 'r')
}

function countStatsRequests(): { get: () => number; stop: () => void } {
  let n = 0
  const listener = ({ request }: { request: Request }) => {
    if (new URL(request.url).pathname.endsWith('/api/v1/client-dist/stats')) n += 1
  }
  server.events.on('request:start', listener)
  return { get: () => n, stop: () => server.events.removeListener('request:start', listener) }
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

describe('ClientDistMonitoringPage（FR-265 四 Tab）', () => {
  it('① 平台管理员：四 Tab 同页面，默认统计 Tab 只渲染请求侧指标且无 CAS 文案', async () => {
    loginAs(ADMIN_TOKEN, 1)
    renderWithProviders(<ClientDistMonitoringPage />)

    expect(await screen.findByRole('heading', { name: '客户端分发观测' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: /统计/ })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: /监控/ })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: /日志/ })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: /客户端/ })).toBeInTheDocument()
    expect(await screen.findByText('请求成功率')).toBeInTheDocument()
    expect(screen.getByText('请求结果分布')).toBeInTheDocument()
    expect(screen.queryByText(/CAS/i)).not.toBeInTheDocument()
    expect(screen.queryByText('更新成功率')).not.toBeInTheDocument()
  })

  it('② 监控 Tab：只展示近实时分发请求聚合', async () => {
    loginAs(ADMIN_TOKEN, 1)
    const user = userEvent.setup()
    renderWithProviders(<ClientDistMonitoringPage />)
    await screen.findByText('请求成功率')

    await user.click(screen.getByRole('tab', { name: /监控/ }))

    expect(await screen.findByText('近 1h 错误')).toBeInTheDocument()
    expect(screen.getByText('近 24h 请求速率')).toBeInTheDocument()
    expect(screen.getByText('最近错误请求')).toBeInTheDocument()
    // 监控 Tab 同时展示最近错误与错误码 TopN，错误码可出现多次
    expect(screen.getAllByText('INVALID_CLIENT_KEY').length).toBeGreaterThanOrEqual(1)
  })

  it('③ 日志 Tab：分页事件表出数，并可打开脱敏 Header 详情', async () => {
    loginAs(ADMIN_TOKEN, 1)
    const user = userEvent.setup()
    renderWithProviders(<ClientDistMonitoringPage />)
    await screen.findByText('请求成功率')

    await user.click(screen.getByRole('tab', { name: /日志/ }))
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

  it('④ 客户端 Tab：运行态指标独立展示，并可联动到日志 Tab 按机器码过滤', async () => {
    loginAs(ADMIN_TOKEN, 1)
    const user = userEvent.setup()
    renderWithProviders(<ClientDistMonitoringPage />)
    await screen.findByText('请求成功率')

    await user.click(screen.getByRole('tab', { name: /客户端/ }))
    expect(await screen.findByText('近 5 分钟启动')).toBeInTheDocument()
    expect(screen.getByText('今日启动')).toBeInTheDocument()
    expect(screen.getByText('客户端运行态')).toBeInTheDocument()
    expect(screen.getByText('m-aaaa')).toBeInTheDocument()

    const row = screen.getByText('m-aaaa').closest('tr')
    expect(row).not.toBeNull()
    await user.click(within(row as HTMLElement).getByRole('button', { name: '看日志' }))

    expect(await screen.findByText('已联动过滤日志：')).toBeInTheDocument()
    expect(screen.getByText('machine=m-aaaa')).toBeInTheDocument()
    expect(screen.getByText('m-aaaa')).toBeInTheDocument()
    expect(screen.queryByText('m-bbbb')).not.toBeInTheDocument()
  })

  it('⑤ 错误码 TopN 可点击进入日志并按错误码过滤', async () => {
    loginAs(ADMIN_TOKEN, 1)
    const user = userEvent.setup()
    renderWithProviders(<ClientDistMonitoringPage />)
    await screen.findByText('请求成功率')

    await user.click(screen.getByRole('tab', { name: /监控/ }))
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

  it('⑥ 非平台管理员：整页降级为权限提示，且不发起平台级统计请求', async () => {
    loginAs(MEMBER_TOKEN, 2)
    const statsRequests = countStatsRequests()
    try {
      renderWithProviders(<ClientDistMonitoringPage />)

      expect(await screen.findByRole('heading', { name: '客户端分发观测' })).toBeInTheDocument()
      expect(await screen.findByText('客户端分发观测需平台管理员权限')).toBeInTheDocument()
      expect(screen.queryByText('请求成功率')).not.toBeInTheDocument()
      expect(statsRequests.get()).toBe(0)
    } finally {
      statsRequests.stop()
    }
  })

  it('⑦ 端点错误：当前 Tab 降级为错误态、不崩溃', async () => {
    loginAs(ADMIN_TOKEN, 1)
    mockInject('get', '/client-dist/stats', { kind: 'status', status: 500 })
    renderWithProviders(<ClientDistMonitoringPage />)

    expect(await screen.findByRole('heading', { name: '客户端分发观测' })).toBeInTheDocument()
    expect(await screen.findByText('加载分发请求观测失败')).toBeInTheDocument()
    expect(screen.queryByText('请求成功率')).not.toBeInTheDocument()
  })
})
