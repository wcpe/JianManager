import { describe, it, expect, beforeEach } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import InstanceLibrary from './InstanceLibrary'

/**
 * 超级工作台实例库（FR-167）虚拟化 + 服务端搜索强断言：
 * ① 折叠态只出展开按钮；② 展开态走 /instances/search 渲染 seed；
 * ③ 1000+ 实例只挂可视窗口（DOM 行数受限，不全量挂载）；
 * ④ 搜索下发服务端 q；⑤ 展开按钮有 aria-label；⑥ 加载态出骨架。
 */
/** 一个小而确定的 /instances/search 桩（默认共享 seed 有 1000+，目标实例未必落首页）。 */
function stubSmallSearch(items: Array<{ id: number; name: string; status?: string }>) {
  server.use(
    http.get(API('/instances/search'), () =>
      HttpResponse.json({
        items: items.map((i) => ({ nodeId: 1, role: 'backend', status: 'STOPPED', serverPort: 0, uuid: `i-${i.id}`, ...i })),
        total: items.length,
        page: 1,
        pageSize: 50,
      }),
    ),
  )
}

describe('InstanceLibrary（mock 假后端）', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('折叠态仅渲染展开按钮', () => {
    renderWithProviders(<InstanceLibrary collapsed onToggleCollapsed={() => {}} />)
    // 折叠态用 superWorkbench.expandLibrary 作 aria-label。
    expect(screen.getByRole('button')).toBeInTheDocument()
    expect(screen.queryByTestId('instance-library-virtual')).not.toBeInTheDocument()
  })

  it('展开态走 /instances/search 渲染 seed 实例', async () => {
    stubSmallSearch([{ id: 1, name: 'survival-1' }, { id: 2, name: 'lobby-proxy' }])
    renderWithProviders(<InstanceLibrary collapsed={false} onToggleCollapsed={() => {}} />)
    expect(await screen.findByText('survival-1')).toBeInTheDocument()
    expect(screen.getByText('lobby-proxy')).toBeInTheDocument()
  })

  it('1000+ 实例只挂可视窗口（虚拟化后 DOM 行数受限）', async () => {
    const many = Array.from({ length: 1000 }, (_, i) => ({
      id: i + 1,
      uuid: `i-${i + 1}`,
      name: `srv-${i + 1}`,
      nodeId: 1,
      role: 'backend',
      status: 'STOPPED',
      serverPort: 0,
    }))
    server.use(
      http.get(API('/instances/search'), ({ request }) => {
        const url = new URL(request.url)
        const page = Number(url.searchParams.get('page') ?? 1)
        const pageSize = Number(url.searchParams.get('pageSize') ?? 50)
        const start = (page - 1) * pageSize
        return HttpResponse.json({
          items: many.slice(start, start + pageSize),
          total: many.length,
          page,
          pageSize,
        })
      }),
    )
    renderWithProviders(<InstanceLibrary collapsed={false} onToggleCollapsed={() => {}} />)

    const surface = await screen.findByTestId('instance-library-virtual')
    expect(Number(surface.dataset.totalCount)).toBe(1000)
    await screen.findByText('srv-1')
    // 虚拟化：可视窗口远少于总数（即便首页仅 50 也应只挂可视行 + overscan）。
    const rows = within(surface).getAllByRole('listitem')
    expect(rows.length).toBeLessThan(50)
  })

  it('搜索输入防抖后下发服务端 q', async () => {
    const user = userEvent.setup()
    const seen: string[] = []
    server.use(
      http.get(API('/instances/search'), ({ request }) => {
        const q = new URL(request.url).searchParams.get('q')
        if (q) seen.push(q)
        return HttpResponse.json({ items: [], total: 0, page: 1, pageSize: 50 })
      }),
    )
    renderWithProviders(<InstanceLibrary collapsed={false} onToggleCollapsed={() => {}} />)

    const input = await screen.findByRole('searchbox')
    await user.type(input, 'lobby')
    await waitFor(() => expect(seen).toContain('lobby'))
  })

  it('实例行展开按钮带 aria-label', async () => {
    stubSmallSearch([{ id: 1, name: 'survival-1' }])
    renderWithProviders(<InstanceLibrary collapsed={false} onToggleCollapsed={() => {}} />)
    await screen.findByText('survival-1')
    // 展开按钮 aria-label 含实例名（英文占位「Toggle functions for …」，主会话补键后改中文）。
    expect(
      screen.getByRole('button', { name: /survival-1/i }),
    ).toBeInTheDocument()
  })

  it('展开实例后追加功能子项行', async () => {
    const user = userEvent.setup()
    stubSmallSearch([{ id: 1, name: 'survival-1' }])
    renderWithProviders(<InstanceLibrary collapsed={false} onToggleCollapsed={() => {}} />)
    await screen.findByText('survival-1')

    const surface = screen.getByTestId('instance-library-virtual')
    const before = within(surface).getAllByRole('listitem').length

    // 点 survival-1 行的展开按钮 → 追加 CARD_TYPES 功能子项行。
    await user.click(screen.getByRole('button', { name: /survival-1/i }))
    await waitFor(() => {
      expect(within(surface).getAllByRole('listitem').length).toBeGreaterThan(before)
    })
  })

  it('加载态渲染骨架而非裸文字', async () => {
    server.use(
      http.get(API('/instances/search'), async () => {
        // 永挂起 → 保持 isLoading，断言骨架。
        await new Promise(() => {})
        return HttpResponse.json({ items: [], total: 0, page: 1, pageSize: 50 })
      }),
    )
    renderWithProviders(<InstanceLibrary collapsed={false} onToggleCollapsed={() => {}} />)
    expect(await screen.findByTestId('instance-library-skeleton')).toBeInTheDocument()
  })
})
