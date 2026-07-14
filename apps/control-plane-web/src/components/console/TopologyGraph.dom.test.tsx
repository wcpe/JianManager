import { describe, it, expect, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import TopologyGraph from './TopologyGraph'

/**
 * TopologyGraph 规模化纵切（FR-335）：断言单条聚合请求（消 per-proxy N+1）+ 视口交互控件存在。
 * 消费 devmock 的 GET /topology 聚合响应（registrations 种子里 proxy 10/20 + M:N 后端）。
 */
function collectTopologyRequests() {
  const paths: string[] = []
  const listener = ({ request }: { request: Request }) => {
    const url = new URL(request.url)
    if (url.pathname.startsWith('/api/v1/topology') || url.pathname.includes('/registrations')) {
      paths.push(url.pathname)
    }
  }
  server.events.on('request:start', listener)
  return { paths, stop: () => server.events.removeListener('request:start', listener) }
}

beforeEach(() => {
  loginMockUser()
})

describe('TopologyGraph（FR-335 拓扑规模化）', () => {
  it('拓扑仅发一条 GET /topology，无 per-proxy 注册请求', async () => {
    const requests = collectTopologyRequests()
    try {
      renderWithProviders(<TopologyGraph />)
      // 种子代理渲染出来即证聚合响应已消费。
      await screen.findByText('survival-proxy')

      const topoCalls = requests.paths.filter((p) => p.endsWith('/topology'))
      const perProxy = requests.paths.filter((p) => /\/proxies\/\d+\/registrations$/.test(p))
      expect(topoCalls).toHaveLength(1)
      expect(perProxy).toHaveLength(0)
    } finally {
      requests.stop()
    }
  })

  it('渲染视口壳与工具条控件（适应视图 / 搜索 / 状态筛选 / 禁用线开关）', async () => {
    renderWithProviders(<TopologyGraph />)
    await screen.findByText('survival-proxy')

    // 适应视图按钮。
    expect(screen.getByRole('button', { name: /适应视图/ })).toBeInTheDocument()
    // 搜索框。
    expect(screen.getByRole('textbox', { name: /搜索节点/ })).toBeInTheDocument()
    // 状态筛选 pills（复用健康分布文案）。
    expect(screen.getByRole('button', { name: /运行/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /崩溃/ })).toBeInTheDocument()
    // 图仍为可访问的 SVG。
    expect(screen.getByRole('img', { name: '群组服拓扑' })).toBeInTheDocument()
  })

  it('名称搜索 + 仅显示匹配：非命中节点从渲染中隐去', async () => {
    const user = userEvent.setup()
    renderWithProviders(<TopologyGraph />)
    await screen.findByText('survival-proxy')
    expect(screen.getByText('creative-proxy')).toBeInTheDocument()

    await user.type(screen.getByRole('textbox', { name: /搜索节点/ }), 'survival')
    await user.click(screen.getByLabelText(/仅显示匹配/))

    // 仅显示匹配后 creative-proxy（不含 survival）应被隐去，survival-proxy 仍在。
    await waitFor(() => expect(screen.queryByText('creative-proxy')).not.toBeInTheDocument())
    expect(screen.getByText('survival-proxy')).toBeInTheDocument()
  })

  it('适应视图按钮点击后 viewBox 保持有效（复位可用）', async () => {
    const user = userEvent.setup()
    renderWithProviders(<TopologyGraph />)
    await screen.findByText('survival-proxy')
    const svg = screen.getByRole('img', { name: '群组服拓扑' })
    const before = svg.getAttribute('viewBox')
    expect(before).toMatch(/^0 0 \d+/)

    await user.click(screen.getByRole('button', { name: /适应视图/ }))
    const after = svg.getAttribute('viewBox')
    // 复位后仍是以内容原点起算的有效 viewBox（数值合法，无 NaN）。
    expect(after).toMatch(/^0 0 \d+(\.\d+)? \d+(\.\d+)?$/)
  })

  it('状态筛选 pill 可切换（aria-pressed 反映选中态）', async () => {
    const user = userEvent.setup()
    renderWithProviders(<TopologyGraph />)
    await screen.findByText('survival-proxy')

    const runningPill = screen.getByRole('button', { name: /运行/ })
    expect(runningPill).toHaveAttribute('aria-pressed', 'false')
    await user.click(runningPill)
    expect(runningPill).toHaveAttribute('aria-pressed', 'true')
  })
})
