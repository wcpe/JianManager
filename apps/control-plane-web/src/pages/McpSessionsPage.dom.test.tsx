import { describe, it, expect, beforeEach, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { useAuthStore } from '@/stores/auth'
import api from '@/api/client'
import McpSessionsPage from './McpSessionsPage'

vi.mock('@/api/client', () => ({
  default: {
    get: vi.fn(),
    delete: vi.fn(),
  },
}))

const mockedApi = api as unknown as {
  get: ReturnType<typeof vi.fn>
  delete: ReturnType<typeof vi.fn>
}

const makeToken = (userId: number, username: string, role: number) =>
  `mock.${btoa(JSON.stringify({ userId, username, role, exp: Math.floor(Date.now() / 1000) + 900 }))}.sig`

function login(role: number) {
  const token = makeToken(1, role === 10 ? 'admin' : 'member', role)
  useAuthStore.getState().login(token, 'r-1')
}

describe('McpSessionsPage（DOM）', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
    useAuthStore.getState().logout()
  })

  it('非平台管理员显示无权限', () => {
    login(0)
    renderWithProviders(<McpSessionsPage />)
    expect(screen.getByText(/仅平台管理员|Platform admin/i)).toBeInTheDocument()
    expect(mockedApi.get).not.toHaveBeenCalled()
  })

  it('管理员渲染会话列表与踢线按钮', async () => {
    login(10)
    mockedApi.get.mockImplementation(async (url: string) => {
      if (url === '/agent/mcp/sessions') {
        return {
          data: {
            sessions: [
              {
                sessionId: 'mcps_test123',
                tokenId: 1,
                tokenName: 'ci-bot',
                tokenPrefix: 'jmat_ab12',
                clientIP: '10.0.0.1',
                transport: 'streamable_http',
                connectedAt: '2026-07-25T06:00:00Z',
                lastActivityAt: '2026-07-25T06:05:00Z',
                lastTool: 'agent_whoami',
              },
            ],
            config: {
              idleTimeout: '30m',
              absoluteTimeout: '24h',
              maxGlobalSessions: 32,
              maxSessionsPerToken: 4,
            },
          },
        }
      }
      return { data: {} }
    })

    renderWithProviders(<McpSessionsPage />)
    expect(await screen.findByText('ci-bot')).toBeInTheDocument()
    expect(screen.getByText(/jmat_ab12/)).toBeInTheDocument()
    expect(screen.getByText('agent_whoami')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /踢线|Kick/i })).toBeInTheDocument()
    // 超时配置展示
    expect(screen.getByText(/30m/)).toBeInTheDocument()
    expect(screen.getByText(/24h/)).toBeInTheDocument()
  })

  it('空态显示提示', async () => {
    login(10)
    mockedApi.get.mockResolvedValue({ data: { sessions: [] } })

    renderWithProviders(<McpSessionsPage />)
    expect(await screen.findByText(/当前无活跃|No active/i)).toBeInTheDocument()
  })

  it('踢线成功后刷新列表', async () => {
    login(10)
    let callCount = 0
    mockedApi.get.mockImplementation(async (url: string) => {
      if (url === '/agent/mcp/sessions') {
        callCount++
        return {
          data: {
            sessions: callCount === 1
              ? [{ sessionId: 'mcps_kickme', tokenId: 1, tokenName: 'temp', tokenPrefix: 'jmat_xx', clientIP: '', transport: 'streamable_http', connectedAt: '', lastActivityAt: '' }]
              : [],
          },
        }
      }
      return { data: {} }
    })
    mockedApi.delete.mockResolvedValue({ data: { ok: true } })

    const user = userEvent.setup()
    renderWithProviders(<McpSessionsPage />)
    expect(await screen.findByText('temp')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /踢线|Kick/i }))

    await waitFor(() => {
      expect(mockedApi.delete).toHaveBeenCalledWith('/agent/mcp/sessions/mcps_kickme')
    })
  })
})
