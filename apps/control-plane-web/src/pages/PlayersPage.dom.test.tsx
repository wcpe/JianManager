import { beforeAll, describe, it, expect, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@jianmanager/devmock/inject'
import { server } from '@jianmanager/devmock/server'
import { db } from '@jianmanager/devmock/db'
import { useAuthStore } from '@/stores/auth'
import type { Session } from '@jianmanager/devmock/handlers/domains/auth'
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

  /**
   * 千级实例的判据：实例数是千级（大档 1200），筛选器**不得**用全量列举——
   * 实时事件与白名单两处原先都是「容器拉全量 `/instances` → map 成 Radix SelectItem」，
   * 现改走服务端搜索：默认前 N 条 + 键入经 300ms 防抖下发 `q`，截断时提示继续输入。
   * 本用例盯住两件事：不下发裸 `/instances`，以及键入确实变成了带 `q` 的服务端搜索请求。
   */
  it('两个实例选择器改走服务端搜索：不下发全量 /instances，键入后带 q（白名单另按 role=backend 收窄）', async () => {
    const user = userEvent.setup()
    const originalFetch = globalThis.fetch
    // 实时事件的 SSE 走 fetch + ReadableStream（jsdom 下无真实端点），给一个空的 init 帧即可。
    globalThis.fetch = vi.fn(
      async () => new Response('event: init\ndata: {"connected":false,"players":[]}\n\n'),
    ) as unknown as typeof fetch

    // 记录实例域的每次请求：路径 + 查询串。
    const calls: { path: string; params: URLSearchParams }[] = []
    const listener = ({ request }: { request: Request }) => {
      const url = new URL(request.url)
      if (url.pathname.startsWith('/api/v1/instances')) calls.push({ path: url.pathname, params: url.searchParams })
    }
    server.events.on('request:start', listener)

    try {
      loginMockAdmin()
      renderWithProviders(<PlayersPage />, { route: '/players' })

      // ① 实时事件 Tab：候选默认窗口走服务端搜索，且整页不出现裸 /instances（全量列举）。
      await user.click(screen.getByRole('button', { name: '实时事件' }))
      await waitFor(() =>
        expect(
          calls.some((c) => c.path === '/api/v1/instances/search' && c.params.get('pageSize') === '50'),
        ).toBe(true),
      )
      expect(calls.filter((c) => c.path === '/api/v1/instances')).toEqual([])
      // 服务端截断 → 选择器提示继续输入缩小范围。
      expect(await screen.findByText(/已显示前 50 项，共 \d+ 项/)).toBeInTheDocument()

      // ② 键入经防抖下发 q（服务端搜索，而不是本地过滤全量列表）。
      await user.click(screen.getByLabelText('选择实例'))
      await user.type(await screen.findByPlaceholderText('搜索或输入…'), 'lobby')
      await waitFor(() => expect(calls.some((c) => c.params.get('q') === 'lobby')).toBe(true), { timeout: 2000 })

      // ③ 白名单 Tab：候选同样走服务端搜索，并按 role=backend 收窄（代理不支持白名单）。
      // 先收起候选下拉：Popover 是 modal，展开时页内其余内容对可访问性查询不可见。
      await user.keyboard('{Escape}')
      await waitFor(() => expect(screen.queryByPlaceholderText('搜索或输入…')).not.toBeInTheDocument())
      await user.click(screen.getByRole('button', { name: '白名单' }))
      await waitFor(() =>
        expect(
          calls.some(
            (c) =>
              c.path === '/api/v1/instances/search' &&
              c.params.get('role') === 'backend' &&
              c.params.get('pageSize') === '50',
          ),
        ).toBe(true),
      )
      expect(calls.filter((c) => c.path === '/api/v1/instances')).toEqual([])
    } finally {
      server.events.removeListener('request:start', listener)
      globalThis.fetch = originalFetch
    }
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

      // 事件类型筛选仍是 Radix Select；实例选择器已改为服务端搜索的 InstancePicker（Popover + 普通按钮选项），
      // 故本 Tab 里 role=combobox 只剩这一个。
      await user.click(screen.getByRole('combobox', { name: '事件类型' }))
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
