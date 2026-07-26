import { describe, it, expect, beforeEach, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { useAuthStore } from '@/stores/auth'
import AgentTokensPage, { parseIdInput, mergeIds, formatScopeSummary } from './AgentTokensPage'
import api from '@/api/client'

// 对话框内 Radix Checkbox 依赖 ResizeObserver，jsdom 未实现，需垫片（同备份存储页先例）。
if (!('ResizeObserver' in globalThis)) {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
}

vi.mock('@/api/client', () => ({
  default: {
    get: vi.fn(),
    post: vi.fn(),
    delete: vi.fn(),
  },
}))

const mockedApi = api as unknown as {
  get: ReturnType<typeof vi.fn>
  post: ReturnType<typeof vi.fn>
  delete: ReturnType<typeof vi.fn>
}

const makeToken = (userId: number, username: string, role: number) =>
  `mock.${btoa(JSON.stringify({ userId, username, role, exp: Math.floor(Date.now() / 1000) + 900 }))}.sig`

function login(role: number) {
  const token = makeToken(1, role === 10 ? 'admin' : 'member', role)
  useAuthStore.getState().login(token, 'r-1')
}

describe('parseIdInput / mergeIds / formatScopeSummary', () => {
  it('解析 ID 输入并去重', () => {
    expect(parseIdInput('1, 2 3;3')).toEqual([1, 2, 3])
    expect(parseIdInput('')).toEqual([])
    expect(parseIdInput('a,0,-1,1.5')).toEqual([])
  })

  it('合并多选与手输 ID', () => {
    expect(mergeIds([1, 2], '2,3')).toEqual([1, 2, 3])
  })

  it('scope 摘要', () => {
    expect(
      formatScopeSummary([1], [2, 3], { instances: '实例', nodes: '节点', none: '无' }),
    ).toBe('实例 1 · 节点 2,3')
    expect(formatScopeSummary([], [], { instances: '实例', nodes: '节点', none: '无' })).toBe('无')
  })
})

describe('AgentTokensPage（DOM）', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
    useAuthStore.getState().logout()
  })

  it('非平台管理员显示无权限', () => {
    login(0)
    renderWithProviders(<AgentTokensPage />)
    expect(screen.getByText(/仅平台管理员可访问|Platform admins only/)).toBeInTheDocument()
    expect(mockedApi.get).not.toHaveBeenCalled()
  })

  it('管理员渲染列表行与吊销入口（V1 兼容展示）', async () => {
    login(10)
    mockedApi.get.mockImplementation(async (url: string) => {
      if (url === '/agent/tokens') {
        return {
          data: [
            {
              id: 1,
              name: 'ci-bot',
              tokenPrefix: 'jmat_ab12',
              scopedInstanceIds: '[1]',
              scopedNodeIds: '[]',
              writeAllowlist: '["instance.life","node.maintenance"]',
              expiresAt: '2099-12-31T00:00:00Z',
              revoked: false,
              createdAt: '2026-07-01T00:00:00Z',
              createdBy: 1,
            },
          ],
        }
      }
      if (url === '/instances') return { data: [] }
      if (url === '/nodes') return { data: [] }
      return { data: [] }
    })

    renderWithProviders(<AgentTokensPage />)
    expect(await screen.findByText('ci-bot')).toBeInTheDocument()
    expect(screen.getByText(/jmat_ab12/)).toBeInTheDocument()
    expect(screen.getByText(/V1/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /吊销|Revoke/ })).toBeInTheDocument()
  })

  it('V2 Token 列表展示能力与策略版本', async () => {
    login(10)
    mockedApi.get.mockImplementation(async (url: string) => {
      if (url === '/agent/tokens') {
        return {
          data: [
            {
              id: 2,
              name: 'v2-bot',
              tokenPrefix: 'jmat_v2xx',
              scopedInstanceIds: '[]',
              scopedNodeIds: '[3]',
              writeAllowlist: '[]',
              policyVersion: 2,
              capabilities: ['instance.read', 'node.read'],
              expiresAt: '2099-12-31T00:00:00Z',
              revoked: false,
              createdAt: '2026-07-26T00:00:00Z',
              createdBy: 1,
            },
          ],
        }
      }
      return { data: [] }
    })

    renderWithProviders(<AgentTokensPage />)
    expect(await screen.findByText('v2-bot')).toBeInTheDocument()
    expect(screen.getByText(/V2/)).toBeInTheDocument()
  })

  it('创建成功展示一次性明文与 JM_AGENT_TOKEN，并提交 V2 payload', async () => {
    login(10)
    mockedApi.get.mockImplementation(async (url: string) => {
      if (url === '/agent/tokens') return { data: [] }
      if (url === '/instances') return { data: [{ id: 1, name: 'survival', status: 'STOPPED' }] }
      if (url === '/nodes') return { data: [{ id: 1, name: 'node-a' }] }
      return { data: [] }
    })
    mockedApi.post.mockResolvedValue({
      data: {
        token: {
          id: 9,
          name: 'cursor-dev',
          tokenPrefix: 'jmat_xy99',
          scopedInstanceIds: '[1]',
          scopedNodeIds: '[]',
          writeAllowlist: '[]',
          policyVersion: 2,
          capabilities: ['node.read', 'instance.read', 'observability.read'],
          expiresAt: '2099-01-01T00:00:00Z',
          revoked: false,
          createdAt: '2026-07-23T00:00:00Z',
          createdBy: 1,
        },
        plaintext: 'jmat_xy99secretplaintext',
      },
    })

    const user = userEvent.setup()
    renderWithProviders(<AgentTokensPage />)
    await screen.findByText(/暂无 Agent Token|No Agent Tokens/)
    await user.click(screen.getByRole('button', { name: /新建 Token|New Token/ }))
    const nameInput = await screen.findByLabelText(/名称|Name/)
    await user.type(nameInput, 'cursor-dev')
    await user.click(screen.getByRole('button', { name: /签发|Issue/ }))

    await waitFor(() => {
      expect(mockedApi.post).toHaveBeenCalledWith(
        '/agent/tokens',
        expect.objectContaining({
          name: 'cursor-dev',
          policyVersion: 2,
          capabilities: expect.arrayContaining(['node.read', 'instance.read', 'observability.read']),
        }),
      )
    })
    const body = mockedApi.post.mock.calls[0][1] as Record<string, unknown>
    expect(body).not.toHaveProperty('writeAllowlist')
    expect(await screen.findByText('jmat_xy99secretplaintext')).toBeInTheDocument()
    expect(screen.getByText(/JM_AGENT_TOKEN=jmat_xy99secretplaintext/)).toBeInTheDocument()
  })
})

