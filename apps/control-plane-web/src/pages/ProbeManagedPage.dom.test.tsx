/**
 * @file 探针托管总控页（单页 3 Tab）的用例。
 *
 * 验两件事：
 * 1. **入口收敛**：三个 Tab 都在一个页面里，不再需要在一级导航间跳转；
 * 2. **Tab 状态进 URL**：`?tab=` 决定当前 Tab，刷新与分享链接都能回到同一处。
 *
 * 被复用的两个业务页各自有完整测试，这里只测容器编排，不重复验业务逻辑。
 */
import { describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'

// 被复用的页面在真实环境里会拉一堆接口；容器的职责只是 Tab 编排，
// 故用轻量替身，让用例聚焦在"哪个 Tab 渲染了谁"。
vi.mock('@/pages/ManagedConfigPage', () => ({
  default: ({ defaultTab }: { defaultTab?: string }) => (
    <div data-testid="stub-config-page">config-page:{defaultTab ?? 'layers'}</div>
  ),
}))

vi.mock('@/pages/ManagedMatrixPage', () => ({
  default: () => <div data-testid="stub-matrix-page">matrix-page</div>,
}))

import ProbeManagedPage from '@/pages/ProbeManagedPage'

describe('探针托管总控页', () => {
  it('默认展示「配置与下发」，并渲染出三个 Tab', async () => {
    renderWithProviders(<ProbeManagedPage />, { route: '/probe/managed' })

    expect(screen.getByTestId('managed-tab-config')).toBeInTheDocument()
    expect(screen.getByTestId('managed-tab-matrix')).toBeInTheDocument()
    expect(screen.getByTestId('managed-tab-history')).toBeInTheDocument()

    // 默认落在配置与下发（用户最高频的动作）。
    expect(await screen.findByTestId('stub-config-page')).toHaveTextContent('config-page:layers')
  })

  it('切到「对账矩阵」渲染矩阵页', async () => {
    const user = userEvent.setup()
    renderWithProviders(<ProbeManagedPage />, { route: '/probe/managed' })

    await user.click(screen.getByTestId('managed-tab-matrix'))
    expect(await screen.findByTestId('stub-matrix-page')).toBeInTheDocument()
  })

  it('切到「下发历史」时复用配置页但停在 pushes 签页', async () => {
    const user = userEvent.setup()
    renderWithProviders(<ProbeManagedPage />, { route: '/probe/managed' })

    await user.click(screen.getByTestId('managed-tab-history'))
    // 复用同一页面组件、用 defaultTab 指定内层签页——不重写已验证的下发逻辑。
    expect(await screen.findByTestId('stub-config-page')).toHaveTextContent('config-page:pushes')
  })

  it('URL 带 ?tab=matrix 时直接落在矩阵 Tab（可分享链接）', async () => {
    renderWithProviders(<ProbeManagedPage />, { route: '/probe/managed?tab=matrix' })

    expect(await screen.findByTestId('stub-matrix-page')).toBeInTheDocument()
  })

  it('URL 的 tab 非法时回退到配置 Tab，而不是白屏', async () => {
    renderWithProviders(<ProbeManagedPage />, { route: '/probe/managed?tab=nonexistent' })

    expect(await screen.findByTestId('stub-config-page')).toHaveTextContent('config-page:layers')
  })
})
