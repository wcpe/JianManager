import { describe, it, expect } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { mockInject } from '@/mocks/inject'
import LoginPage from './LoginPage'

/**
 * LoginPage 强断言纵切（FR-199 样例，地基随附）：验 render harness + 假后端 auth 联动 + 错误注入。
 * 注：错误凭据(401) 触发整页刷新的回归断言归「登录失败刷新」fix（sdd-fix-bug），不在地基样例内。
 */
async function fillAndSubmit(username: string, password: string) {
  const user = userEvent.setup()
  await user.type(screen.getByLabelText('用户名'), username)
  await user.type(screen.getByLabelText('密码'), password)
  await user.click(screen.getByRole('button', { name: '登录' }))
}

describe('LoginPage（mock 假后端）', () => {
  it('正确凭据 → 跳转控制台（/）', async () => {
    renderWithProviders(<LoginPage />, { route: '/login' })
    await screen.findByLabelText('用户名')
    await fillAndSubmit('admin', 'admin123')
    await waitFor(() => expect(window.location.pathname).toBe('/'))
  })

  it('注入 500 → 显示错误提示，停留登录页', async () => {
    mockInject('post', '/auth/login', { kind: 'status', status: 500, body: { message: '服务器内部错误' } })
    renderWithProviders(<LoginPage />, { route: '/login' })
    await screen.findByLabelText('用户名')
    await fillAndSubmit('admin', 'admin123')
    expect(await screen.findByText(/服务器内部错误|登录失败/)).toBeInTheDocument()
    expect(window.location.pathname).toBe('/login')
  })
})
