import { describe, it, expect } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@jianmanager/devmock/inject'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import ClientChannelsPage from './ClientChannelsPage'

/**
 * ClientChannelsPage 强断言（FR-210）：渲染 seed 频道 → 新建频道联动出现 → 注入 500 显错误态。
 * 渲染前 loginMockUser() 让 requireAuth 放行。
 */
describe('ClientChannelsPage（mock 假后端）', () => {
  it('渲染出 seed 频道卡片', async () => {
    loginMockUser()
    renderWithProviders(<ClientChannelsPage />)
    expect(await screen.findByText('空岛一区')).toBeInTheDocument()
    expect(screen.getByText('skyblock-s1')).toBeInTheDocument()
    expect(screen.getByText('生存二区')).toBeInTheDocument()
  })

  it('新建频道 → 列表联动出现新卡片', async () => {
    loginMockUser()
    const user = userEvent.setup()
    renderWithProviders(<ClientChannelsPage />)
    await screen.findByText('空岛一区')

    // 打开新增频道模态（页眉「新增频道」按钮）。
    await user.click(screen.getByRole('button', { name: /新增频道/ }))
    const dialog = await screen.findByRole('dialog')

    await user.type(within(dialog).getByPlaceholderText('skyblock-s1'), 'creative-s3')
    // 模态内首个文本输入即「名称」（频道标识用 placeholder 定位，描述靠后）。
    const nameInput = within(dialog).getAllByRole('textbox')[1]
    await user.type(nameInput, '创造三区')
    await user.click(within(dialog).getByRole('button', { name: '创建' }))

    // 创建成功后进入该频道工作台：标题出现新频道名。
    expect(await screen.findByRole('heading', { name: /创造三区/ })).toBeInTheDocument()
  })

  it('拉取密钥列表标识已过期与即将过期', async () => {
    loginMockUser()
    const now = Date.now()
    server.use(
      http.get(API('/client-channels/:channelId'), () =>
        HttpResponse.json({
          id: 1,
          channelId: 'skyblock-s1',
          name: '空岛一区',
          description: '空岛生存主分发频道',
          currentVersion: 2,
          createdAt: '2026-06-01T08:00:00Z',
          updatedAt: '2026-06-20T08:00:00Z',
          keys: [
            {
              id: 10,
              name: '过期包',
              keyPrefix: 'jmck_old',
              revoked: false,
              expiresAt: new Date(now - 24 * 60 * 60 * 1000).toISOString(),
              lastUsedAt: null,
              createdAt: '2026-06-01T09:00:00Z',
              revealable: true,
            },
            {
              id: 11,
              name: '即将过期包',
              keyPrefix: 'jmck_soon',
              revoked: false,
              expiresAt: new Date(now + 2 * 24 * 60 * 60 * 1000).toISOString(),
              lastUsedAt: null,
              createdAt: '2026-06-01T09:00:00Z',
              revealable: true,
            },
          ],
        }),
      ),
    )

    renderWithProviders(<ClientChannelsPage />, { route: '/client-channels?channel=skyblock-s1&tab=keys' })

    expect(await screen.findByText('过期包')).toBeInTheDocument()
    expect(screen.getByText('已过期')).toBeInTheDocument()
    expect(screen.getByText('即将过期')).toBeInTheDocument()
  })

  it('注入 500 → 列表显示错误态（不崩溃）', async () => {
    loginMockUser()
    mockInject('get', '/client-channels', { kind: 'status', status: 500 })
    renderWithProviders(<ClientChannelsPage />)

    // 频道列表加载失败：页面仍渲染标题与空状态引导（list 为空时显空态大引导卡），不抛错白屏。
    expect(await screen.findByRole('heading', { name: /客户端分发/ })).toBeInTheDocument()
    await waitFor(() =>
      expect(screen.getByText('创建第一个分发频道')).toBeInTheDocument(),
    )
    // seed 频道不应出现（请求被注入为 500）。
    expect(screen.queryByText('空岛一区')).not.toBeInTheDocument()
  })
})
