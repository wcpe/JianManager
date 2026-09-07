import { beforeAll, describe, it, expect } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import { db } from '@jianmanager/devmock/db'
import { useAuthStore } from '@/stores/auth'
import type { Session } from '@jianmanager/devmock/handlers/domains/auth'
import InstancePlayersSegment from './InstancePlayersSegment'

/**
 * InstancePlayersSegment 强断言（FR-339 玩家分区接真）。
 * devmock 种子（domains/plugin.ts）：在线 Alice@实例1 / Bob@实例2；封禁 Griefer(生效·全局) / Spammer(已解除·单服)；
 * 实例1 白名单 [Alice, Bob]。本分区按 instanceId=1 过滤在线列表（只剩 Alice），封禁列表全量展示（不按实例过滤）。
 */

/**
 * 登录为平台管理员（role=10）：踢/封确认为普通 Dialog（无角色门禁），但解封走 DangerConfirm scope=group，
 * 需 store.role≥1 才放行确认按钮。构造带 role 的 fakeJWT 灌入 auth store + sessions（复用 PlayersPage 范式）。
 */
function loginMockAdmin(): void {
  const payload = btoa(JSON.stringify({ userId: 1, username: 'admin', role: 10, exp: Math.floor(Date.now() / 1000) + 900 }))
  const token = `mock.${payload}.sig`
  db<Session>('sessions').insert({ accessToken: token, refreshToken: 'r-admin', userId: 1 })
  useAuthStore.getState().login(token, 'r-admin')
}

/** 按 Panel 标题定位其面板容器（在线/白名单都含 Alice，需分区隔离断言）。 */
function panelByTitle(title: string): HTMLElement {
  return screen.getByText(title).closest('[data-slot="panel"]') as HTMLElement
}

/**
 * 按玩家名定位其所在行卡片（FR-423：在线玩家与白名单由 2 列表格改为 ul/li 行卡片，
 * 故行容器是 li 而非 tr；封禁记录仍是宽表，继续用 tr）。
 */
function playerRow(scope: HTMLElement, name: string): HTMLElement {
  return within(scope).getByText(name).closest('li') as HTMLElement
}

