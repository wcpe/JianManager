import { describe, it, expect, beforeEach } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import { server } from '@jianmanager/devmock/server'
import { useAuthStore } from '@/stores/auth'
import InstanceConsolePage from './InstanceConsolePage'

/** 收集发往 /instances/:id/kill 的请求（断言强杀是否被确认框拦住）。 */
function collectKillRequests() {
  const paths: string[] = []
  const listener = ({ request }: { request: Request }) => {
    const url = new URL(request.url)
    if (url.pathname.endsWith('/kill')) paths.push(url.pathname)
  }
  server.events.on('request:start', listener)
  return { paths, stop: () => server.events.removeListener('request:start', listener) }
}

/** FR-269：服务器统一控制台 mock-api 原型。 */
describe('InstanceConsolePage', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('渲染服务器状态条、固定分区和概览 KPI', async () => {
    renderWithProviders(<InstanceConsolePage instanceId={1} />)

    expect(await screen.findByText(/服务器控制台 \/ survival-1/)).toBeInTheDocument()
    expect(screen.getByText('运行')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /打开终端/ })).toBeInTheDocument()

    for (const tab of ['概览', '控制台', '文件配置', '监控', '玩家', '插件', '备份定时', '业务', 'Bot']) {
      expect(screen.getByRole('button', { name: tab })).toBeInTheDocument()
    }

    expect(screen.getByText('CPU')).toBeInTheDocument()
    expect(screen.getByText('内存')).toBeInTheDocument()
    expect(screen.getAllByText('TPS').length).toBeGreaterThan(0)
    expect(screen.getByText('最近事件')).toBeInTheDocument()
    expect(screen.getByText('运行日志预览')).toBeInTheDocument()
  })

  it('从 URL 恢复激活 Tab，切换后同步回 searchParams', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstanceConsolePage instanceId={1} />, { route: '/instances/1?tab=players' })

    expect(await screen.findByRole('button', { name: '玩家' })).toHaveAttribute('aria-pressed', 'true')

    await user.click(screen.getByRole('button', { name: '概览' }))

    expect(new URLSearchParams(window.location.search).get('tab')).toBeNull()
    expect(screen.getByRole('button', { name: '概览' })).toHaveAttribute('aria-pressed', 'true')
  })

  it('强杀必须经危险操作确认框，确认后才发 kill 请求（FR-059）', async () => {
    // DangerConfirm scope=group 要求 role>=1（loginMockUser 的 token 解不出 role）。
    useAuthStore.setState({ role: 1, isAuthenticated: true })
    const user = userEvent.setup()
    const spy = collectKillRequests()
    try {
      renderWithProviders(<InstanceConsolePage instanceId={1} />)

      // 点「强杀」：不得直接发请求，必须先弹危险操作确认框。
      await user.click(await screen.findByRole('button', { name: /强杀/ }))
      const dialog = await screen.findByRole('dialog')
      expect(within(dialog).getByText('强制关停实例「survival-1」？')).toBeInTheDocument()
      expect(spy.paths).toHaveLength(0)

      // 确认后才真正调用 POST /instances/1/kill。
      await user.click(within(dialog).getByRole('button', { name: '强制终止' }))
      await waitFor(() => expect(spy.paths).toContain('/api/v1/instances/1/kill'))
    } finally {
      spy.stop()
    }
  })

  it('取消危险操作确认框不发 kill 请求（FR-059）', async () => {
    useAuthStore.setState({ role: 1, isAuthenticated: true })
    const user = userEvent.setup()
    const spy = collectKillRequests()
    try {
      renderWithProviders(<InstanceConsolePage instanceId={1} />)

      await user.click(await screen.findByRole('button', { name: /强杀/ }))
      const dialog = await screen.findByRole('dialog')
      await user.click(within(dialog).getByRole('button', { name: '取消' }))

      await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
      expect(spy.paths).toHaveLength(0)
    } finally {
      spy.stop()
    }
  })
})
