import { describe, it, expect } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import { useAuthStore } from '@/stores/auth'
import GroupsPage from './GroupsPage'

/**
 * GroupsPage 强断言（FR-199 身份访问域）。受 requireAuth 保护：渲染前登录。
 * 三条：① 渲染种子用户组 ② 创建用户组后列表联动多一项 ③ 注入 500 后页面降级不崩溃。
 */
function adminJwt(role = 10): string {
  const payload = btoa(JSON.stringify({ userId: 1, username: 'admin', role, exp: Math.floor(Date.now() / 1000) + 900 }))
  return `mock.${payload}.sig`
}

/** 登录为平台管理员：写 sessions（过 requireAuth）+ 同步 auth store role（过危险操作门禁）。 */
function loginPlatformAdmin(): void {
  const token = adminJwt(10)
  loginMockUser(token)
  useAuthStore.getState().login(token, 'test-refresh-token')
}

describe('GroupsPage（mock 假后端）', () => {
  it('渲染种子用户组（默认组 / 运营组）', async () => {
    loginMockUser()
    renderWithProviders(<GroupsPage />)
    expect(await screen.findByText('默认组')).toBeInTheDocument()
    expect(screen.getByText('运营组')).toBeInTheDocument()
    // 成员名药丸佐证 members 字段保真。
    expect(screen.getByText('admin')).toBeInTheDocument()
    expect(screen.getByText('operator')).toBeInTheDocument()
  })

  it('创建用户组 → 列表联动出现新项（POST /groups → 重查）', async () => {
    loginMockUser()
    renderWithProviders(<GroupsPage />)
    await screen.findByText('默认组')
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /创建用户组/ }))
    const dialog = await screen.findByRole('dialog', { name: '创建用户组' })
    // FR-026：创建用户组弹窗必须走 shadcn Dialog，并用可访问 label 关联 Input/Textarea。
    await user.type(within(dialog).getByLabelText(/名称/), '测试组')
    expect(within(dialog).getByLabelText('描述')).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: '创建' }))
    expect(await screen.findByText('测试组')).toBeInTheDocument()
  })

  it('创建、编辑与成员管理用户组对话框走共享 Dialog 并支持 Esc 关闭', async () => {
    loginMockUser()
    renderWithProviders(<GroupsPage />)
    await screen.findByText('默认组')
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: /创建用户组/ }))
    const createHeading = await screen.findByRole('heading', { name: '创建用户组' })
    expect(createHeading.closest('[role="dialog"]')).toBeInTheDocument()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('heading', { name: '创建用户组' })).not.toBeInTheDocument())

    const defaultPanel = screen.getByText('默认组').closest('[data-slot="panel"]') as HTMLElement
    await user.click(within(defaultPanel).getByRole('button', { name: '编辑' }))
    const editHeading = await screen.findByRole('heading', { name: '编辑用户组「默认组」' })
    expect(editHeading.closest('[role="dialog"]')).toBeInTheDocument()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('heading', { name: '编辑用户组「默认组」' })).not.toBeInTheDocument())

    await user.click(within(defaultPanel).getByRole('button', { name: '成员' }))
    const membersHeading = await screen.findByRole('heading', { name: '管理「默认组」成员' })
    expect(membersHeading.closest('[role="dialog"]')).toBeInTheDocument()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('heading', { name: '管理「默认组」成员' })).not.toBeInTheDocument())
  })

  it('删除用户组 → 该项从列表消失（DELETE /groups/:id 联动）', async () => {
    loginPlatformAdmin()
    renderWithProviders(<GroupsPage />)
    await screen.findByText('运营组')
    const user = userEvent.setup()
    // 运营组所在 Panel 内的删除按钮（Panel 带 data-slot="panel"）。
    const opsPanel = screen.getByText('运营组').closest('[data-slot="panel"]') as HTMLElement
    await user.click(within(opsPanel).getByRole('button', { name: '删除' }))
    // DangerConfirm（scope=platform）需逐字输入组名确认。
    const confirmInput = await screen.findByRole('textbox')
    await user.type(confirmInput, '运营组')
    const dialog = confirmInput.closest('[role="dialog"]') as HTMLElement
    await user.click(within(dialog).getByRole('button', { name: '删除' }))
    await waitFor(() => expect(screen.queryByText('运营组')).not.toBeInTheDocument())
  })

  it('注入 500（GET /groups）→ 页面降级显空态、不崩溃', async () => {
    loginMockUser()
    mockInject('get', '/groups', { kind: 'status', status: 500 })
    renderWithProviders(<GroupsPage />)
    expect(await screen.findByText('暂无用户组')).toBeInTheDocument()
    expect(screen.queryByText('默认组')).not.toBeInTheDocument()
  })
})
