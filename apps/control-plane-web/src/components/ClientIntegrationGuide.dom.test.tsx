import { describe, it, expect, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@jianmanager/devmock/inject'
import ClientIntegrationGuide from './ClientIntegrationGuide'
import ClientDistFlowGuide from './ClientDistFlowGuide'

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

  it('展示内嵌版本、core 整数版本与两件套可用性和体积', async () => {
    loginMockUser()
    renderWithProviders(<ClientIntegrationGuide channelId="skyblock-s1" keys={[]} />)

    const info = await screen.findByTestId('embedded-updater-info')
    expect(info).toHaveTextContent('0.9.0')
    expect(info).toHaveTextContent('core 整数版本')
    expect(info).toHaveTextContent('3')
    expect(info).toHaveTextContent('wedge.jar')
    expect(info).toHaveTextContent('32.0 KB')
    expect(info).toHaveTextContent('updater-core.jar')
    expect(info).toHaveTextContent('1.0 MB')
  })

  it('楔子未内嵌时禁用下载并显示明确说明', async () => {
    loginMockUser()
    mockInject('get', '/client-dist/updater-jars', {
      kind: 'status',
      status: 200,
      body: {
        version: '0.9.0',
        coreVersion: '3',
        wedge: { available: false, size: 0 },
        core: { available: false, size: 0 },
      },
    })
    renderWithProviders(<ClientIntegrationGuide channelId="skyblock-s1" keys={[]} />)

    const info = await screen.findByTestId('embedded-updater-info')
    expect(screen.getByRole('button', { name: /wedge\.jar/ })).toBeDisabled()
    expect(screen.getByRole('status')).toHaveTextContent('楔子未内嵌，当前无法下载')
    expect(within(info).getAllByText(/不可用 · 0 B/)).toHaveLength(2)
  })

  it('未填入真实密钥时禁止下载配置文件', async () => {
    loginMockUser()
    vi.stubGlobal('URL', { ...URL, createObjectURL: vi.fn(() => 'blob:mock'), revokeObjectURL: vi.fn() })

    renderWithProviders(<ClientIntegrationGuide channelId="skyblock-s1" keys={[]} />)

    const dlBtns = await screen.findAllByRole('button', { name: /下载 jm-updater\.json/ })
    expect(dlBtns[0]).toBeDisabled()
    expect(URL.createObjectURL).not.toHaveBeenCalled()
  })

  it('接入指引符合无签名与可查看密钥的当前协议', async () => {
    loginMockUser()
    renderWithProviders(<ClientIntegrationGuide channelId="skyblock-s1" keys={[]} />)

    expect(await screen.findByText(/拉 manifest/)).toBeInTheDocument()
    expect(screen.getByText(/可随时查看明文/)).toBeInTheDocument()
    expect(screen.queryByText(/签名 manifest/)).not.toBeInTheDocument()
    expect(screen.queryByText(/明文仅创建时一次性显示/)).not.toBeInTheDocument()
  })

  it('流程图不再宣称签名验签或随整合包携带 updater-core', async () => {
    const user = (await import('@testing-library/user-event')).default.setup()
    renderWithProviders(<ClientDistFlowGuide />)

    await user.click(screen.getByRole('button', { name: /分发是怎么跑起来的/ }))
    expect(screen.getByText(/系统自动生成文件清单与 SHA-256/)).toBeInTheDocument()
    expect(screen.getByText(/楔子会在首次启动时自动拉取更新核心/)).toBeInTheDocument()
    expect(screen.queryByText(/系统自动签名/)).not.toBeInTheDocument()
    expect(screen.queryByText(/验签/)).not.toBeInTheDocument()
    expect(screen.queryByText(/更新核心\.jar.*一起塞进整合包/)).not.toBeInTheDocument()
  })
})
