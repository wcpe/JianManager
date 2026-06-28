import { describe, it, expect } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import AlertsPage from './AlertsPage'

/**
 * AlertsPage 强断言（FR-208 可观测日志域）：验种子规则/事件/通道渲染、写操作联动、错误注入。
 * 渲染前 loginMockUser() 让 requireAuth 保护的 alerts 端点放行。
 * RuleDialog 是自绘模态（MODAL_PANEL，无 role=dialog），故按标题文案锁定其面板再取字段。
 */
describe('AlertsPage（mock 假后端）', () => {
  it('① 渲染出种子告警规则', async () => {
    loginMockUser()
    renderWithProviders(<AlertsPage />)
    expect(await screen.findByText('CPU 过载告警')).toBeInTheDocument()
    expect(screen.getByText('实例崩溃告警')).toBeInTheDocument()
  })

  it('② 事件页：渲染种子事件，级别筛选 critical 后联动收敛', async () => {
    loginMockUser()
    renderWithProviders(<AlertsPage />)
    await screen.findByText('CPU 过载告警')

    // 事件 Tab 按钮名含未读角标数字（如「事件1」），用前缀匹配。
    await userEvent.click(screen.getByRole('button', { name: /^事件/ }))
    expect(await screen.findByText(/CPU 使用率 91\.5%/)).toBeInTheDocument()
    expect(screen.getByText(/异常退出/)).toBeInTheDocument()

    // 级别下拉（首个原生 select）选 critical → 仅崩溃事件保留，CPU 事件消失（跨 endpoint 联动）。
    const selects = screen.getAllByRole('combobox')
    await userEvent.selectOptions(selects[0], 'critical')
    await waitFor(() => expect(screen.queryByText(/CPU 使用率 91\.5%/)).not.toBeInTheDocument())
    expect(screen.getByText(/异常退出/)).toBeInTheDocument()
  })

  it('② 创建规则 → 列表联动出现新规则', async () => {
    loginMockUser()
    renderWithProviders(<AlertsPage />)
    await screen.findByText('CPU 过载告警')

    await userEvent.click(screen.getByRole('button', { name: /创建规则/ }))
    // 自绘模态：以标题「创建规则」定位面板，再取其内首个文本框（规则名称）。
    const heading = await screen.findByRole('heading', { name: '创建规则' })
    const panel = heading.parentElement as HTMLElement
    const nameInput = within(panel).getAllByRole('textbox')[0]
    await userEvent.type(nameInput, 'TPS 过低告警')
    await userEvent.click(within(panel).getByRole('button', { name: '保存' }))

    expect(await screen.findByText('TPS 过低告警')).toBeInTheDocument()
  })

  it('③ 注入 500 → 规则列表显示空态（非崩溃）', async () => {
    loginMockUser()
    mockInject('get', '/alerts/rules', { kind: 'status', status: 500 })
    renderWithProviders(<AlertsPage />)
    // 规则查询失败 → rules 为 undefined → 走空态文案，页面不崩溃。
    expect(await screen.findByText('暂无告警规则')).toBeInTheDocument()
  })
})
