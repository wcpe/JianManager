import { describe, it, expect } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Routes, Route } from 'react-router'
import { renderWithProviders } from '@/test/render'
import { mockInject } from '@/mocks/inject'
import SetupPage from './SetupPage'

/**
 * SetupPage 强断言（FR-199 身份访问域）。公开页：无需 loginMockUser。
 * 地基默认 /setup/status 返回 setupRequired:false → SetupPage 会 <Navigate to="/login">，
 * 故验「需引导」分支须先注入 setupRequired:true。
 */

/** 把 SetupPage 挂在带 /login 落点的路由里，便于断言「未引导→跳登录」。 */
function renderSetup() {
  return renderWithProviders(
    <Routes>
      <Route path="/" element={<SetupPage />} />
      <Route path="/login" element={<div>登录占位</div>} />
    </Routes>,
    { route: '/' },
  )
}

describe('SetupPage（mock 假后端）', () => {
  it('注入 setupRequired:true → 渲染引导表单（种子/默认态强断言）', async () => {
    mockInject('get', '/setup/status', { kind: 'status', status: 200, body: { setupRequired: true } })
    renderSetup()
    // 标题 + 副标题 + 三个字段标签可见，证明引导表单渲染。
    expect(await screen.findByText('首次使用，请创建管理员账号')).toBeInTheDocument()
    expect(screen.getByLabelText('用户名')).toHaveValue('admin')
    expect(screen.getByLabelText('密码')).toBeInTheDocument()
    expect(screen.getByLabelText('确认密码')).toBeInTheDocument()
  })

  it('未引导（默认 setupRequired:false）→ 跳转登录页', async () => {
    renderSetup()
    expect(await screen.findByText('登录占位')).toBeInTheDocument()
  })

  it('提交创建管理员 → 成功登录（联动：POST /setup 写入 token）', async () => {
    mockInject('get', '/setup/status', { kind: 'status', status: 200, body: { setupRequired: true } })
    renderSetup()
    await screen.findByText('首次使用，请创建管理员账号')
    const user = userEvent.setup()
    await user.type(screen.getByLabelText('密码'), 'admin12345')
    await user.type(screen.getByLabelText('确认密码'), 'admin12345')
    await user.click(screen.getByRole('button', { name: '开始使用' }))
    // useSetup.onSuccess 调 loginStore(token) → 写 localStorage.accessToken（mock 返回 setup-token-*）。
    await waitFor(() =>
      expect(localStorage.getItem('accessToken')).toMatch(/^setup-token-/),
    )
  })

  it('注入 500 → 渲染期错误（页面不崩溃，仍可见引导骨架）', async () => {
    // status 查询 500 时 isLoading 结束、status 为 undefined → 既不跳转也不渲染表单，显示加载占位；
    // 这里改注入提交端点 500，验提交后错误态。
    mockInject('get', '/setup/status', { kind: 'status', status: 200, body: { setupRequired: true } })
    mockInject('post', '/setup', { kind: 'status', status: 500, body: { message: '服务器内部错误' } })
    renderSetup()
    await screen.findByText('首次使用，请创建管理员账号')
    const user = userEvent.setup()
    await user.type(screen.getByLabelText('密码'), 'admin12345')
    await user.type(screen.getByLabelText('确认密码'), 'admin12345')
    await user.click(screen.getByRole('button', { name: '开始使用' }))
    expect(await screen.findByText(/服务器内部错误|创建失败/)).toBeInTheDocument()
  })
})
