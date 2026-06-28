import { describe, it, expect } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import { useAuthStore } from '@/stores/auth'
import UsersPage from './UsersPage'

/**
 * UsersPage 强断言（FR-199 身份访问域）。受 requireAuth 保护：渲染前 loginMockUser()。
 * 三条：① 渲染种子用户行 ② 创建用户后列表联动多一行 ③ 注入 500 后页面降级不崩溃。
 */

/** 构造解码出指定 role 的 mock JWT（payload 内嵌 role，供 decodeJwt/危险操作门禁读取）。 */
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
describe('UsersPage（mock 假后端）', () => {
  it('渲染种子用户（admin / operator 两行）', async () => {
    loginMockUser()
    renderWithProviders(<UsersPage />)
    expect(await screen.findByText('admin')).toBeInTheDocument()
    expect(screen.getByText('operator')).toBeInTheDocument()
    // 角色文案佐证字段保真（admin=平台管理员，role 10）。
    expect(screen.getAllByText('平台管理员').length).toBeGreaterThan(0)
  })

  it('创建用户 → 列表联动出现新行（POST /auth/register → 重查 /users）', async () => {
    loginMockUser()
    renderWithProviders(<UsersPage />)
    await screen.findByText('admin')
    const user = userEvent.setup()
    // 打开创建对话框（标题「创建用户」所在的 modal 面板）。
    await user.click(screen.getByRole('button', { name: /创建用户/ }))
    const heading = await screen.findByRole('heading', { name: '创建用户' })
    const dialog = heading.closest('div.fixed') as HTMLElement
    // 对话框字段无 label 关联：用户名=唯一 textbox，密码=唯一 password 输入。
    await user.type(within(dialog).getByRole('textbox'), 'newbie')
    await user.type(dialog.querySelector('input[type="password"]') as HTMLInputElement, 'newbie123')
    await user.click(within(dialog).getByRole('button', { name: '创建' }))
    // 重查后表格新增 newbie 行。
    expect(await screen.findByText('newbie')).toBeInTheDocument()
  })

  it('删除用户 → 该行从列表消失（DELETE /users/:id 联动）', async () => {
    loginPlatformAdmin()
    renderWithProviders(<UsersPage />)
    await screen.findByText('operator')
    const user = userEvent.setup()
    // operator 所在行的删除按钮。
    const operatorRow = screen.getByText('operator').closest('tr')!
    await user.click(within(operatorRow).getByRole('button', { name: '删除' }))
    // DangerConfirm（scope=platform）需逐字输入用户名确认。
    const confirmInput = await screen.findByRole('textbox')
    await user.type(confirmInput, 'operator')
    const dialog = confirmInput.closest('[role="dialog"]') as HTMLElement
    await user.click(within(dialog).getByRole('button', { name: '删除' }))
    await waitFor(() => expect(screen.queryByText('operator')).not.toBeInTheDocument())
  })

  it('注入 500（GET /users）→ 页面降级显空态、不崩溃', async () => {
    loginMockUser()
    mockInject('get', '/users', { kind: 'status', status: 500 })
    renderWithProviders(<UsersPage />)
    // useUsers 失败 → data undefined → 渲染空态文案，证明未崩溃且种子不再出现。
    expect(await screen.findByText('暂无用户')).toBeInTheDocument()
    expect(screen.queryByText('admin')).not.toBeInTheDocument()
  })
})
