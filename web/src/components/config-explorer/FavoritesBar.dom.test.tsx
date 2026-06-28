import { describe, it, expect, beforeEach, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { mockInject } from '@/mocks/inject'
import { loginMockUser } from '@/test/auth'
import FavoritesBar from './FavoritesBar'

/**
 * FavoritesBar 强断言（FR-205 配置浏览器「已发现配置」面板）：验 `GET /configs/discover`
 * 把种子配置文件渲染出来 + 点选打开回调 + 收藏切换回调 + discover 500 错误注入显错误态。
 *
 * 聚焦此子组件而非整棵 ConfigExplorer：ConfigExplorer 内嵌 ResourceExplorer 会拉文件域
 * 端点，本面板只命中 configs 域 discover 端点，断言更稳、不跨域噪声。
 */

function renderBar(favorites: string[] = []) {
  const onOpen = vi.fn()
  const onToggleFavorite = vi.fn()
  const utils = renderWithProviders(
    <FavoritesBar
      instanceId={1}
      favorites={favorites}
      onOpen={onOpen}
      onToggleFavorite={onToggleFavorite}
    />,
  )
  return { ...utils, onOpen, onToggleFavorite }
}

describe('FavoritesBar 已发现配置面板（mock 假后端）', () => {
  beforeEach(() => {
    loginMockUser() // /configs/discover 经 requireAuth，需有效 session
  })

  it('渲染种子：discover 列出工作目录配置文件（server.properties + paper-global.yml）', async () => {
    renderBar()
    // 两份种子配置文件按 basename 呈现（discover 返回 2 条，标题计数也是 2）。
    expect(await screen.findByText('server.properties')).toBeInTheDocument()
    expect(screen.getByText('paper-global.yml')).toBeInTheDocument()
    // 已发现配置 section header 带发现计数「(2)」（收藏 header 为「(0)」，故 (2) 唯一）。
    expect(screen.getByText(/\(2\)/)).toBeInTheDocument()
  })

  it('交互：点选已发现文件 → onOpen 收到完整相对路径（非 basename）', async () => {
    const user = userEvent.setup()
    const { onOpen } = renderBar()
    // paper-global.yml 在 config/ 子目录，呈现 basename，但打开须回传完整路径。
    await screen.findByText('paper-global.yml')
    await user.click(screen.getByText('paper-global.yml'))
    expect(onOpen).toHaveBeenCalledWith('config/paper-global.yml')
  })

  it('交互：filter 过滤已发现列表（只留命中项）', async () => {
    const user = userEvent.setup()
    renderBar()
    await screen.findByText('server.properties')
    const filter = screen.getByPlaceholderText('筛选文件…')
    await user.type(filter, 'paper')
    // 过滤后 server.properties 隐去，paper-global.yml 仍在。
    await waitFor(() => expect(screen.queryByText('server.properties')).not.toBeInTheDocument())
    expect(screen.getByText('paper-global.yml')).toBeInTheDocument()
  })

  it('交互：点星标 → onToggleFavorite 收到该文件路径', async () => {
    const user = userEvent.setup()
    const { onToggleFavorite } = renderBar()
    await screen.findByText('server.properties')
    // server.properties 行内的星标按钮（收藏）。
    const row = screen.getByTitle('server.properties')
    await user.click(within(row).getByRole('button', { name: '收藏' }))
    expect(onToggleFavorite).toHaveBeenCalledWith('server.properties')
  })

  it('注入 500（GET configs/discover）→ 显示「发现配置失败」错误态而非崩溃', async () => {
    mockInject('get', '/instances/:id/configs/discover', { kind: 'status', status: 500 })
    renderBar()
    expect(await screen.findByText('发现配置失败')).toBeInTheDocument()
  })
})
