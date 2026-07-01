import { describe, it, expect } from 'vitest'
import { screen } from '@testing-library/react'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import ClientSignKeyCard from './ClientSignKeyCard'

/**
 * ClientSignKeyCard 强断言（FR-248）：渲染 mock 公钥 + keyId + 来源徽章 + 复制按钮；
 * 注入 503（signer 未配置）显降级提示、不崩。渲染前 loginMockUser() 让 requireAuth 放行。
 */
describe('ClientSignKeyCard（mock 假后端）', () => {
  it('渲染签名公钥 + keyId + 来源徽章 + 复制按钮', async () => {
    loginMockUser()
    renderWithProviders(<ClientSignKeyCard />)

    // 公钥文本（等宽区）出现。
    const code = await screen.findByTestId('sign-key-public')
    expect(code).toHaveTextContent('MCowBQYDK2VwAyEAsO7B/k+2++wQtN/L0jpCXCjsGnYV5Sx2eyCk0pDzV0Y=')
    // keyId 与来源徽章（generated → 自动生成）。
    expect(screen.getByText('k1')).toBeInTheDocument()
    expect(screen.getByText('自动生成')).toBeInTheDocument()
    // 复制按钮存在。
    expect(screen.getByRole('button', { name: /复制公钥/ })).toBeInTheDocument()
  })

  it('注入 503（签名器未配置）→ 显降级提示，不崩', async () => {
    loginMockUser()
    mockInject('get', '/client-dist/sign-key', { kind: 'status', status: 503 })
    renderWithProviders(<ClientSignKeyCard />)

    // 卡片标题仍在，正文降级为「暂无法获取签名公钥」提示，无公钥区、不白屏。
    expect(await screen.findByText('签名公钥')).toBeInTheDocument()
    expect(await screen.findByText(/暂无法获取签名公钥/)).toBeInTheDocument()
    expect(screen.queryByTestId('sign-key-public')).not.toBeInTheDocument()
  })
})
