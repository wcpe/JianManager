import { describe, it, expect } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { OutboundTestButton } from './OutboundTestButton'

/**
 * 出站连通性测试按钮（FR-280）：editable 模式渲染可编辑目标 URL（默认非 GitHub），
 * 测任意地址；非 editable 保持固定目标一键测试。mock /diagnostics/http-test 恒返 200 可达。
 */
describe('OutboundTestButton（可自定义目标，FR-280）', () => {
  it('editable：默认 google、可改任意地址并测通', async () => {
    loginMockUser()
    renderWithProviders(<OutboundTestButton defaultUrl="https://www.google.com" editable label="测试出站连通性" />)
    const input = screen.getByLabelText('测试目标地址') as HTMLInputElement
    expect(input.value).toBe('https://www.google.com')
    expect(input.value).not.toContain('github')

    await userEvent.clear(input)
    await userEvent.type(input, 'https://example.com')
    await userEvent.click(screen.getByRole('button', { name: '测试出站连通性' }))
    // mock 恒返可达 200 → 出现可达态。
    expect(await screen.findByText(/可达/)).toBeInTheDocument()
  })

  it('非 editable：无输入框，固定目标一键测试', async () => {
    loginMockUser()
    renderWithProviders(<OutboundTestButton defaultUrl="https://api.foojay.io/x" label="测试 JDK 下载源" />)
    expect(screen.queryByLabelText('测试目标地址')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '测试 JDK 下载源' }))
    expect(await screen.findByText(/可达/)).toBeInTheDocument()
  })
})
