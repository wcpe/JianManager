import { describe, it, expect, beforeEach, vi } from 'vitest'
import { screen } from '@testing-library/react'
import { renderWithProviders } from '@/test/render'
import { useAuthStore } from '@/stores/auth'
import api from '@/api/client'
import AgentCallLogsPage from './AgentCallLogsPage'

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

describe('AgentCallLogsPage（DOM）', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
    useAuthStore.getState().logout()
  })

  it('非平台管理员显示无权限', () => {
    login(0)
    renderWithProviders(<AgentCallLogsPage />)
    expect(screen.getByText(/仅平台管理员|Platform admin/i)).toBeInTheDocument()
    expect(mockedApi.get).not.toHaveBeenCalled()
  })

  it('管理员渲染流水行（含 client 区分与成功/失败徽章）', async () => {
    login(10)
    mockedApi.get.mockImplementation(async (url: string) => {
      if (url.startsWith('/agent/call-logs')) {
        return {
          data: {
            items: [
              {
                id: 1,
                tokenId: 1,
                tokenName: 'ci-bot',
                action: 'agent.whoami',
                client: 'mcp',
                transport: 'streamable_http',
                success: true,
                latencyMs: 12,
                ip: '10.0.0.1',
                createdAt: '2026-07-25T06:00:00Z',
              },
              {
                id: 2,
                tokenId: 2,
                tokenName: 'ops-cli',
                action: 'agent.instance_stop',
                client: 'jmagent',
                success: false,
                error: 'FORBIDDEN',
                createdAt: '2026-07-25T06:01:00Z',
              },
            ],
            total: 2,
            page: 1,
            pageSize: 50,
          },
        }
      }
      if (url === '/agent/tokens') return { data: [] }
      return { data: [] }
    })

    renderWithProviders(<AgentCallLogsPage />)
    // 等待数据加载
    expect(await screen.findByText('agent.whoami')).toBeInTheDocument()
    expect(screen.getByText('agent.instance_stop')).toBeInTheDocument()
    expect(screen.getByText('ci-bot')).toBeInTheDocument()
    expect(screen.getByText('ops-cli')).toBeInTheDocument()
    // client 列存在
    expect(screen.getByText('12 ms')).toBeInTheDocument()
    // 失败行有错误
    expect(screen.getByText('FORBIDDEN')).toBeInTheDocument()
  })

  it('空态显示提示', async () => {
    login(10)
    mockedApi.get.mockResolvedValue({
      data: { items: [], total: 0, page: 1, pageSize: 50 },
    })

    renderWithProviders(<AgentCallLogsPage />)
    expect(await screen.findByText(/无匹配|No matching/i)).toBeInTheDocument()
  })
})
