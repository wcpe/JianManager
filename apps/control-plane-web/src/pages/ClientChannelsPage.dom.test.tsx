import { beforeAll, describe, it, expect } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { domainRoute, mockInject } from '@jianmanager/devmock/inject'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import ClientChannelsPage from './ClientChannelsPage'

beforeAll(() => {
  if (!('ResizeObserver' in globalThis)) {
    class RO {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
    ;(globalThis as { ResizeObserver?: unknown }).ResizeObserver = RO
  }
})

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

  it('创建拉取密钥后说明可随时再次查看明文', async () => {
    loginMockUser()
    const user = userEvent.setup()
    renderWithProviders(<ClientChannelsPage />, { route: '/client-channels?channel=skyblock-s1&tab=keys' })

    await screen.findByText('空岛一区')
    await user.click(screen.getByRole('button', { name: '创建密钥' }))
    const createDialog = await screen.findByRole('dialog')
    await user.type(within(createDialog).getByPlaceholderText('如：正式包 / 灰度'), '回归密钥')
    await user.click(within(createDialog).getByRole('button', { name: '创建密钥' }))

    const secretDialog = await screen.findByRole('dialog')
    expect(within(secretDialog).getByText('拉取密钥')).toBeInTheDocument()
    expect(within(secretDialog).getByText('此密钥已加密保存，关闭后仍可在密钥列表中随时查看明文。')).toBeInTheDocument()
    expect(within(secretDialog).queryByText(/仅此一次|无法再次查看/)).not.toBeInTheDocument()
  })

  it('旧哈希密钥 KeyEnc 为空时禁用查看并提示明文不可找回', async () => {
    loginMockUser()
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
              id: 12,
              name: '旧版哈希密钥',
              keyPrefix: 'jmck_legacy',
              revoked: false,
              expiresAt: null,
              lastUsedAt: null,
              createdAt: '2025-12-01T09:00:00Z',
              revealable: false,
            },
          ],
        }),
      ),
    )

    renderWithProviders(<ClientChannelsPage />, { route: '/client-channels?channel=skyblock-s1&tab=keys' })

    const keyName = await screen.findByText('旧版哈希密钥')
    const row = keyName.closest('tr')
    expect(row).not.toBeNull()
    const revealButton = within(row!).getByRole('button', { name: '查看' })
    expect(revealButton).toBeDisabled()
    expect(revealButton.getAttribute('title')).toContain('明文不可找回')
    expect(revealButton.getAttribute('title')).toContain('编辑')
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

  it('频道工作台展示安全摘要并链到安全中心', async () => {
    loginMockUser()
    server.use(
      domainRoute('get', '/client-channels/:id/security-summary', () =>
        HttpResponse.json({
          channelId: 'skyblock-s1',
          riskLevel: 'warn',
          abnormalRequests: 3,
          blockedIpCount: 1,
          restrictedKeyCount: 2,
          protectionMode: 'throttle',
          windowMinutes: 60,
        }),
      ),
    )
    renderWithProviders(<ClientChannelsPage />, { route: '/client-channels?channelId=skyblock-s1&ip=192.0.2.9&machineId=m-1&errCode=RATE_LIMITED&version=2&tab=stats' })

    const bar = await screen.findByTestId('channel-security-summary')
    expect(within(bar).getByText('安全摘要')).toBeInTheDocument()
    expect(await within(bar).findByText('warn')).toBeInTheDocument()
    expect(within(bar).getByText(/近窗异常\s*3/)).toBeInTheDocument()
    expect(within(bar).getByText(/封禁 IP\s*1/)).toBeInTheDocument()
    expect(within(bar).getByText(/受限 Key\s*2/)).toBeInTheDocument()
    const link = within(bar).getByRole('link', { name: '打开安全中心' })
    expect(screen.getByRole('tab', { name: '统计' })).toHaveAttribute('data-state', 'active')
    expect(link).toHaveAttribute(
      'href',
      '/client-dist-security?channelId=skyblock-s1&ip=192.0.2.9&machineId=m-1&errCode=RATE_LIMITED&version=2&tab=logs',
    )
  })
})
