import { describe, it, expect, beforeEach } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { API } from '@jianmanager/devmock/api'
import { server } from '@jianmanager/devmock/server'
import { useAuthStore } from '@/stores/auth'
import { usePermissionsStore } from '@/stores/permissions'
import { ALL_PERMISSION_NODE_IDS } from '@/lib/shared/roles'
import PermissionsPage from './PermissionsPage'

function adminJwt(role = 10): string {
  const payload = btoa(JSON.stringify({ userId: 1, username: 'admin', role, exp: Math.floor(Date.now() / 1000) + 900 }))
  return `mock.${payload}.sig`
}

function loginPlatformAdmin(): void {
  const token = adminJwt(10)
  loginMockUser(token)
  useAuthStore.getState().login(token, 'test-refresh-token')
  usePermissionsStore.setState({
    nodes: new Set(ALL_PERMISSION_NODE_IDS),
    roleKey: 'platform_admin',
    isPlatformAdmin: true,
    loaded: true,
  })
}

describe('PermissionsPage', () => {
  beforeEach(() => {
    loginPlatformAdmin()
  })

  it('渲染角色模板列表与三栏骨架', async () => {
    renderWithProviders(<PermissionsPage />)
    expect(await screen.findByTestId('permissions-role-group_admin')).toBeInTheDocument()
    expect(screen.getByTestId('permissions-role-group_viewer')).toBeInTheDocument()
    expect(screen.getByTestId('permissions-user-1')).toBeInTheDocument()
    expect(screen.getByTestId('permissions-save')).toBeDisabled()
    expect(screen.getByTestId('permissions-preview-count')).toBeInTheDocument()
  })

  it('选中角色后树可点，节点计数变化', async () => {
    const user = userEvent.setup()
    renderWithProviders(<PermissionsPage />)
    await user.click(await screen.findByTestId('permissions-role-group_viewer'))

    const node = await screen.findByTestId('permissions-node-instance.read')
    expect(node).toHaveAttribute('data-on', 'true')

    const preview = screen.getByTestId('permissions-preview-count')
    const before = Number(preview.textContent)
    await user.click(node)
    await waitFor(() => {
      expect(Number(screen.getByTestId('permissions-preview-count').textContent)).toBeLessThan(before)
    })
    expect(screen.getByTestId('permissions-save')).toBeEnabled()
  })

  it('无 rbac.read 且非平台管理员时展示禁止态', () => {
    usePermissionsStore.setState({
      nodes: new Set(['instance.read']),
      roleKey: 'group_viewer',
      isPlatformAdmin: false,
      loaded: true,
    })
    useAuthStore.setState({ role: 3 })
    renderWithProviders(<PermissionsPage />)
    expect(screen.getByText(/权限配置读取|permission read/i)).toBeInTheDocument()
  })

  // 左栏原先用「useUsers() 拉全量 + users.map() 全量渲染」（规则点名的禁用形态）。
  // 现在候选走服务端搜索（limit 信封 + 键入 q，300ms 防抖），列表走变高虚拟窗口。
  it('用户候选走服务端搜索：请求带 limit 信封，键入只发一次带 q 的请求', async () => {
    const userUrls: string[] = []
    server.use(
      http.get(API('/users'), ({ request }) => {
        userUrls.push(request.url)
        return HttpResponse.json({
          items: [{ id: 1, uuid: 'u1', username: 'admin', role: 10, status: 1, createdAt: '2026-01-01T00:00:00Z' }],
          total: 1,
          limit: 100,
          offset: 0,
        })
      }),
    )
    renderWithProviders(<PermissionsPage />)

    await waitFor(() => expect(userUrls.length).toBeGreaterThan(0))
    expect(userUrls[0]).toContain('limit=100')
    expect(userUrls[0]).not.toContain('q=')

    const user = userEvent.setup()
    await user.type(screen.getByLabelText('搜索'), 'adm')

    await waitFor(() => expect(userUrls.some((u) => u.includes('q=adm'))).toBe(true))
    // 300ms 防抖：中间的 a / ad 不许各发一次。
    expect(userUrls.filter((u) => u.includes('q='))).toHaveLength(1)
  })

  it('深链 ?user=<不存在的 id> → 中栏按未选择呈现（不是空权限树）', async () => {
    renderWithProviders(<PermissionsPage />, { route: '/permissions?user=999999' })

    // 候选窗口已装下全部用户（命中 1 条）且该 id 不在其中 → 判定为「确实不存在」。
    expect(await screen.findByText('未选择对象')).toBeInTheDocument()
    // 中栏提示（工具栏与主体各一处，故用 getAllByText）。
    expect(screen.getAllByText('左侧选择角色模板或用户后在此编辑权限卡片。').length).toBeGreaterThan(0)
  })
})
