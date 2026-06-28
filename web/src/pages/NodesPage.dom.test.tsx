import { describe, it, expect } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import { server } from '@/mocks/server'
import { API } from '@/mocks/api'
import NodesPage from './NodesPage'

/**
 * NodesPage 强断言（FR-200）：①渲染 seed 节点 ②进入维护写操作→列表联动出维护标记 ③注入 500→错误态。
 * NodesPage 跨域调 GET /instances（FR-201 域），本域 worktree 未注册——用 server.use 本地桩返回空，
 * 满足 onUnhandledRequest:'error' 覆盖闸，且不污染他域 handler 文件。
 */
function stubInstances() {
  server.use(http.get(API('/instances'), () => HttpResponse.json([])))
}

describe('NodesPage（mock 假后端）', () => {
  it('渲染 seed 节点列表（alpha / beta）', async () => {
    loginMockUser()
    stubInstances()
    renderWithProviders(<NodesPage />)

    expect(await screen.findByText('alpha')).toBeInTheDocument()
    expect(screen.getByText('beta')).toBeInTheDocument()
    expect(screen.getByText('10.0.0.11')).toBeInTheDocument()
  })

  it('对节点进入维护后，列表联动出现「维护」标记', async () => {
    loginMockUser()
    stubInstances()
    const user = userEvent.setup()
    renderWithProviders(<NodesPage />)

    // 选中 alpha → 右栏详情出现操作菜单（kebab，aria-label=操作）。
    await user.click(await screen.findByText('alpha'))
    const actionsBtn = await screen.findByRole('button', { name: '操作' })
    await user.click(actionsBtn)
    await user.click(await screen.findByRole('menuitem', { name: '进入维护' }))

    // setNodeMaintenance 成功后失效 ['nodes'] → 重新拉取，handler 现回 maintenance:true，列表行渲染「维护中」徽标。
    await waitFor(() => expect(screen.getAllByText('维护中').length).toBeGreaterThan(0))
  })

  it('注入 500：节点列表请求失败，列表区不崩溃（无节点行）', async () => {
    loginMockUser()
    stubInstances()
    mockInject('get', '/nodes', { kind: 'status', status: 500 })
    renderWithProviders(<NodesPage />)

    // 错误态：列表空（不出现 seed 节点名），页面整体仍渲染（搜索框可见）。
    await screen.findByRole('textbox')
    await waitFor(() => {
      expect(screen.queryByText('alpha')).not.toBeInTheDocument()
    })
    expect(within(document.body).queryByText('beta')).not.toBeInTheDocument()
  })
})
