import { describe, it, expect } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import AuditPage from './AuditPage'

/**
 * AuditPage 强断言（FR-199 身份访问域）。受 requireAuth 保护：渲染前 loginMockUser()。
 * AuditPage 同时调 GET /users（筛选下拉）+ GET /audit（列表），两者均由本域 mock。
 * 三条：① 渲染种子审计行 ② 筛选 action 后列表联动收窄 ③ 注入 500 后页面显错误态。
 */
describe('AuditPage（mock 假后端）', () => {
  it('渲染种子审计日志（三条 action 均在）', async () => {
    loginMockUser()
    renderWithProviders(<AuditPage />)
    expect(await screen.findByText('user.login')).toBeInTheDocument()
    expect(screen.getByText('instance.start')).toBeInTheDocument()
    expect(screen.getByText('group.create')).toBeInTheDocument()
    // 已加载计数佐证行数（种子 3 条）。
    expect(screen.getByText('已加载 3 条')).toBeInTheDocument()
  })

  it('按 action 筛选 → 列表联动收窄（仅命中行保留）', async () => {
    loginMockUser()
    renderWithProviders(<AuditPage />)
    await screen.findByText('user.login')
    const user = userEvent.setup()
    await user.type(screen.getByPlaceholderText(/操作/), 'instance')
    // 重查后只剩 instance.start，其余消失。
    await waitFor(() => expect(screen.queryByText('user.login')).not.toBeInTheDocument())
    expect(screen.getByText('instance.start')).toBeInTheDocument()
    expect(screen.queryByText('group.create')).not.toBeInTheDocument()
  })

  it('注入 500（GET /audit）→ 页面显错误态', async () => {
    loginMockUser()
    mockInject('get', '/audit', { kind: 'status', status: 500 })
    renderWithProviders(<AuditPage />)
    // useAuditLogs isError → AuditPage 渲染 audit.loadError。
    expect(await screen.findByText('加载审计日志失败')).toBeInTheDocument()
  })
})
