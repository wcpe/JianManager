import { describe, it, expect } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@/mocks/server'
import LicensesPage from './LicensesPage'

/**
 * LicensesPage 强断言（FR-210）：渲染 seed 依赖清单 → 搜索过滤联动 → 注入 500 显空/错误态。
 * 许可清单走静态资源 /licenses.json（裸 http.get mock），非 /api，故错误注入用 server.use 覆盖。
 */
describe('LicensesPage（mock 假后端）', () => {
  it('渲染出 seed 依赖（运行时 react / 开发 vitest）', async () => {
    loginMockUser()
    renderWithProviders(<LicensesPage />)
    expect(await screen.findByText('react')).toBeInTheDocument()
    expect(screen.getByText('vitest')).toBeInTheDocument()
    expect(screen.getByText('github.com/gin-gonic/gin')).toBeInTheDocument()
  })

  it('搜索过滤 → 列表联动收敛', async () => {
    loginMockUser()
    const user = userEvent.setup()
    renderWithProviders(<LicensesPage />)
    await screen.findByText('react')

    await user.type(screen.getByPlaceholderText('按包名过滤…'), 'gin')
    await waitFor(() => expect(screen.getByText('github.com/gin-gonic/gin')).toBeInTheDocument())
    expect(screen.queryByText('react')).not.toBeInTheDocument()
    expect(screen.queryByText('vitest')).not.toBeInTheDocument()
  })

  it('展开行 → 显示许可证全文（联动）', async () => {
    loginMockUser()
    const user = userEvent.setup()
    renderWithProviders(<LicensesPage />)
    const reactCell = await screen.findByText('react')
    await user.click(reactCell.closest('tr')!)
    expect(await screen.findByText('MIT License — react')).toBeInTheDocument()
  })

  it('注入 500 → 显示空/错误态（不崩溃）', async () => {
    loginMockUser()
    server.use(http.get('*/licenses.json', () => HttpResponse.json({ message: 'boom' }, { status: 500 })))
    renderWithProviders(<LicensesPage />)
    expect(await screen.findByText('暂无依赖数据（构建期生成）')).toBeInTheDocument()
    expect(screen.queryByText('react')).not.toBeInTheDocument()
  })
})
