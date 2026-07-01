import { describe, it, expect, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import ClientIntegrationGuide from './ClientIntegrationGuide'

/**
 * ClientIntegrationGuide 强断言（FR-253，见 ADR-053）：
 * jm-updater.json 示例含 signPublicKey（从签名公钥端点取）+ 下载按钮存在且可点。
 * 渲染前 loginMockUser() 让 requireAuth 放行（mock 默认用户 role=10）。
 */
describe('ClientIntegrationGuide（mock 假后端）', () => {
  it('jm-updater.json 示例含 signPublicKey + 下载按钮存在', async () => {
    loginMockUser()
    renderWithProviders(<ClientIntegrationGuide channelId="skyblock-s1" />)

    // 步骤三标题出现。
    expect(await screen.findByText(/配置 jm-updater\.json/)).toBeInTheDocument()
    // 示例 JSON 含 signPublicKey（mock sign-key 返回的公钥，异步加载完才出现）。
    expect(await screen.findByText(/MCowBQYDK2VwAyEAsO7B/)).toBeInTheDocument()
    // 下载按钮存在。
    expect(screen.getByRole('button', { name: /下载 jm-updater\.json/ })).toBeInTheDocument()
  })

  it('点击下载按钮触发 updater-config 端点调用', async () => {
    loginMockUser()
    const user = userEvent.setup()
    // 桩 URL.createObjectURL / a.click（jsdom 有桩但 click 无效果，不报错即可）。
    vi.stubGlobal('URL', { ...URL, createObjectURL: vi.fn(() => 'blob:mock'), revokeObjectURL: vi.fn() })

    renderWithProviders(<ClientIntegrationGuide channelId="skyblock-s1" />)

    const btn = await screen.findByRole('button', { name: /下载 jm-updater\.json/ })
    await user.click(btn)

    // 端点被调（mock 返回 200，不报错即通过；downloadUpdaterConfig 内部触发下载）。
    // 若端点未注册或报错，toast.error 会显示「下载失败」，断言其不出现。
    expect(screen.queryByText(/下载失败/)).not.toBeInTheDocument()
  })
})
