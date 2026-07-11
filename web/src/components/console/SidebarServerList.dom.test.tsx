import { describe, it, expect, beforeEach } from 'vitest'
import { screen, within, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { useAuthStore } from '@/stores/auth'
import { useConsoleStore } from '@/stores/console'
import SidebarServerList from './SidebarServerList'
import ServerSelector from './ServerSelector'
import ConsoleSidebar from './ConsoleSidebar'

// 快照与假后端种子实例对齐（id/uuid/nodeId 一致）；status 故意存过期值，
// 逼常驻列走低频合并查询取真状态，同时给测试一个「查询已结算」的确定性信号。
const survival = { id: 1, uuid: 'i-survival', nodeId: 1, name: 'survival-1', status: 'STOPPED' } // 假后端实际 RUNNING
const lobby = { id: 2, uuid: 'i-lobby', nodeId: 1, name: 'lobby-proxy', status: 'RUNNING' } // 假后端实际 STOPPED
const creative = { id: 3, uuid: 'i-creative', nodeId: 2, name: 'creative-1', status: 'CRASHED' }

type Stored = typeof survival

function seedSelection(favorites: Stored[], recent: Stored[]) {
  localStorage.setItem('server-selector.favorites', JSON.stringify(favorites))
  localStorage.setItem('server-selector.recent', JSON.stringify(recent))
}

/**
 * 侧栏常驻服务器列（FR-293）：收藏置顶 + 最近打开（LRU ≤8），与服务器选择器
 * 弹窗共用 localStorage 存储并实时互通；点击行进入该服控制台。
 */
describe('SidebarServerList 常驻服务器列（FR-293）', () => {
  beforeEach(() => {
    loginMockUser()
    useConsoleStore.setState({ selectedNodeId: null })
  })

  it('收藏区置顶、最近区按 LRU 排序，已收藏条目不重复出现在最近区', async () => {
    // survival 同时在收藏与最近：最近区必须把它去重掉。
    seedSelection([survival], [creative, survival, lobby])
    renderWithProviders(<SidebarServerList />)

    const list = screen.getByTestId('sidebar-server-list')
    const favSection = within(list).getByTestId('sidebar-server-favorites')
    const recentSection = within(list).getByTestId('sidebar-server-recent')

    expect(within(favSection).getByText('survival-1')).toBeInTheDocument()
    expect(within(recentSection).queryByText('survival-1')).toBeNull()

    // 全列顺序：收藏区在前，最近区按 LRU（creative 最新在前）。
    const rows = within(list).getAllByTestId('sidebar-server-row')
    const names = rows.map((row) => within(row).getByText(/survival-1|creative-1|lobby-proxy/).textContent)
    expect(names).toEqual(['survival-1', 'creative-1', 'lobby-proxy'])

    // 行 title 提示含节点名（节点查询解析后）。
    await waitFor(() => {
      expect(within(favSection).getByText('survival-1').closest('button')).toHaveAttribute('title', 'survival-1 · alpha')
    })
    // 状态合并查询结算：survival 快照 STOPPED → 服务端 RUNNING。
    await waitFor(() => {
      expect(within(favSection).getByLabelText('RUNNING')).toBeInTheDocument()
    })
  })

  it('行内星标收藏与选择器弹窗互通，双向即时同步', async () => {
    const user = userEvent.setup()
    seedSelection([], [creative])
    renderWithProviders(
      <>
        <ServerSelector />
        <SidebarServerList />
      </>,
    )

    const list = screen.getByTestId('sidebar-server-list')

    // 常驻列星标收藏 → 收藏区出现、最近区隐藏（去重）。
    await user.click(within(list).getByRole('button', { name: '收藏 creative-1' }))
    expect(within(within(list).getByTestId('sidebar-server-favorites')).getByText('creative-1')).toBeInTheDocument()
    expect(within(list).queryByTestId('sidebar-server-recent')).toBeNull()

    // 打开选择器弹窗：收藏 QuickList 已同步出现 creative-1。
    await user.click(screen.getByRole('button', { name: '选择服务器' }))
    const dialog = await screen.findByRole('dialog', { name: '服务器选择器' })
    expect(within(dialog).getByText('收藏')).toBeInTheDocument()
    expect(within(dialog).getAllByText('creative-1').length).toBeGreaterThan(0)

    // 等选择器的搜索结果落地，避免用例结束时还有在途请求。
    await screen.findByTestId('server-selector-virtual')

    // 弹窗内取消收藏 → 常驻列收藏区消失、creative 回到最近区。
    await user.click(within(dialog).getAllByRole('button', { name: '取消收藏 creative-1' })[0]!)
    await waitFor(() => {
      expect(within(list).queryByTestId('sidebar-server-favorites')).toBeNull()
      expect(within(within(list).getByTestId('sidebar-server-recent')).getByText('creative-1')).toBeInTheDocument()
    })
  })

  it('点击行导航到该服控制台并写入最近', async () => {
    const user = userEvent.setup()
    seedSelection([survival], [])
    renderWithProviders(<SidebarServerList />)

    // 先等节点/状态查询结算（title 带节点名 + 状态点刷成服务端值），再点击。
    const list = screen.getByTestId('sidebar-server-list')
    await waitFor(() => {
      expect(within(list).getByText('survival-1').closest('button')).toHaveAttribute('title', 'survival-1 · alpha')
      expect(within(list).getByLabelText('RUNNING')).toBeInTheDocument()
    })

    await user.click(screen.getByText('survival-1'))

    expect(window.location.pathname).toBe('/instances/1')
    const recent = JSON.parse(localStorage.getItem('server-selector.recent') ?? '[]') as Stored[]
    expect(recent[0]?.id).toBe(1)
  })

  it('状态点复用实例查询数据：本地快照过期时以服务端状态为准', async () => {
    // 快照存的是 STOPPED，假后端里 survival-1 实际是 RUNNING。
    seedSelection([survival], [])
    renderWithProviders(<SidebarServerList />)

    const list = screen.getByTestId('sidebar-server-list')
    expect(within(list).getByLabelText('STOPPED')).toBeInTheDocument()
    await waitFor(() => {
      expect(within(list).getByLabelText('RUNNING')).toBeInTheDocument()
    })
    await waitFor(() => {
      expect(within(list).getByText('survival-1').closest('button')).toHaveAttribute('title', 'survival-1 · alpha')
    })
  })

  it('收藏与最近双空时显示引导文案', () => {
    renderWithProviders(<SidebarServerList />)

    const list = screen.getByTestId('sidebar-server-list')
    expect(list).toHaveTextContent('打开「选择服务器」')
    expect(screen.queryByTestId('sidebar-server-row')).toBeNull()
  })

  it('侧栏折叠图标轨态不渲染常驻列表', async () => {
    seedSelection([survival], [creative])
    useAuthStore.setState({ role: 1 })
    useConsoleStore.setState({ sidebarCollapsed: false, collapsedGroups: {} })
    const { container } = renderWithProviders(<ConsoleSidebar />)

    const expanded = container.querySelector('[data-mode="expanded"]') as HTMLElement
    const compact = container.querySelector('[data-mode="collapsed"]') as HTMLElement
    expect(within(expanded).getByTestId('sidebar-server-list')).toBeInTheDocument()
    expect(within(compact).queryByTestId('sidebar-server-list')).toBeNull()

    // 等状态/节点查询结算，避免用例结束时仍有在途请求。
    await waitFor(() => {
      expect(within(expanded).getByLabelText('RUNNING')).toBeInTheDocument()
      expect(within(expanded).getByText('survival-1').closest('button')).toHaveAttribute('title', 'survival-1 · alpha')
    })
  })
})
