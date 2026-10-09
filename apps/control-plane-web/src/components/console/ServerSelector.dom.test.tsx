import { describe, it, expect, beforeEach } from 'vitest'
import { fireEvent, screen, within, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse, delay } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
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

  // 键盘与焦点（FR-496）：弹层是自绘 portal，只声明了 `role="dialog" aria-modal="true"`。
  // 以下用例锁定这个声明的兑现契约——它们都不会抛错，去掉实现后只会静默失效
  // （Esc 关不掉、Tab 跑到遮罩背后的侧栏、关闭后焦点掉到 body），故必须逐条钉住。
  it('Esc 关闭弹层（自绘模态此前没有键盘出口）', async () => {
    const user = userEvent.setup()
    renderWithProviders(<ServerSelector />)

    await user.click(screen.getByRole('button', { name: '选择服务器' }))
    expect(await screen.findByRole('dialog', { name: '服务器选择器' })).toBeInTheDocument()

    await user.keyboard('{Escape}')

    await waitFor(() => {
      expect(screen.queryByRole('dialog', { name: '服务器选择器' })).not.toBeInTheDocument()
    })
  })

  it('打开即聚焦搜索框，Tab / Shift+Tab 在弹层内首尾环绕', async () => {
    const user = userEvent.setup()
    renderWithProviders(<ServerSelector />)

    await user.click(screen.getByRole('button', { name: '选择服务器' }))
    const dialog = await screen.findByRole('dialog', { name: '服务器选择器' })

    // 焦点进入弹层并落在搜索框（呼出即可打字），而不是留在遮罩背后的触发按钮上
    const searchbox = screen.getByRole('searchbox', { name: '搜索服务器' })
    expect(document.activeElement).toBe(searchbox)

    // 等可视窗口出第一批行，弹层内的可聚焦集合才稳定
    await screen.findAllByTestId('server-selector-row')
    const focusables = Array.from(dialog.querySelectorAll<HTMLElement>('button, input'))
    const first = focusables[0]
    // 首 = 标题栏关闭按钮（弹层 Tab 序列的头部）
    expect(first).toBe(within(dialog).getByRole('button', { name: '关闭' }))

    // Shift+Tab 从头部往回退：环绕到弹层另一端，而不是退到遮罩背后的页面。
    // 不写死落点元素：环绕落点由弹层内的 Tab 序列决定（真实浏览器里 = 最后一行行内控件），
    // 而 jsdom 的 querySelectorAll 对逗号选择器组不按文档顺序返回（先 button 后 input），
    // 按元素名断言会把这条用例绑在 jsdom 的选择器实现上。
    first.focus()
    await user.tab({ shift: true })
    const wrapped = document.activeElement
    expect(dialog.contains(wrapped)).toBe(true)
    expect(wrapped).not.toBe(first)

    // 从另一端再 Tab：回到弹层第一个可聚焦元素，闭环成立且中途不落到 body / 背景页
    await user.tab()
    expect(document.activeElement).toBe(first)
  })

  it('关闭后焦点还回触发按钮，不落在 body 上', async () => {
    const user = userEvent.setup()
    renderWithProviders(<ServerSelector />)

    const trigger = screen.getByRole('button', { name: '选择服务器' })
    await user.click(trigger)
    const dialog = await screen.findByRole('dialog', { name: '服务器选择器' })
    expect(document.activeElement).not.toBe(trigger)

    await user.click(within(dialog).getByRole('button', { name: '关闭' }))
    await waitFor(() => {
      expect(screen.queryByRole('dialog', { name: '服务器选择器' })).not.toBeInTheDocument()
    })

    expect(document.activeElement).toBe(trigger)
  })

  it('点遮罩关闭仍然有效，且焦点同样还回触发按钮', async () => {
    const user = userEvent.setup()
    renderWithProviders(<ServerSelector />)

    const trigger = screen.getByRole('button', { name: '选择服务器' })
    await user.click(trigger)
    const dialog = await screen.findByRole('dialog', { name: '服务器选择器' })

    // 遮罩 = 弹层面板的外壳。焦点陷阱只把「键盘导航带出去」的焦点拉回来，
    // 鼠标点出去的焦点不能被抢（否则点遮罩关闭会失灵）——关闭回调语义必须原样保留。
    await user.click(dialog.parentElement as HTMLElement)

    await waitFor(() => {
      expect(screen.queryByRole('dialog', { name: '服务器选择器' })).not.toBeInTheDocument()
    })
    expect(document.activeElement).toBe(trigger)
  })
})
