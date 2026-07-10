import { describe, it, expect, beforeEach, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import { server } from '@/mocks/server'
import { useAuthStore } from '@/stores/auth'
import WorkspaceCanvas from './WorkspaceCanvas'

// 卡片本体（终端 WS / 编辑器等）与本测试无关且在 jsdom 下重：置空，只保留画布 + 工具栏。
vi.mock('./WorkspaceCard', () => ({
  default: () => null,
}))

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

/**
 * 工作区工具栏「强制终止」危险操作确认（FR-059）。
 *
 * 实例 1（survival-1）种子状态 RUNNING → 工具栏展示 停止/重启/强制终止。
 * 强杀必须经 DangerConfirm 二次确认，不得点击即发 POST /instances/:id/kill。
 */
describe('WorkspaceCanvas 强杀确认（FR-059）', () => {
  beforeEach(() => {
    loginMockUser()
    // DangerConfirm scope=group 要求 role>=1（loginMockUser 的 token 解不出 role）。
    useAuthStore.setState({ role: 1, isAuthenticated: true })
  })

  it('点强制终止先弹确认框，确认后才发 kill 请求', async () => {
    const user = userEvent.setup()
    const spy = collectKillRequests()
    try {
      renderWithProviders(<WorkspaceCanvas instanceId={1} />)

      await user.click(await screen.findByRole('button', { name: '强制终止' }))
      const dialog = await screen.findByRole('dialog')
      expect(within(dialog).getByText('强制关停实例「survival-1」？')).toBeInTheDocument()
      expect(spy.paths).toHaveLength(0)

      await user.click(within(dialog).getByRole('button', { name: '强制终止' }))
      await waitFor(() => expect(spy.paths).toContain('/api/v1/instances/1/kill'))
    } finally {
      spy.stop()
    }
  })

  it('取消确认框不发 kill 请求', async () => {
    const user = userEvent.setup()
    const spy = collectKillRequests()
    try {
      renderWithProviders(<WorkspaceCanvas instanceId={1} />)

      await user.click(await screen.findByRole('button', { name: '强制终止' }))
      const dialog = await screen.findByRole('dialog')
      await user.click(within(dialog).getByRole('button', { name: '取消' }))

      await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
      expect(spy.paths).toHaveLength(0)
    } finally {
      spy.stop()
    }
  })
})
