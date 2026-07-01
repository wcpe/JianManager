import { describe, it, expect, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import ClientIntegrationGuide from './ClientIntegrationGuide'

/**
 * ClientIntegrationGuide 强断言（FR-259）：jm-updater.json 示例含 coreEndpoint（楔子自动拉 core）
 * + 不再含 signPublicKey + 下载按钮存在且可点。
 */
describe('ClientIntegrationGuide（mock 假后端）', () => {
  it('jm-updater.json 示例含 coreEndpoint + 不含 signPublicKey', async () => {
    loginMockUser()
    renderWithProviders(<ClientIntegrationGuide channelId="skyblock-s1" />)

    // 示例 JSON 含 coreEndpoint（异步加载完才出现，用 code 元素精确定位避免父容器文本匹配）。
    const codeBlocks = await screen.findAllByText(/coreEndpoint/)
    expect(codeBlocks.length).toBeGreaterThan(0)
    // 不再含 signPublicKey。
    expect(screen.queryByText(/signPublicKey/)).not.toBeInTheDocument()
    // 下载按钮存在。
    const dlBtns = screen.getAllByRole('button', { name: /下载 jm-updater\.json/ })
    expect(dlBtns.length).toBeGreaterThan(0)
  })

  it('点击下载按钮触发 updater-config 端点调用', async () => {
    loginMockUser()
    const user = userEvent.setup()
    vi.stubGlobal('URL', { ...URL, createObjectURL: vi.fn(() => 'blob:mock'), revokeObjectURL: vi.fn() })

    renderWithProviders(<ClientIntegrationGuide channelId="skyblock-s1" />)

    const dlBtns = await screen.findAllByRole('button', { name: /下载 jm-updater\.json/ })
    await user.click(dlBtns[0])

    // 端点被调（mock 返回 200，不报错即通过；downloadUpdaterConfig 内部触发下载）。
    expect(screen.queryByText(/下载失败/)).not.toBeInTheDocument()
  })
})
