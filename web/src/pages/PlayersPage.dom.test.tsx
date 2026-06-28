import { describe, it, expect } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import { db } from '@/mocks/db'
import { useAuthStore } from '@/stores/auth'
import type { Session } from '@/mocks/handlers/domains/auth'
import PlayersPage from './PlayersPage'

/**
 * PlayersPage 强断言（FR-206 玩家域）：渲染 seed 在线玩家 / 封禁记录联动 / 注入 500 降级不崩。
 * 默认「在线玩家」tab 仅打 GET /players；封禁 tab 打 GET /bans（均属本域，不触发跨域 /instances）。
 */

/**
 * 登录为平台管理员（role=10）：解封走 DangerConfirm scope=group 的前端角色门禁，
 * 需 store.role≥1 才放行确认按钮，故构造带 role 的 fakeJWT 并灌入 auth store + sessions。
 */
function loginMockAdmin(): void {
  const payload = btoa(JSON.stringify({ userId: 1, username: 'admin', role: 10, exp: Math.floor(Date.now() / 1000) + 900 }))
  const token = `mock.${payload}.sig`
  db<Session>('sessions').insert({ accessToken: token, refreshToken: 'r-admin', userId: 1 })
  useAuthStore.getState().login(token, 'r-admin')
}

describe('PlayersPage（mock 假后端）', () => {
  it('渲染 seed 在线玩家', async () => {
    loginMockUser()
    renderWithProviders(<PlayersPage />, { route: '/players' })
    expect(await screen.findByText('Alice')).toBeInTheDocument()
    expect(screen.getByText('Bob')).toBeInTheDocument()
  })

  it('解封写操作 → 封禁记录状态联动（生效中 → 已解除）', async () => {
    const user = userEvent.setup()
    loginMockAdmin()
    renderWithProviders(<PlayersPage />, { route: '/players' })

    // 切到封禁记录 tab，确认 seed 封禁行（Griefer 生效中）。
    await user.click(screen.getByRole('button', { name: '封禁记录' }))
    const row = (await screen.findByText('Griefer')).closest('tr') as HTMLElement
    expect(within(row).getByText('生效中')).toBeInTheDocument()

    // 解封 Griefer：点行内「解封」→ 弹出 DangerConfirm，在弹窗内确认。
    await user.click(within(row).getByRole('button', { name: '解封' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: '解封' }))

    // 联动：该玩家封禁记录置为「已解除」。
    await waitFor(() => {
      const after = screen.getByText('Griefer').closest('tr') as HTMLElement
      expect(within(after).getByText('已解除')).toBeInTheDocument()
    })
  })

  it('注入 500 → 在线列表降级为空态，不崩溃（页面标题仍在）', async () => {
    mockInject('get', '/players', { kind: 'status', status: 500 })
    loginMockUser()
    renderWithProviders(<PlayersPage />, { route: '/players' })

    // 标题始终渲染（未整页崩溃/刷新）。
    expect(screen.getByRole('heading', { name: '玩家管理' })).toBeInTheDocument()
    // 加载失败后优雅降级为空态文案。
    expect(await screen.findByText('暂无在线玩家')).toBeInTheDocument()
  })
})
