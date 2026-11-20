import { describe, it, expect, beforeEach, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import UserTokensPage from './UserTokensPage'
import { userTokenStatusLevel } from '@/lib/auth/status'
import { userTokenStatus, normalize, type UserTokenInfo } from '@/api/userTokens'
import api from '@/api/client'

// DangerConfirm 等对话框组件依赖 ResizeObserver，jsdom 未实现需垫片。
if (!('ResizeObserver' in globalThis)) {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
}

vi.mock('@/api/client', () => ({
  default: { get: vi.fn(), post: vi.fn(), delete: vi.fn(), put: vi.fn() },
}))

const mockedApi = api as unknown as {
  get: ReturnType<typeof vi.fn>
  post: ReturnType<typeof vi.fn>
  delete: ReturnType<typeof vi.fn>
}

/** 构造一条令牌记录（默认有效、未吊销）。 */
function makeToken(over: Partial<UserTokenInfo> = {}): UserTokenInfo {
  return {
    id: 1,
    name: '我的笔记本',
    deviceLabel: 'laptop (windows)',
    tokenPrefix: 'jmut_abcd',
    scopedNodeIds: [],
    scopedInstanceIds: [],
    capabilities: [],
    expiresAt: new Date(Date.now() + 86400_000).toISOString(),
    revoked: false,
    lastUsedAt: null,
    createdAt: new Date().toISOString(),
    ...over,
  }
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('userTokenStatus', () => {
  it('未吊销且未过期 → active', () => {
    expect(userTokenStatus(makeToken())).toBe('active')
  })

  it('已吊销 → revoked（优先于过期判断）', () => {
    expect(
      userTokenStatus(makeToken({ revoked: true, expiresAt: '2000-01-01T00:00:00Z' })),
    ).toBe('revoked')
  })

  it('已过期 → expired', () => {
    expect(userTokenStatus(makeToken({ expiresAt: '2000-01-01T00:00:00Z' }))).toBe('expired')
  })
})

describe('userTokenStatusLevel', () => {
  it('映射到 StatusBadge 档位', () => {
    expect(userTokenStatusLevel('active')).toBe('success')
    expect(userTokenStatusLevel('revoked')).toBe('danger')
    expect(userTokenStatusLevel('expired')).toBe('warning')
  })
})

describe('normalize', () => {
  it('null 数组兜底为空数组', () => {
    const raw = {
      ...makeToken(),
      scopedNodeIds: null,
      scopedInstanceIds: null,
      capabilities: null,
    } as unknown as UserTokenInfo
    const out = normalize(raw)
    expect(out.scopedNodeIds).toEqual([])
    expect(out.scopedInstanceIds).toEqual([])
    expect(out.capabilities).toEqual([])
  })
})

describe('UserTokensPage', () => {
  it('空列表显示引导文案', async () => {
    mockedApi.get.mockResolvedValue({ data: { tokens: [], count: 0 } })
    renderWithProviders(<UserTokensPage />)

    await waitFor(() => {
      expect(screen.getByText(/还没有令牌/)).toBeInTheDocument()
    })
  })

  it('列出令牌并展示设备与前缀', async () => {
    mockedApi.get.mockResolvedValue({ data: { tokens: [makeToken()], count: 1 } })
    renderWithProviders(<UserTokensPage />)

    await waitFor(() => {
      expect(screen.getByText('我的笔记本')).toBeInTheDocument()
    })
    expect(screen.getByText('laptop (windows)')).toBeInTheDocument()
    expect(screen.getByText('jmut_abcd…')).toBeInTheDocument()
  })

  it('已吊销的令牌不显示吊销按钮', async () => {
    mockedApi.get.mockResolvedValue({
      data: { tokens: [makeToken({ revoked: true })], count: 1 },
    })
    renderWithProviders(<UserTokensPage />)

    await waitFor(() => {
      expect(screen.getByText('我的笔记本')).toBeInTheDocument()
    })
    expect(screen.queryByRole('button', { name: '吊销' })).not.toBeInTheDocument()
  })

  it('创建成功后展示一次性明文', async () => {
    mockedApi.get.mockResolvedValue({ data: { tokens: [], count: 0 } })
    mockedApi.post.mockResolvedValue({
      data: { token: makeToken(), plaintext: 'jmut_PLAINTEXT_ONCE', notice: '只显示一次' },
    })
    renderWithProviders(<UserTokensPage />)

    await waitFor(() => expect(screen.getByText(/还没有令牌/)).toBeInTheDocument())

    await userEvent.click(screen.getByRole('button', { name: /新建令牌/ }))
    const nameInput = await screen.findByLabelText('名称')
    await userEvent.type(nameInput, '测试机')
    await userEvent.click(screen.getByRole('button', { name: /^新建令牌$/ }))

    await waitFor(() => {
      expect(screen.getByText('jmut_PLAINTEXT_ONCE')).toBeInTheDocument()
    })
    // 明文只展示一次，应有明确提示。
    expect(screen.getByText(/只显示一次/)).toBeInTheDocument()
  })

  it('加载失败时展示后端错误文案', async () => {
    mockedApi.get.mockRejectedValue({ response: { data: { message: '后端炸了' } } })
    renderWithProviders(<UserTokensPage />)

    await waitFor(() => {
      expect(screen.getByText('后端炸了')).toBeInTheDocument()
    })
  })
})
