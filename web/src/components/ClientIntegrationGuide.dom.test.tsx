import { describe, it, expect, vi } from 'vitest'
import { screen } from '@testing-library/react'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import ClientIntegrationGuide from './ClientIntegrationGuide'

/**
 * ClientIntegrationGuide 强断言（FR-259）：jm-updater.json 示例只含 API 根 endpoint
 * + 不再含 coreEndpoint/signPublicKey + 下载按钮存在且可点。
 */
describe('ClientIntegrationGuide（mock 假后端）', () => {
  it('jm-updater.json 示例只含 API 根 endpoint', async () => {
    loginMockUser()
    renderWithProviders(<ClientIntegrationGuide channelId="skyblock-s1" keys={[]} />)

    expect(await screen.findByText(/"endpoint": "http:\/\/localhost:3000\/api\/v1"/)).toBeInTheDocument()
    expect(screen.queryByText(/coreEndpoint/)).not.toBeInTheDocument()
    expect(screen.queryByText(/signPublicKey/)).not.toBeInTheDocument()
    const dlBtns = screen.getAllByRole('button', { name: /下载 jm-updater\.json/ })
    expect(dlBtns.length).toBeGreaterThan(0)
  })

  it('未填入真实密钥时禁止下载配置文件', async () => {
    loginMockUser()
    vi.stubGlobal('URL', { ...URL, createObjectURL: vi.fn(() => 'blob:mock'), revokeObjectURL: vi.fn() })

    renderWithProviders(<ClientIntegrationGuide channelId="skyblock-s1" keys={[]} />)

    const dlBtns = await screen.findAllByRole('button', { name: /下载 jm-updater\.json/ })
    expect(dlBtns[0]).toBeDisabled()
    expect(URL.createObjectURL).not.toHaveBeenCalled()
  })
})
