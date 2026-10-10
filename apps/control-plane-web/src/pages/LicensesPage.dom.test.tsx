import { describe, it, expect } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import LicensesPage from './LicensesPage'

/**
 * LicensesPage 强断言（FR-210）：渲染 seed 依赖清单 → 搜索过滤联动 → 注入 500 显空/错误态。
 * 许可清单走静态资源 /licenses.json（裸 http.get mock），非 /api，故错误注入用 server.use 覆盖。
 */
describe('LicensesPage（mock 假后端）', () => {
  it('渲染五个发行来源及其代表依赖', async () => {
    loginMockUser()
    renderWithProviders(<LicensesPage />)
    expect(await screen.findByText('react')).toBeInTheDocument()
    expect(screen.getByText('mineflayer')).toBeInTheDocument()
    expect(screen.getByText('github.com/gin-gonic/gin')).toBeInTheDocument()
    expect(screen.getByText('com.github.luben:zstd-jni')).toBeInTheDocument()
    expect(screen.getByText('org.ow2.asm:asm')).toBeInTheDocument()
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

  it('注入 500 → 显示错误态而非「暂无依赖数据」（不崩溃）', async () => {
    loginMockUser()
    server.use(http.get('*/licenses.json', () => HttpResponse.json({ message: 'boom' }, { status: 500 })))
    renderWithProviders(<LicensesPage />)
    // 取数失败与「清单为空」必须区分：后者会被读成「构建期没生成清单」。
    expect(await screen.findByTestId('licenses-error')).toBeInTheDocument()
    expect(screen.queryByText('暂无依赖数据（构建期生成）')).not.toBeInTheDocument()
    expect(screen.queryByText('react')).not.toBeInTheDocument()
  })

  it('展开开关键盘可达：聚焦后回车即可展开全文（aria-expanded 联动）', async () => {
    loginMockUser()
    const user = userEvent.setup()
    renderWithProviders(<LicensesPage />)
    await screen.findByText('react')

    // 此前整行靠 <tr onClick>：tr 不可聚焦、无 role，键盘用户完全无法展开。
    const toggle = screen.getAllByTestId('license-toggle')[0]!
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    toggle.focus()
    expect(toggle).toHaveFocus()

    await user.keyboard('{Enter}')
    expect(await screen.findByText('MIT License — react')).toBeInTheDocument()
    expect(screen.getAllByTestId('license-toggle')[0]).toHaveAttribute('aria-expanded', 'true')
  })

  it('依赖表虚拟化：数百行只渲染滚动窗口内的行', async () => {
    loginMockUser()
    const dependencies = Array.from({ length: 150 }, (_, i) => ({
      name: `pkg-${i + 1}`,
      version: '1.0.0',
      license: 'MIT',
      author: 'someone',
      url: '',
      scope: 'web',
      ecosystem: 'npm',
      type: 'runtime',
      licenseText: `license text ${i + 1}`,
    }))
    server.use(http.get('*/licenses.json', () => HttpResponse.json({ generatedAt: '', dependencies })))
    renderWithProviders(<LicensesPage />)
    await screen.findByText('pkg-1')

    const rows = screen.getAllByTestId('license-row')
    expect(rows.length).toBeGreaterThan(0)
    expect(rows.length).toBeLessThan(dependencies.length)
    // 窗口首行在、远处的行不在：确实按滚动窗口裁剪，而非整表进 DOM。
    expect(screen.queryByText('pkg-150')).toBeNull()
    expect(screen.getByTestId('licenses-virtual-spacer-after')).toBeInTheDocument()
  })
})