describe('InstancePlayersSegment（mock 假后端）', () => {
  beforeAll(() => {
    // Radix Dialog 在 jsdom 下依赖的指针/滚动 API 缺失补桩（复用 PlayersPage 范式）。
    Object.defineProperty(HTMLElement.prototype, 'hasPointerCapture', { configurable: true, value: () => false })
    Object.defineProperty(HTMLElement.prototype, 'setPointerCapture', { configurable: true, value: () => undefined })
    Object.defineProperty(HTMLElement.prototype, 'releasePointerCapture', { configurable: true, value: () => undefined })
    Object.defineProperty(HTMLElement.prototype, 'scrollIntoView', { configurable: true, value: () => undefined })
  })

  it('在线列表按本实例过滤：仅渲染实例 1 的玩家（Alice），实例 2 的 Bob 不入列', async () => {
    loginMockAdmin()
    renderWithProviders(<InstancePlayersSegment instanceId={1} />)

    const online = panelByTitle('在线玩家')
    expect(await within(online).findByText('Alice')).toBeInTheDocument()
    // Bob 属实例 2，被本实例过滤排除（不在在线面板内）。
    expect(within(online).queryByText('Bob')).not.toBeInTheDocument()
  })

  it('点「踢出」弹出确认弹窗（含原因输入），取消后关闭', async () => {
    const user = userEvent.setup()
    loginMockAdmin()
    renderWithProviders(<InstancePlayersSegment instanceId={1} />)

    const online = panelByTitle('在线玩家')
    await within(online).findByText('Alice')
    const row = playerRow(online, 'Alice')
    await user.click(within(row).getByRole('button', { name: '踢出' }))

    const dialog = await screen.findByRole('dialog', { name: '踢出玩家' })
    // 确认弹窗含原因输入（危险确认例外，非内联展开表单）。
    expect(within(dialog).getByPlaceholderText('可选，写明封禁/踢出原因')).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: '取消' }))

    await waitFor(() => expect(screen.queryByRole('dialog', { name: '踢出玩家' })).not.toBeInTheDocument())
  })

  it('封禁确认后请求携带 scope.instanceId=1（本实例作用域）', async () => {
    const user = userEvent.setup()
    loginMockAdmin()

    // 用 server.use 临时桩捕获封禁请求体，断言携带本实例作用域。
    // 注：useBanPlayer 直接以 scope 对象为 POST body（api.post(url, scope)），故 body 即 { instanceId, reason }。
    let banBody: Record<string, unknown> | null = null
    server.use(
      http.post(API('/players/:name/ban'), async ({ request }) => {
        banBody = (await request.json().catch(() => ({}))) as Record<string, unknown>
        return HttpResponse.json({ player: 'Alice', action: 'ban', total: 1, succeeded: 1, failed: 0, results: [] })
      }),
    )

    renderWithProviders(<InstancePlayersSegment instanceId={1} />)

    const online = panelByTitle('在线玩家')
    await within(online).findByText('Alice')
    const row = playerRow(online, 'Alice')
    await user.click(within(row).getByRole('button', { name: '封禁' }))

    const dialog = await screen.findByRole('dialog', { name: '封禁玩家' })
    await user.type(within(dialog).getByPlaceholderText('可选，写明封禁/踢出原因'), '违规')
    await user.click(within(dialog).getByRole('button', { name: '封禁' }))

    await waitFor(() => expect(banBody).not.toBeNull())
    expect((banBody as unknown as { instanceId?: number })?.instanceId).toBe(1)
    expect((banBody as unknown as { reason?: string })?.reason).toBe('违规')
  })

  it('封禁列表全量渲染（不按实例过滤），生效封禁带 scope 徽章且可解封', async () => {
    const user = userEvent.setup()
    loginMockAdmin()
    renderWithProviders(<InstancePlayersSegment instanceId={1} />)

    const bansPanel = panelByTitle('封禁记录')
    // 全量展示：生效中的 Griefer 与已解除的 Spammer 均在列（network/global 封禁同样影响本实例）。
    const grieferRow = (await within(bansPanel).findByText('Griefer')).closest('tr') as HTMLElement
    expect(within(grieferRow).getByText('生效中')).toBeInTheDocument()
    // 生效封禁带 scope 徽章（global → 全局）。
    expect(within(grieferRow).getByText('全局')).toBeInTheDocument()
    expect(within(bansPanel).getByText('Spammer')).toBeInTheDocument()

    // 解封 Griefer：行内「解封」→ DangerConfirm，确认后状态联动为「已解除」。
    await user.click(within(grieferRow).getByRole('button', { name: '解封' }))
    const dialog = await screen.findByRole('dialog', { name: '解封玩家' })
    await user.click(within(dialog).getByRole('button', { name: '解封' }))

    await waitFor(() => {
      const after = within(panelByTitle('封禁记录')).getByText('Griefer').closest('tr') as HTMLElement
      expect(within(after).getByText('已解除')).toBeInTheDocument()
    })
  })

  it('白名单渲染实例 1 种子成员并可添加（写联动）', async () => {
    const user = userEvent.setup()
    loginMockAdmin()
    renderWithProviders(<InstancePlayersSegment instanceId={1} />)

    const wlPanel = panelByTitle('白名单')
    // 实例 1 种子白名单 [Alice, Bob] 渲染在白名单面板内。
    expect(await within(wlPanel).findByText('Alice')).toBeInTheDocument()
    expect(within(wlPanel).getByText('Bob')).toBeInTheDocument()

    // 单行输入添加 Carl（行内微交互）→ POST 写 → 列表联动新增。
    await user.type(within(wlPanel).getByPlaceholderText('输入玩家名加入白名单'), 'Carl')
    await user.click(within(wlPanel).getByRole('button', { name: '添加' }))

    await waitFor(() => expect(within(panelByTitle('白名单')).getByText('Carl')).toBeInTheDocument())
  })

  it('分栏后三块内容同屏可达且各自可操作（FR-423）', async () => {
    const user = userEvent.setup()
    loginMockAdmin()
    renderWithProviders(<InstancePlayersSegment instanceId={1} />)

    const online = panelByTitle('在线玩家')
    const whitelist = panelByTitle('白名单')
    const bans = panelByTitle('封禁记录')

    // 分栏不得把任何一块挪出 DOM（xl 以下只是降级为纵向堆叠，仍全部挂载）。
    await within(online).findByText('Alice')
    await within(bans).findByText('Griefer')
    expect(within(whitelist).getByText('Bob')).toBeInTheDocument()

    // 左栏（在线 + 白名单）与右栏（封禁）是同一分栏根下的两个兄弟栏位。
    const leftColumn = online.parentElement as HTMLElement
    const rightColumn = bans.parentElement as HTMLElement
    expect(whitelist.parentElement).toBe(leftColumn)
    expect(rightColumn).not.toBe(leftColumn)
    const splitRoot = leftColumn.parentElement as HTMLElement
    expect(rightColumn.parentElement).toBe(splitRoot)

    // 分栏根遵循 FR-422 骨架契约（吃满内容区），并在 xl 起分左右；左 38% / 右 62% 权重。
    expect(splitRoot).toHaveClass('flex', 'min-h-0', 'flex-1', 'flex-col', 'xl:flex-row')
    expect(leftColumn).toHaveClass('xl:flex-1')
    expect(rightColumn).toHaveClass('xl:flex-[1.6]')

    // 三块卡一律「内容驱动 + 栏高封顶」并卡内滚动：记录少时不被拉平成死区，
    // 多时撑到上限后滚动。写死 flex-1 会让 2 条封禁也撑满 800+px。
    expect(bans).toHaveClass('max-h-full', 'min-h-0', 'flex-none')
    expect((within(bans).getByText('Griefer').closest('table') as HTMLElement).parentElement?.parentElement).toHaveClass('overflow-auto')

    // 三块各自的入口动作都还在且可点：白名单删除、封禁解封、在线踢出（点开确认弹窗为止）。
    expect(within(playerRow(whitelist, 'Bob')).getByRole('button', { name: '删除' })).toBeEnabled()
    const grieferRow = within(bans).getByText('Griefer').closest('tr') as HTMLElement
    expect(within(grieferRow).getByRole('button', { name: '解封' })).toBeEnabled()
    await user.click(within(playerRow(online, 'Alice')).getByRole('button', { name: '踢出' }))
    expect(await screen.findByRole('dialog', { name: '踢出玩家' })).toBeInTheDocument()
  })

  it('本实例探针不可达 → 在线面板显降级横幅并落空态', async () => {
    loginMockAdmin()
    // 临时桩令本实例后端 available=false，触发 players.degraded 降级横幅。
    server.use(
      http.get(API('/players'), () =>
        HttpResponse.json({
          players: [],
          backends: [{ instanceId: 1, instanceName: 'survival-1', available: false }],
        }),
      ),
    )
    renderWithProviders(<InstancePlayersSegment instanceId={1} />)

    // 降级横幅文案带子服名（players.degraded 插值）。
    expect(await screen.findByText(/survival-1/)).toBeInTheDocument()
    // 无在线玩家 → 空态。
    expect(await screen.findByText('暂无在线玩家')).toBeInTheDocument()
  })
})
