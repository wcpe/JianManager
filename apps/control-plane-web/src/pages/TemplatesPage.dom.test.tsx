import { describe, it, expect } from 'vitest'
import { screen, within, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import TemplatesPage from './TemplatesPage'

/**
 * TemplatesPage 强断言（FR-202 供给与模板域）：验 render harness + 假后端 templates 联动 + 错误注入。
 * 三条统一断言：① 渲染出 seed 模板；② 创建 / 删除后列表联动；③ 注入错误后页面显错误态（非崩溃）。
 * 受 requireAuth 保护的端点（GET/POST/DELETE /templates）需先 loginMockUser 放行。
 */
describe('TemplatesPage（mock 假后端）', () => {
  it('渲染出 seed 模板卡片', async () => {
    loginMockUser()
    renderWithProviders(<TemplatesPage />, { route: '/templates' })

    expect(await screen.findByText('Paper 1.21')).toBeInTheDocument()
    expect(screen.getByText('Velocity 代理')).toBeInTheDocument()
    expect(screen.getByText('原版 1.21')).toBeInTheDocument()
    // 应用总数统计卡反映 seed 行数（3）。
    expect(screen.getByText('应用总数')).toBeInTheDocument()
  })

  it('创建模板 → 列表联动出现新卡片', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<TemplatesPage />, { route: '/templates' })
    await screen.findByText('Paper 1.21')

    await user.click(screen.getByRole('button', { name: '新建模板' }))
    const dialog = await screen.findByRole('dialog')
    // 必填字段标签含「*」与 sr-only 文案，用正则匹配标签文本。
    await user.type(within(dialog).getByLabelText(/^名称/), '测试模板')
    await user.type(within(dialog).getByLabelText(/^启动命令/), 'java -jar test.jar nogui')
    await user.click(within(dialog).getByRole('button', { name: '创建' }))

    // POST /templates 后 useTemplates 失效重取，新模板卡片应出现。
    expect(await screen.findByText('测试模板')).toBeInTheDocument()
  })

  it('删除模板 → 列表联动移除该卡片', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<TemplatesPage />, { route: '/templates' })
    await screen.findByText('Velocity 代理')

    // 在「Velocity 代理」卡片上点删除按钮（aria-label=删除）。
    const card = screen.getByText('Velocity 代理').closest('[data-slot="panel"]') as HTMLElement
    await user.click(within(card).getByRole('button', { name: '删除' }))

    // DangerConfirm 弹窗里确认（destructive「删除」按钮）。
    const confirmDialog = await screen.findByRole('dialog')
    await user.click(within(confirmDialog).getByRole('button', { name: '删除' }))

    // DELETE /templates/:id 后列表失效重取，该卡片消失（其余仍在）。
    await waitFor(() => expect(screen.queryByText('Velocity 代理')).not.toBeInTheDocument())
    expect(screen.getByText('Paper 1.21')).toBeInTheDocument()
  })

  it('注入 500 → 显示空/错误态而非崩溃', async () => {
    loginMockUser()
    mockInject('get', '/templates', { kind: 'status', status: 500 })
    renderWithProviders(<TemplatesPage />, { route: '/templates' })

    // GET /templates 失败 → data 为空 → 渲染空态面板，页面不崩溃、无 seed 卡片。
    expect(await screen.findByText('暂无模板')).toBeInTheDocument()
    expect(screen.queryByText('Paper 1.21')).not.toBeInTheDocument()
  })
})
