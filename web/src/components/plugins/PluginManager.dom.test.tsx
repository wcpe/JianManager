import { describe, it, expect } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import PluginManager from './PluginManager'

/**
 * PluginManager 强断言（FR-206 插件域）：渲染 seed 插件 / 启用禁用切换联动 / 注入 500 显错误态。
 * seed 插件挂在 instanceId=1（见 mocks/handlers/domains/plugin.ts）。
 */
describe('PluginManager（mock 假后端）', () => {
  it('渲染 seed 插件列表', async () => {
    loginMockUser()
    renderWithProviders(<PluginManager instanceId={1} />)
    expect(await screen.findByText('EssentialsX.jar')).toBeInTheDocument()
    expect(screen.getByText('WorldEdit.jar')).toBeInTheDocument()
  })

  it('禁用写操作 → 该行状态联动（已启用 → 已禁用）', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<PluginManager instanceId={1} />)

    const row = (await screen.findByText('EssentialsX.jar')).closest('tr') as HTMLElement
    expect(within(row).getByText('已启用')).toBeInTheDocument()

    // 点该行「禁用」→ 切换 enabled，列表刷新后状态变「已禁用」。
    await user.click(within(row).getByRole('button', { name: '禁用' }))
    await waitFor(() => {
      const after = screen.getByText('EssentialsX.jar').closest('tr') as HTMLElement
      expect(within(after).getByText('已禁用')).toBeInTheDocument()
    })
  })

  it('注入 500 → 显示加载失败错误态', async () => {
    mockInject('get', '/instances/:id/plugins', { kind: 'status', status: 500, body: { message: '插件列表加载失败' } })
    loginMockUser()
    renderWithProviders(<PluginManager instanceId={1} />)
    expect(await screen.findByText(/插件列表加载失败|加载失败/)).toBeInTheDocument()
  })
})
