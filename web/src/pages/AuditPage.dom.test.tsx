import { describe, it, expect, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import { server } from '@/mocks/server'
import { API } from '@/mocks/api'
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
    // 分页 envelope 计数佐证行数与总数（种子 3 条）。
    expect(screen.getByText('已加载 3 / 共 3 条')).toBeInTheDocument()
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

  it('分页 envelope 显示真实命中总数', async () => {
    loginMockUser()
    server.use(
      http.get(API('/audit'), () =>
        HttpResponse.json({
          items: [
            {
              id: 1,
              uuid: 'a-1',
              userId: 1,
              action: 'user.login',
              targetType: 'user',
              targetId: '1',
              detail: '',
              ip: '127.0.0.1',
              createdAt: '2026-06-28T10:00:00Z',
              user: { id: 1, username: 'admin' },
            },
          ],
          total: 250,
          page: 1,
          pageSize: 100,
        }),
      ),
    )

    renderWithProviders(<AuditPage />)

    expect(await screen.findByText('user.login')).toBeInTheDocument()
    expect(screen.getByText('已加载 1 / 共 250 条')).toBeInTheDocument()
  })

  it('导出使用相同筛选条件，但不携带分页参数', async () => {
    loginMockUser()
    const user = userEvent.setup()
    let exportedUrl: URL | null = null
    const createObjectURL = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:audit')
    const revokeObjectURL = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => undefined)
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined)
    server.use(
      http.get(API('/audit/export'), ({ request }) => {
        exportedUrl = new URL(request.url)
        return new HttpResponse('{"id":1}\n', { headers: { 'Content-Type': 'application/x-ndjson' } })
      }),
    )

    renderWithProviders(<AuditPage />)
    await screen.findByText('user.login')
    await user.type(screen.getByPlaceholderText(/操作/), 'instance')
    await user.click(screen.getByRole('button', { name: '导出' }))

    await waitFor(() => expect(exportedUrl).not.toBeNull())
    expect(exportedUrl?.searchParams.get('action')).toBe('instance')
    expect(exportedUrl?.searchParams.has('page')).toBe(false)
    expect(exportedUrl?.searchParams.has('pageSize')).toBe(false)
    expect(exportedUrl?.searchParams.has('limit')).toBe(false)

    createObjectURL.mockRestore()
    revokeObjectURL.mockRestore()
    click.mockRestore()
  })

  it('注入 500（GET /audit）→ 页面显错误态', async () => {
    loginMockUser()
    mockInject('get', '/audit', { kind: 'status', status: 500 })
    renderWithProviders(<AuditPage />)
    // useAuditLogs isError → AuditPage 渲染 audit.loadError。
    expect(await screen.findByText('加载审计日志失败')).toBeInTheDocument()
  })
})
