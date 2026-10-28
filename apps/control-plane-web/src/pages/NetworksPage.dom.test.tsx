import { describe, it, expect, beforeEach } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@jianmanager/devmock/inject'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import NetworksPage from './NetworksPage'
import { NetworkInstancePickerView } from '@/components/views/networks/NetworksPageView'
import { WORKSPACES } from '@/lib/console/workspace-navigation'

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
  it('拓扑深链直接渲染拓扑视图，避免两个侧栏入口落到同一列表', async () => {
    renderWithProviders(<NetworksPage />, { route: '/networks/topology' })

    expect(await screen.findByText('群组服拓扑')).toBeInTheDocument()
    expect(screen.queryByText('survival')).not.toBeInTheDocument()
  })

  it('侧栏群组网络两个入口分别指向分组管理与拓扑页', () => {
    // FR-507 阶段 6：侧栏改以四工作区为真源（原六域的 `groupNetwork` 已归入
    // 「服务器运维 → 群组与工作台」分组）。两个入口的顺序不变。
    const group = WORKSPACES.flatMap((ws) => ws.groups).find((item) => item.key === 'opsGroupsWorkbench')
    const targets = group?.children?.map((item) => item.to)

    expect(targets?.slice(0, 2)).toEqual(['/networks/topology', '/networks'])
  })

  it('① 渲染出种子群组（名称 + 成员数）', async () => {
    const { container } = renderWithProviders(<NetworksPage />, { route: '/networks' })

    expect(container.firstElementChild).toHaveAttribute('data-page', 'networks')
    // 阶段 6 页面迁移起，外壳改用布局层 PageShell（原为手写的 jm-page-stack 骨架类）。
    // data-page 由 PageShell spread 透传，故上一行断言不变；这里改认它的标识。
    expect(container.firstElementChild).toHaveAttribute('data-slot', 'page-shell')
    expect(screen.getByRole('button', { name: '创建群组' })).toHaveAttribute('data-slot', 'button')
    expect(screen.getByRole('button', { name: /列表/ }).parentElement).toHaveClass('jm-toolbar-surface')
    expect(await screen.findByText('survival')).toBeInTheDocument()
    expect(screen.getByText('creative')).toBeInTheDocument()
    expect(screen.getByText('minigames')).toBeInTheDocument()
    // memberCount 经 {{count}} 插值：survival 3 / minigames 0。
    expect(screen.getByText('3 个成员')).toBeInTheDocument()
    expect(screen.getByText('0 个成员')).toBeInTheDocument()
  })

  it('①-1 列表健康分布读概要 memberStatus 计数，零 per-network 详情请求（FR-335）', async () => {
    const perNetwork: string[] = []
    const listener = ({ request }: { request: Request }) => {
      const url = new URL(request.url)
      // GET /networks/:id 形态（列表页不应触发）。
      if (/\/api\/v1\/networks\/\d+$/.test(url.pathname) && request.method === 'GET') {
        perNetwork.push(url.pathname)
      }
    }
    server.events.on('request:start', listener)
    try {
      renderWithProviders(<NetworksPage />, { route: '/networks' })
      // survival 种子 3 成员：2 RUNNING + 1 CRASHED → 健康分布直接来自概要计数。
      await screen.findByText('survival')
      await waitFor(() => expect(screen.getByText(/运行 2/)).toBeInTheDocument())
      expect(screen.getByText(/崩溃 1/)).toBeInTheDocument()
      // 列表页零 per-network 详情请求。
      expect(perNetwork).toHaveLength(0)
    } finally {
      server.events.removeListener('request:start', listener)
    }
  })

  it('② 创建群组 → 列表联动出现新行', async () => {
    const user = userEvent.setup()
    renderWithProviders(<NetworksPage />, { route: '/networks' })
    await screen.findByText('survival')

    await user.click(screen.getByRole('button', { name: '创建群组' }))
    // 模态打开：唯一 placeholder=survival 的名称输入框；提交按钮名「创建」(≠ 打开按钮「创建群组」)。
    const dialog = await screen.findByRole('dialog', { name: '创建群组' })
    await user.type(within(dialog).getByPlaceholderText('survival'), 'hardcore')
    await user.click(within(dialog).getByRole('button', { name: '创建' }))

    // 创建成功 → invalidate ['networks'] → 列表重拉，新群组出现。
    expect(await screen.findByText('hardcore')).toBeInTheDocument()
  })

  it('②-1 创建与详情弹窗走共享 Dialog 并支持 Esc 关闭', async () => {
    const user = userEvent.setup()
    renderWithProviders(<NetworksPage />, { route: '/networks' })
    await screen.findByText('survival')

    await user.click(screen.getByRole('button', { name: '创建群组' }))
    expect(await screen.findByRole('dialog', { name: '创建群组' })).toBeInTheDocument()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog', { name: '创建群组' })).not.toBeInTheDocument())

    const survivalCard = screen.getByText('survival').closest('[data-slot="panel"]') as HTMLElement
    await user.click(within(survivalCard).getByRole('button', { name: '管理' }))
    expect(await screen.findByRole('dialog', { name: 'survival' })).toBeInTheDocument()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'survival' })).not.toBeInTheDocument())
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

  it('③ 注入 500 → 列表降级为错误态（不崩溃、不渲染种子，且不谎报「暂无群组」）', async () => {
    mockInject('get', '/networks', { kind: 'status', status: 500 })
    renderWithProviders(<NetworksPage />, { route: '/networks' })

    // 标题恒在证明未崩溃；种子群组不应出现；错误态与「一个群组都没建」必须区分开。
    expect(await screen.findByText('群组管理')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByText('错误')).toBeInTheDocument())
    expect(screen.queryByText('暂无群组')).not.toBeInTheDocument()
    expect(screen.queryByText('survival')).not.toBeInTheDocument()
  })

  it('④ 详情深链：`?network=abc` 不再渲染空白弹窗', async () => {
    renderWithProviders(<NetworksPage />, { route: '/networks?network=abc' })

    // 列表照常可用（非法参数被忽略），且不出现任何详情弹窗。
    expect(await screen.findByText('survival')).toBeInTheDocument()
    expect(screen.queryAllByRole('dialog')).toHaveLength(0)
  })

  it('④ 详情深链：指向不存在的群组时给出空态提示，而不是空白弹窗', async () => {
    renderWithProviders(<NetworksPage />, { route: '/networks?network=99999' })

    // 弹窗标题退回 #id（不再空白），正文给出「群组已删除」提示。
    const dialog = await screen.findByRole('dialog', { name: '#99999' })
    expect(await within(dialog).findByTestId('network-detail-missing')).toHaveTextContent('群组已删除')
  })

  it('④ 成员列表成员数级时只渲染滚动窗口内的行（虚拟化）', async () => {
    const members = Array.from({ length: 60 }, (_, i) => ({
      instanceId: i + 1,
      name: `member-${i + 1}`,
      role: 'backend',
      status: 'RUNNING',
    }))
    server.use(
      http.get(API('/networks/1'), () =>
        HttpResponse.json({ id: 1, name: 'survival', description: '', members }),
      ),
    )
    renderWithProviders(<NetworksPage />, { route: '/networks?network=1' })

    const dialog = await screen.findByRole('dialog', { name: 'survival' })
    await within(dialog).findByText('member-1')

    const rows = within(dialog).getAllByTestId('network-member-row')
    expect(rows.length).toBeGreaterThan(0)
    expect(rows.length).toBeLessThan(members.length)
    // 窗口首行在、远处的行不在：确实按滚动窗口裁剪，而非整列进 DOM。
    expect(within(dialog).queryByText('member-60')).toBeNull()
  })

  it('⑤ 新建群组弹窗的名称/描述输入框有可访问名', async () => {
    const user = userEvent.setup()
    renderWithProviders(<NetworksPage />, { route: '/networks' })
    await screen.findByText('survival')

    await user.click(screen.getByRole('button', { name: '创建群组' }))
    const dialog = await screen.findByRole('dialog', { name: '创建群组' })

    // FieldLabel htmlFor ↔ Input id 关联：此前两个输入框既无 id 也无 aria-label，读屏拿不到字段名。
    // 必填字段的标签文本带 `*` 与 sr-only 的「必填标记」，故用前缀正则匹配。
    expect(within(dialog).getByLabelText(/^群组名/)).toBeInTheDocument()
    expect(within(dialog).getByLabelText(/^描述/)).toBeInTheDocument()
  })

  it('⑤ 候选截断提示按服务端窗口计数，不被「排除已入组成员」拉低', async () => {
    renderWithProviders(
      <NetworkInstancePickerView
        items={[
          { id: 1, name: 'a', status: 'RUNNING', nodeId: 1, role: 'backend' },
          { id: 2, name: 'b', status: 'RUNNING', nodeId: 1, role: 'backend' },
          { id: 3, name: 'c', status: 'RUNNING', nodeId: 1, role: 'backend' },
        ]}
        total={50}
        selected={[]}
        onToggle={() => {}}
        // 实例 1 已在组内（候选里被排除）：shown 仍应是服务端窗口的 3，而不是排除后的 2。
        memberIds={[1]}
        onQueryChange={() => {}}
        onAdd={() => {}}
      />,
    )

    expect(await screen.findByText('已显示前 3 项，共 50 项；继续输入可缩小范围')).toBeInTheDocument()
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
