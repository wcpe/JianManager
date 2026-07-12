import { describe, it, expect, beforeEach } from 'vitest'
import { fireEvent, screen, within, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse, delay } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@/mocks/server'
import { API } from '@/mocks/api'
import { useConsoleStore } from '@/stores/console'
import ServerSelector from './ServerSelector'

const favorite = { id: 1, name: 'survival-1', uuid: 'i-survival', nodeId: 1, status: 'RUNNING' }
const recent = { id: 3, name: 'creative-1', uuid: 'i-creative', nodeId: 2, status: 'CRASHED' }

describe('ServerSelector DOM', () => {
  beforeEach(() => {
    loginMockUser()
    useConsoleStore.setState({ selectedNodeId: null })
    localStorage.setItem('server-selector.favorites', JSON.stringify([favorite]))
    localStorage.setItem('server-selector.recent', JSON.stringify([recent]))
  })

  it('1000+ 数据只渲染可视窗口，并展示搜索、分组、最近与收藏', async () => {
    const user = userEvent.setup()
    renderWithProviders(<ServerSelector />)

    await user.click(screen.getByRole('button', { name: '选择服务器' }))

    expect(await screen.findByRole('dialog', { name: '服务器选择器' })).toBeInTheDocument()
    expect(screen.getByRole('searchbox', { name: '搜索服务器' })).toBeInTheDocument()
    expect(screen.getByText('最近')).toBeInTheDocument()
    expect(screen.getByText('收藏')).toBeInTheDocument()
    expect(screen.getByText('creative-1')).toBeInTheDocument()
    expect(screen.getByText('survival-1')).toBeInTheDocument()

    const surface = await screen.findByTestId('server-selector-virtual')
    await waitFor(() => expect(Number(surface.dataset.totalCount)).toBeGreaterThanOrEqual(1000))
    expect(screen.queryAllByTestId('server-selector-row').length).toBeLessThan(80)

    await user.click(screen.getByRole('button', { name: '按状态' }))
    expect(await screen.findByText('RUNNING')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '按节点' }))
    await waitFor(() => {
      expect(screen.getAllByTestId('server-selector-group').some((el) => el.textContent?.includes('alpha'))).toBe(true)
    })
  })

  it('模态渲染到 body（Portal），不困在侧栏包含块内', async () => {
    const user = userEvent.setup()
    const { container } = renderWithProviders(<ServerSelector />)

    await user.click(screen.getByRole('button', { name: '选择服务器' }))
    const dialog = await screen.findByRole('dialog', { name: '服务器选择器' })

    // 侧栏祖先带 transform/contain（FR-131）会给 fixed 建立包含块、把全屏模态压到侧栏宽度内；
    // Portal 到 body 后模态不再是本组件渲染容器的后代，逃出该包含块。
    expect(container.contains(dialog)).toBe(false)
    expect(document.body.contains(dialog)).toBe(true)
  })

  it('搜索和空态可用，节点作用域透传到服务端查询', async () => {
    const user = userEvent.setup()
    useConsoleStore.setState({ selectedNodeId: 2 })
    const seen: Record<string, string>[] = []
    server.use(
      http.get(API('/instances/search'), ({ request }) => {
        const params = Object.fromEntries(new URL(request.url).searchParams.entries())
        seen.push(params)
        return HttpResponse.json({ items: [], total: 0, page: 1, pageSize: 200 })
      }),
      http.get(API('/instances/aggregate'), ({ request }) => {
        seen.push(Object.fromEntries(new URL(request.url).searchParams.entries()))
        return HttpResponse.json({
          total: 0,
          byStatus: { RUNNING: 0, STOPPED: 0, CRASHED: 0, STARTING: 0, STOPPING: 0 },
          byNode: [],
          byRole: { backend: 0, proxy: 0, universal: 0 },
        })
      }),
    )

    renderWithProviders(<ServerSelector />)
    await user.click(screen.getByRole('button', { name: '选择服务器' }))
    await user.type(screen.getByRole('searchbox', { name: '搜索服务器' }), 'not-found')

    expect(await screen.findByText('没有符合条件的服务器')).toBeInTheDocument()
    await waitFor(() => expect(seen.some((p) => p.q === 'not-found' && p.nodeId === '2')).toBe(true))
  })

  it('加载态可见', async () => {
    const user = userEvent.setup()
    server.use(
      http.get(API('/instances/search'), async () => {
        await delay(120)
        return HttpResponse.json({ items: [], total: 0, page: 1, pageSize: 200 })
      }),
    )

    renderWithProviders(<ServerSelector />)
    await user.click(screen.getByRole('button', { name: '选择服务器' }))

    const dialog = await screen.findByRole('dialog', { name: '服务器选择器' })
    expect(within(dialog).getByText('加载中...')).toBeInTheDocument()
  })

  it('行稳定悬停 150ms 预取实例详情，快速掠过不请求（FR-297）', async () => {
    const user = userEvent.setup()
    // 经请求事件计数实例详情请求（GET /instances/:数字id，天然排除 search/aggregate 子路径）。
    const detailHits: string[] = []
    const listener = ({ request }: { request: Request }) => {
      const match = new URL(request.url).pathname.match(/\/api\/v1\/instances\/(\d+)$/)
      if (match) detailHits.push(match[1])
    }
    server.events.on('request:start', listener)
    try {
      renderWithProviders(<ServerSelector />)
      await user.click(screen.getByRole('button', { name: '选择服务器' }))
      const rows = await screen.findAllByTestId('server-selector-row')

      // 快速掠过：进入后立即离开，防抖期内取消，不发预取请求。
      fireEvent.mouseEnter(rows[0])
      fireEvent.mouseLeave(rows[0])
      await new Promise((resolve) => setTimeout(resolve, 250))
      expect(detailHits).toHaveLength(0)

      // 稳定悬停：超过 150ms 触发一次实例详情预取。
      fireEvent.mouseEnter(rows[0])
      await waitFor(() => expect(detailHits.length).toBe(1))
    } finally {
      server.events.removeListener('request:start', listener)
    }
  })
})
