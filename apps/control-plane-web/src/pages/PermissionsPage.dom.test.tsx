import { describe, it, expect, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { useAuthStore } from '@/stores/auth'
import { usePermissionsStore } from '@/stores/permissions'
import { ALL_PERMISSION_NODE_IDS } from '@/lib/roles'
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

describe('PermissionsPage（FR-432）', () => {
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
})
