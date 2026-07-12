import { beforeAll, describe, it, expect, vi } from 'vitest'
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
  beforeAll(() => {
    Object.defineProperty(HTMLElement.prototype, 'hasPointerCapture', { configurable: true, value: () => false })
    Object.defineProperty(HTMLElement.prototype, 'setPointerCapture', { configurable: true, value: () => undefined })
    Object.defineProperty(HTMLElement.prototype, 'releasePointerCapture', { configurable: true, value: () => undefined })
    Object.defineProperty(HTMLElement.prototype, 'scrollIntoView', { configurable: true, value: () => undefined })
  })

  it('渲染 seed 在线玩家', async () => {
    loginMockUser()
    renderWithProviders(<PlayersPage />, { route: '/players' })
    expect(await screen.findByText('Alice')).toBeInTheDocument()
    expect(screen.getByText('Bob')).toBeInTheDocument()
  })

  it('踢出确认弹窗使用共享 Dialog，取消后关闭并重置原因', async () => {
    const user = userEvent.setup()
    loginMockAdmin()
    renderWithProviders(<PlayersPage />, { route: '/players' })

    const row = (await screen.findByText('Alice')).closest('tr') as HTMLElement
    await user.click(within(row).getByRole('button', { name: '踢出' }))

    const dialog = await screen.findByRole('dialog', { name: '踢出玩家' })
    expect(within(dialog).getByText('确认对玩家 Alice（子服 lobby）执行此操作？')).toBeInTheDocument()
    await user.type(within(dialog).getByPlaceholderText('可选，写明封禁/踢出原因'), '临时测试')
    await user.click(within(dialog).getByRole('button', { name: '取消' }))

    await waitFor(() => expect(screen.queryByRole('dialog', { name: '踢出玩家' })).not.toBeInTheDocument())
    await user.click(within(row).getByRole('button', { name: '踢出' }))
    expect(await screen.findByPlaceholderText('可选，写明封禁/踢出原因')).toHaveValue('')
  })

  it('封禁确认弹窗确认后写入封禁记录', async () => {
    const user = userEvent.setup()
    loginMockAdmin()
    renderWithProviders(<PlayersPage />, { route: '/players' })

    const row = (await screen.findByText('Bob')).closest('tr') as HTMLElement
    await user.click(within(row).getByRole('button', { name: '封禁' }))

    const dialog = await screen.findByRole('dialog', { name: '封禁玩家' })
    await user.type(within(dialog).getByPlaceholderText('可选，写明封禁/踢出原因'), '违规行为')
    await user.click(within(dialog).getByRole('button', { name: '封禁' }))
    await waitFor(() => expect(screen.queryByRole('dialog', { name: '封禁玩家' })).not.toBeInTheDocument())

    await user.click(screen.getByRole('button', { name: '封禁记录' }))
    const banRow = (await screen.findByText('Bob')).closest('tr') as HTMLElement
    expect(within(banRow).getByText('违规行为')).toBeInTheDocument()
    expect(within(banRow).getByText('生效中')).toBeInTheDocument()
  })

  it('勾选两个在线玩家后可批量踢出确认并批量封禁', async () => {
    const user = userEvent.setup()
    loginMockAdmin()
    renderWithProviders(<PlayersPage />, { route: '/players' })

    await user.click(await screen.findByRole('checkbox', { name: '选择玩家 Alice（lobby）' }))
    await user.click(screen.getByRole('checkbox', { name: '选择玩家 Bob（survival）' }))

    await user.click(screen.getByRole('button', { name: '批量踢出' }))
    const kickDialog = await screen.findByRole('dialog', { name: '批量踢出玩家' })
    expect(within(kickDialog).getByText('已选择 2 名玩家，涉及 2 个子服。')).toBeInTheDocument()
    await user.click(within(kickDialog).getByRole('button', { name: '取消' }))
    await waitFor(() => expect(screen.queryByRole('dialog', { name: '批量踢出玩家' })).not.toBeInTheDocument())

    await user.click(screen.getByRole('button', { name: '批量封禁' }))
    const banDialog = await screen.findByRole('dialog', { name: '批量封禁玩家' })
    expect(within(banDialog).getByText('已选择 2 名玩家，涉及 2 个子服；封禁按玩家名全局执行，同名多服只提交一次。')).toBeInTheDocument()
    await user.type(within(banDialog).getByPlaceholderText('可选，写明封禁/踢出原因'), '批量违规')
    await user.click(within(banDialog).getByRole('button', { name: '批量封禁' }))
    await waitFor(() => expect(screen.queryByRole('dialog', { name: '批量封禁玩家' })).not.toBeInTheDocument())

    await user.click(screen.getByRole('button', { name: '封禁记录' }))
    for (const player of ['Alice', 'Bob']) {
      const row = (await screen.findByText(player)).closest('tr') as HTMLElement
      expect(within(row).getByText('批量违规')).toBeInTheDocument()
      expect(within(row).getByText('生效中')).toBeInTheDocument()
    }
  })

  it('按子服筛选会收敛在线玩家与分组计数', async () => {
    const user = userEvent.setup()
    loginMockAdmin()
    renderWithProviders(<PlayersPage />, { route: '/players' })

    expect(await screen.findByText('Alice')).toBeInTheDocument()
    expect(screen.getByText('Bob')).toBeInTheDocument()

    await user.click(screen.getAllByRole('combobox')[0]!)
    await user.click(await screen.findByRole('option', { name: 'survival' }))

    expect(screen.queryByText('Alice')).not.toBeInTheDocument()
    expect(screen.getByText('Bob')).toBeInTheDocument()
    expect(screen.getByText('survival · 1 人')).toBeInTheDocument()
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
    loginMockAdmin()
    renderWithProviders(<PlayersPage />, { route: '/players' })

    // 标题始终渲染（未整页崩溃/刷新）。
    expect(screen.getByRole('heading', { name: '玩家管理' })).toBeInTheDocument()
    // 加载失败后优雅降级为空态文案。
    expect(await screen.findByText('暂无在线玩家')).toBeInTheDocument()
  })

  it('实时事件支持暂停、过滤与清空控件', async () => {
    const user = userEvent.setup()
    const originalFetch = globalThis.fetch
    globalThis.fetch = vi.fn(async () =>
      new Response(
        [
          'event: init',
          'data: {"connected":true,"players":[{"name":"Alice","server":"lobby"}]}',
          '',
          'event: player',
          'data: {"instanceUuid":"inst-1","instanceId":1,"instanceName":"lobby","type":"player_join","timestamp":1816999999,"playerName":"Carl","server":"lobby"}',
          '',
          'event: player',
          'data: {"instanceUuid":"inst-1","instanceId":1,"instanceName":"lobby","type":"chat","timestamp":1817000000,"playerName":"Alice","message":"hi"}',
          '',
        ].join('\n'),
      ),
    ) as unknown as typeof fetch
    loginMockAdmin()
    renderWithProviders(<PlayersPage />, { route: '/players' })

    try {
      await user.click(screen.getByRole('button', { name: '实时事件' }))
      expect(await screen.findByText('事件流')).toBeInTheDocument()
      const eventPanel = screen.getByText('事件流').closest('.border') as HTMLElement
      expect(await within(eventPanel).findByText('Carl')).toBeInTheDocument()
      expect(await within(eventPanel).findByText(/hi/)).toBeInTheDocument()

      await user.click(screen.getAllByRole('combobox')[1]!)
      await user.click(await screen.findByRole('option', { name: '发言' }))
      expect(within(eventPanel).queryByText('Carl')).not.toBeInTheDocument()
      expect(within(eventPanel).getByText(/hi/)).toBeInTheDocument()

      await user.click(screen.getByRole('button', { name: '暂停' }))
      expect(screen.getByRole('button', { name: '继续' })).toBeInTheDocument()

      await user.click(screen.getByRole('button', { name: '清空' }))
      expect(await screen.findByText('暂无事件')).toBeInTheDocument()
    } finally {
      globalThis.fetch = originalFetch
    }
  })
})
