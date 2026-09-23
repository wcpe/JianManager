import { describe, it, expect, beforeEach } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import { server } from '@jianmanager/devmock/server'
import { useAuthStore } from '@/stores/auth'
import InstanceConsolePage from './InstanceConsolePage'

/** 收集发往 /instances/:id/adopt-runtime 的请求（断言接管是否被确认框拦住）。 */
function collectAdoptRequests() {
  const paths: string[] = []
  const listener = ({ request }: { request: Request }) => {
    const url = new URL(request.url)
    if (url.pathname.endsWith('/adopt-runtime')) paths.push(url.pathname)
  }
  server.events.on('request:start', listener)
  return { paths, stop: () => server.events.removeListener('request:start', listener) }
}

/**
 * FR-471：运行态漂移告警与「接管」入口（服务器控制台）。
 *
 * 种子 creative-plot（id=21）为 STOPPED + runtimeDriftPid>0，代表「面板显示已停止、
 * 磁盘上有 tmux 手工启的进程」这一现实场景。接管是会给该服造成一次真实重启的写操作，
 * 必须经 DangerConfirm 二次确认后才下发。
 */
describe('InstanceConsolePage 运行态漂移（FR-471）', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('漂移实例顶部显示告警横幅，含未纳管 PID 与命令行', async () => {
    renderWithProviders(<InstanceConsolePage instanceId={21} />, { route: '/instances/21' })

    const banner = await screen.findByTestId('runtime-drift-banner')
    expect(banner).toHaveTextContent('运行态漂移')
    expect(banner).toHaveTextContent('41237')
    // 命令行摘要：判断「这是不是我们那台服」的关键证据。
    expect(banner).toHaveTextContent('java -Xmx2G -jar paper.jar nogui')
  })

  it('无漂移实例不显示告警横幅', async () => {
    renderWithProviders(<InstanceConsolePage instanceId={1} />, { route: '/instances/1' })

    // 等实例标题渲染完成，再断言横幅缺席（避免「还在加载」造成的假绿）。
    expect(await screen.findByRole('heading', { name: 'survival-1' })).toBeInTheDocument()
    expect(screen.queryByTestId('runtime-drift-banner')).not.toBeInTheDocument()
  })

  it('点「接管」先弹二次确认，确认后才发 adopt-runtime 请求，且成功后横幅消失', async () => {
    // DangerConfirm scope=group 要求 role>=1（loginMockUser 的 token 解不出 role）。
    useAuthStore.setState({ role: 1, isAuthenticated: true })
    const user = userEvent.setup()
    const spy = collectAdoptRequests()
    try {
      renderWithProviders(<InstanceConsolePage instanceId={21} />, { route: '/instances/21' })

      await user.click(await screen.findByTestId('runtime-drift-adopt'))

      const dialog = await screen.findByRole('dialog')
      expect(within(dialog).getByText('接管实例「creative-plot」的运行态？')).toBeInTheDocument()
      // 确认前不得发写请求。
      expect(spy.paths).toHaveLength(0)

      await user.click(within(dialog).getByRole('button', { name: '接管' }))

      await waitFor(() => expect(spy.paths).toContain('/api/v1/instances/21/adopt-runtime'))
      // 假后端接管后清空漂移字段 → 列表/详情失效重拉 → 横幅随数据消失（无本地残留态）。
      await waitFor(() => expect(screen.queryByTestId('runtime-drift-banner')).not.toBeInTheDocument())
    } finally {
      spy.stop()
    }
  })

  it('取消二次确认不发 adopt-runtime 请求', async () => {
    useAuthStore.setState({ role: 1, isAuthenticated: true })
    const user = userEvent.setup()
    const spy = collectAdoptRequests()
    try {
      renderWithProviders(<InstanceConsolePage instanceId={21} />, { route: '/instances/21' })

      await user.click(await screen.findByTestId('runtime-drift-adopt'))
      const dialog = await screen.findByRole('dialog')
      await user.click(within(dialog).getByRole('button', { name: '取消' }))

      await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
      expect(spy.paths).toHaveLength(0)
    } finally {
      spy.stop()
    }
  })
})
