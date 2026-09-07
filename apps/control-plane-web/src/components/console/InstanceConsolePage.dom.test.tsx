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

    // 瘦身顶栏（FR-412）：标题只留实例名，「服务器控制台 /」前缀与副标题已删；
    // 「打开终端」按钮亦删（控制台就是一个页签，无需重复入口）。
    expect(await screen.findByRole('heading', { name: 'survival-1' })).toBeInTheDocument()
    expect(screen.getByText('运行')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /打开终端/ })).not.toBeInTheDocument()

    for (const tab of ['概览', '控制台', '文件配置', '监控', '玩家', '插件', '备份定时', '业务', 'Bot']) {
      // WAI-ARIA tabs：页签是 tab 角色（不再是裸 button），激活态是 aria-selected。
      expect(screen.getByRole('tab', { name: tab })).toBeInTheDocument()
    }

    // CPU/内存 现在顶栏指标条与概览 KPI 卡各出现一次。
    expect(screen.getAllByText('CPU')).toHaveLength(2)
    expect(screen.getAllByText('内存')).toHaveLength(2)
    expect(screen.getAllByText('TPS').length).toBeGreaterThan(0)
    // 三卡合流（FR-423）：「最近事件 / 关注事项 / 崩溃诊断」并为一条「动态与告警」流。
    expect(screen.getByText('动态与告警')).toBeInTheDocument()
    expect(screen.queryByText('最近事件')).toBeNull()
    expect(screen.queryByText('关注事项')).toBeNull()
    expect(screen.getByText('运行日志预览')).toBeInTheDocument()
  })

  it('Tab 重组（FR-413）：环境变量并入文件配置分段，?tab=env 旧深链仍落到该分段', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstanceConsolePage instanceId={1} />, { route: '/instances/1?tab=env' })

    // 「环境变量」不再是顶级页签（Tab 栏内查不到），文件配置页签被旧深链激活。
    const tabBar = await screen.findByRole('tablist')
    expect(within(tabBar).getByRole('tab', { name: '文件配置' })).toHaveAttribute('aria-selected', 'true')
    expect(within(tabBar).queryByRole('tab', { name: '环境变量' })).toBeNull()

    // 旧深链落到环境变量分段：分段按钮按下态。
    const envSegment = screen.getByRole('button', { name: '环境变量' })
    expect(envSegment).toHaveAttribute('aria-pressed', 'true')

    // 切到文件分段 → URL 去掉 seg；再切回环境变量 → seg=env。
    await user.click(screen.getByRole('button', { name: '文件' }))
    await waitFor(() => expect(window.location.search).toBe('?tab=resource'))
    await user.click(envSegment)
    await waitFor(() => expect(window.location.search).toBe('?tab=resource&seg=env'))
  })

  it('从 URL 恢复激活 Tab，切换后同步回 searchParams', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstanceConsolePage instanceId={1} />, { route: '/instances/1?tab=players' })

    expect(await screen.findByRole('tab', { name: '玩家' })).toHaveAttribute('aria-selected', 'true')

    await user.click(screen.getByRole('tab', { name: '概览' }))

    expect(new URLSearchParams(window.location.search).get('tab')).toBeNull()
    expect(screen.getByRole('tab', { name: '概览' })).toHaveAttribute('aria-selected', 'true')
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
