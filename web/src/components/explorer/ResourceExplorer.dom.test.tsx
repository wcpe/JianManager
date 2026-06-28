import { describe, it, expect } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { mockInject } from '@/mocks/inject'
import { loginMockUser } from '@/test/auth'
import ResourceExplorer from './ResourceExplorer'

/**
 * 资源管理器（文件管理器主视图）强断言（FR-204 文件归档域）。
 * 验 mock 假后端 files 集合渲染 + 目录下钻导航联动 + 错误注入。
 * 文件 API 受 requireAuth 保护，故渲染前 loginMockUser() 注入会话 token。
 * 用 instanceId=1（files 种子所在实例）。
 */

describe('ResourceExplorer（mock 假后端）', () => {
  it('渲染工作目录种子：根级文件与目录', async () => {
    loginMockUser()
    renderWithProviders(<ResourceExplorer instanceId={1} />)

    // 根目录列出种子：文件 server.properties（仅右列表有）+ 目录 plugins/world（左树与右列表各一份）。
    expect(await screen.findByText('server.properties')).toBeInTheDocument()
    expect(screen.getAllByText('world').length).toBeGreaterThan(0)
    expect(screen.getAllByText('plugins').length).toBeGreaterThan(0)
  })

  it('下钻目录：双击 plugins 反映其子项', async () => {
    loginMockUser()
    const user = userEvent.setup()
    renderWithProviders(<ResourceExplorer instanceId={1} />)

    // 双击右侧列表里的 plugins 目录行 → 导航进入 → 列出 plugins 下子项。
    const pluginsRows = await screen.findAllByText('plugins')
    // 取列表里的那个（可点击的文件行 span）。最后一个通常为列表项；逐个尝试双击直到出现子项。
    for (const row of pluginsRows) {
      await user.dblClick(row)
    }
    expect(await screen.findByText('config.yml')).toBeInTheDocument()
    expect(screen.getByText('Essentials.jar')).toBeInTheDocument()
  })

  it('注入 500：目录加载失败显示错误态（不崩溃）', async () => {
    loginMockUser()
    mockInject('get', '/instances/:id/files', { kind: 'status', status: 500 })
    renderWithProviders(<ResourceExplorer instanceId={1} />)

    // FileList 把加载错误渲染为 destructive 文案；注入默认 body.message = "注入的模拟错误"。
    await waitFor(() => expect(screen.getByText('注入的模拟错误')).toBeInTheDocument())
    // 列表未渲染出种子文件（确认是错误态而非正常态）。
    expect(screen.queryByText('server.properties')).not.toBeInTheDocument()
  })
})
