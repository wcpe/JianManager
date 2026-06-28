import { describe, it, expect, beforeEach } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import { server } from '@/mocks/server'
import { API } from '@/mocks/api'
import NetworksPage from './NetworksPage'

/**
 * NetworksPage 强断言纵切（FR-203 群组服网络域）：验种子渲染 + 创建联动 + 错误注入。
 * NetworksPage 初始（列表视图）还会拉 GET /instances?role=proxy（拓扑用）——该端点归实例域，
 * 本域 worktree 无之，故用 server.use 临时桩，避免 onUnhandledRequest:'error' 误伤本域断言。
 */
beforeEach(() => {
  loginMockUser()
  server.use(http.get(API('/instances'), () => HttpResponse.json([])))
})

describe('NetworksPage（mock 假后端）', () => {
  it('① 渲染出种子群组（名称 + 成员数）', async () => {
    renderWithProviders(<NetworksPage />, { route: '/networks' })

    expect(await screen.findByText('survival')).toBeInTheDocument()
    expect(screen.getByText('creative')).toBeInTheDocument()
    expect(screen.getByText('minigames')).toBeInTheDocument()
    // memberCount 经 {{count}} 插值：survival 3 / minigames 0。
    expect(screen.getByText('3 个成员')).toBeInTheDocument()
    expect(screen.getByText('0 个成员')).toBeInTheDocument()
  })

  it('② 创建群组 → 列表联动出现新行', async () => {
    const user = userEvent.setup()
    renderWithProviders(<NetworksPage />, { route: '/networks' })
    await screen.findByText('survival')

    await user.click(screen.getByRole('button', { name: '创建群组' }))
    // 模态打开：唯一 placeholder=survival 的名称输入框；提交按钮名「创建」(≠ 打开按钮「创建群组」)。
    await screen.findByRole('heading', { name: '创建群组' })
    await user.type(screen.getByPlaceholderText('survival'), 'hardcore')
    await user.click(screen.getByRole('button', { name: '创建' }))

    // 创建成功 → invalidate ['networks'] → 列表重拉，新群组出现。
    expect(await screen.findByText('hardcore')).toBeInTheDocument()
  })

  it('② 注册 proxy↔backend → registrations 联动反映（M:N 同后端多代理）', async () => {
    // survival-proxy(10) 种子有 lobby(11)/world(12) 两条注册。
    const proxyId = 10
    const before = await fetchJson<unknown[]>(`/api/v1/proxies/${proxyId}/registrations`)
    expect(before).toHaveLength(2)

    // 注册一个新后端(99) → 该代理注册数 +1。
    const created = await fetchJson<{ backendId: number }>(`/api/v1/proxies/${proxyId}/registrations`, {
      method: 'POST',
      body: JSON.stringify({ backendId: 99, alias: 'extra' }),
    })
    expect(created.backendId).toBe(99)

    const after = await fetchJson<{ backendId: number }[]>(`/api/v1/proxies/${proxyId}/registrations`)
    expect(after).toHaveLength(3)
    expect(after.some((r) => r.backendId === 99)).toBe(true)
  })

  it('③ 注入 500 → 列表降级为错误态（不崩溃、不渲染种子）', async () => {
    mockInject('get', '/networks', { kind: 'status', status: 500 })
    renderWithProviders(<NetworksPage />, { route: '/networks' })

    // 标题恒在证明未崩溃；种子群组不应出现；空占位渲染（错误降级）。
    expect(await screen.findByText('群组管理')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByText('暂无群组')).toBeInTheDocument())
    expect(screen.queryByText('survival')).not.toBeInTheDocument()
  })
})

/** 带 mock 鉴权头直发 fetch，验 handler 行为（不经页面/axios 拦截）。 */
async function fetchJson<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(url, {
    ...init,
    headers: { Authorization: 'Bearer test-access-token', 'Content-Type': 'application/json', ...init?.headers },
  })
  return (await res.json()) as T
}
