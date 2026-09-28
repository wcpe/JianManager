import { describe, it, expect, beforeEach, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { useAuthStore } from '@/stores/auth'
import api from '@/api/client'
import McpActivityPage from './McpActivityPage'

vi.mock('@/api/client', () => ({
  default: {
    get: vi.fn(),
  },
}))

const mockedApi = api as unknown as {
  get: ReturnType<typeof vi.fn>
}

const makeToken = (userId: number, username: string, role: number) =>
  `mock.${btoa(JSON.stringify({ userId, username, role, exp: Math.floor(Date.now() / 1000) + 900 }))}.sig`

function login(role: number) {
  const token = makeToken(1, role === 10 ? 'admin' : 'member', role)
  useAuthStore.getState().login(token, 'r-1')
}

const ACTIVITY_URL = '/agent/mcp/activity'

describe('McpActivityPage（DOM）', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
    useAuthStore.getState().logout()
  })

  it('非平台管理员显示无权限', () => {
    login(0)
    renderWithProviders(<McpActivityPage />)
    expect(screen.getByText(/仅平台管理员|Platform admin/i)).toBeInTheDocument()
    expect(mockedApi.get).not.toHaveBeenCalled()
  })

  it('管理员按 Token 聚合渲染活动行（默认 24h 窗口）', async () => {
    login(10)
    mockedApi.get.mockImplementation(async (url: string) => {
      if (url === ACTIVITY_URL) {
        return {
          data: {
            window: '24h',
            generatedAt: '2026-07-25T06:05:00Z',
            items: [
              {
                tokenId: 1,
                tokenName: 'ci-bot',
                tokenPrefix: 'jmat_ab12',
                lastActivityAt: '2026-07-25T06:05:00Z',
                lastAction: 'agent.whoami',
                callCount: 128,
                failureCount: 3,
                clientIPs: ['10.0.0.1'],
                clients: { 'claude-code': 120, curl: 8 },
              },
            ],
          },
        }
      }
      return { data: {} }
    })

    renderWithProviders(<McpActivityPage />)
    expect(await screen.findByText('ci-bot')).toBeInTheDocument()
    expect(screen.getByText(/jmat_ab12/)).toBeInTheDocument()
    expect(screen.getByText('agent.whoami')).toBeInTheDocument()
    expect(screen.getByText('128')).toBeInTheDocument()
    expect(screen.getByText('3')).toBeInTheDocument()
    expect(screen.getByText('10.0.0.1')).toBeInTheDocument()
    // 客户端列按调用次数降序聚合
    expect(screen.getByText(/claude-code ×120/)).toBeInTheDocument()
    // 默认窗口 24h 且请求带上 window 参数
    expect(mockedApi.get).toHaveBeenCalledWith(ACTIVITY_URL, { params: { window: '24h' } })
    expect(screen.getByRole('button', { name: '24 小时' })).toHaveAttribute('aria-pressed', 'true')
  })

  it('切换窗口后按新窗口重新请求', async () => {
    login(10)
    mockedApi.get.mockResolvedValue({ data: { window: '168h', generatedAt: '', items: [] } })

    const user = userEvent.setup()
    renderWithProviders(<McpActivityPage />)
    await user.click(screen.getByRole('button', { name: '7 天' }))

    await waitFor(() => {
      expect(mockedApi.get).toHaveBeenCalledWith(ACTIVITY_URL, { params: { window: '168h' } })
    })
    expect(screen.getByRole('button', { name: '7 天' })).toHaveAttribute('aria-pressed', 'true')
  })

  it('空态显示提示', async () => {
    login(10)
    mockedApi.get.mockResolvedValue({ data: { window: '24h', generatedAt: '', items: [] } })

    renderWithProviders(<McpActivityPage />)
    expect(await screen.findByText(/当前窗口内无 MCP 活动|No MCP activity/i)).toBeInTheDocument()
  })

  it('吊销 Token 入口指向 Agent Token 页（无会话可踢）', async () => {
    login(10)
    mockedApi.get.mockResolvedValue({ data: { window: '24h', generatedAt: '', items: [] } })

    renderWithProviders(<McpActivityPage />)
    expect(await screen.findByRole('link', { name: /吊销 Token|Revoke Token/i })).toHaveAttribute(
      'href',
      '/agent-tokens',
    )
    // 会话时代的踢线按钮不得残留
    expect(screen.queryByRole('button', { name: /踢线|Kick/i })).not.toBeInTheDocument()
  })
})
