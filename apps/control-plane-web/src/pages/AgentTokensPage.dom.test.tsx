import { describe, it, expect, beforeEach, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
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
      if (url === '/instances/search') return { data: { items: [], total: 0, page: 1, pageSize: 50 } }
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

  /**
   * 千级实例的判据：实例数是千级（大档 1200），签发对话框的实例多选区**不得**全量列举——
   * 原先容器 `useInstances()` 无门控地拉全量、弹窗里再挂 1200 × 4 个元素，现改为服务端搜索
   * （默认前 N 条 + 键入 300ms 防抖下发 `q`）+ 虚拟化候选 + 截断提示 + 已选回显。
   */
  it('实例候选走服务端搜索：不下发全量 /instances，键入后带 q，且已选项在窗口外也能回显', async () => {
    login(10)
    mockedApi.get.mockImplementation(
      async (url: string, cfg?: { params?: Record<string, unknown> }) => {
        if (url === '/agent/tokens') return { data: [] }
        if (url === '/instances/search') {
          const q = cfg?.params?.q
          // 带 q 的搜索按服务端语义只回命中项（此处故意不回任何项，验证「候选窗口随 q 变化」）。
          return {
            data: {
              items: q ? [] : [{ id: 7, name: 'survival-07', status: 'RUNNING' }],
              total: q ? 0 : 1200,
              page: 1,
              pageSize: 50,
            },
          }
        }
        if (url === '/nodes') return { data: [{ id: 1, name: 'node-a' }] }
        return { data: [] }
      },
    )

    const user = userEvent.setup()
    renderWithProviders(<AgentTokensPage />)
    await screen.findByText(/暂无 Agent Token|No Agent Tokens/)

    // 弹窗打开前不发实例候选请求（取数时机与节点候选一致）。
    const searchCalls = () =>
      mockedApi.get.mock.calls.filter(([url]) => url === '/instances/search') as [
        string,
        { params?: Record<string, unknown> },
      ][]
    expect(searchCalls()).toEqual([])

    await user.click(screen.getByRole('button', { name: /新建 Token|New Token/ }))

    // 打开后按服务端搜索取候选窗口（默认前 N 条），而不是 /instances 全量列举。
    const searchBox = await screen.findByLabelText('输入实例名筛选')
    await waitFor(() =>
      expect(searchCalls().some(([, cfg]) => cfg?.params?.pageSize === 50)).toBe(true),
    )
    expect(mockedApi.get.mock.calls.filter(([url]) => url === '/instances')).toEqual([])
    // 服务端截断 → 提示继续输入缩小范围。
    expect(screen.getByText(/已显示前 1 项，共 1200 项/)).toBeInTheDocument()

    // 勾选后立即可见「已勾选 + 实例」回显（键入收窄候选后仍能看见勾了什么）。
    await user.click(screen.getByRole('checkbox', { name: 'survival-07' }))
    const picked = screen.getByText('已勾选 1 个实例').parentElement as HTMLElement
    expect(within(picked).getByText('#7 survival-07')).toBeInTheDocument()

    // 键入 → 300ms 防抖后下发带 q 的服务端搜索（而非本地过滤全量列表）。
    await user.type(searchBox, 'lobby')
    await waitFor(
      () => expect(searchCalls().some(([, cfg]) => cfg?.params?.q === 'lobby')).toBe(true),
      { timeout: 2000 },
    )
    // 候选窗口已随 q 变化（服务端未命中 → 候选空态），而已勾选项仍留在回显里。
    await waitFor(() => expect(screen.getByText('暂无实例')).toBeInTheDocument())
    expect(within(picked).getByText('#7 survival-07')).toBeInTheDocument()

    // 关窗重开：搜索框草稿与容器里的关键字一起归零，候选回到未过滤窗口
    // （否则会出现「框是空的、候选却仍被上次关键字挡住」）。
    await user.click(screen.getByRole('button', { name: /取消|Cancel/ }))
    await waitFor(() => expect(screen.queryByLabelText('输入实例名筛选')).not.toBeInTheDocument())
    const before = searchCalls().length
    await user.click(screen.getByRole('button', { name: /新建 Token|New Token/ }))
    expect(await screen.findByLabelText('输入实例名筛选')).toHaveValue('')
    await waitFor(() => expect(searchCalls().length).toBeGreaterThan(before))
    // 关键字归零经 300ms 防抖才下发，故最终那次候选请求必须是不带 q 的未过滤窗口。
    await waitFor(
      () => expect(searchCalls()[searchCalls().length - 1]?.[1]?.params?.q).toBeUndefined(),
      { timeout: 2000 },
    )
  })

  it('创建成功展示一次性明文与 JM_AGENT_TOKEN，并提交 V2 payload', async () => {
    login(10)
    mockedApi.get.mockImplementation(async (url: string) => {
      if (url === '/agent/tokens') return { data: [] }
      if (url === '/instances/search')
        return { data: { items: [{ id: 1, name: 'survival', status: 'STOPPED' }], total: 1, page: 1, pageSize: 50 } }
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

