import { describe, it, expect } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import BusinessSegment from './BusinessSegment'

/**
 * BusinessSegment 强断言（FR-206 业务台）：
 * manifest 驱动渲染能力清单 → 选只读动作下发命中 → 注入 manifest 500 显不可用降级。
 * manifest seed 含 economy 域 balance/leaderboard/transfer/... （见 mocks/handlers/domains/plugin.ts）。
 */
describe('BusinessSegment（mock 假后端）', () => {
  it('manifest 驱动渲染能力清单（economy 域动作）', async () => {
    loginMockUser()
    renderWithProviders(<BusinessSegment instanceId={1} />)
    // 能力清单渲染出 economy 域与其只读动作。
    expect(await screen.findByText('balance')).toBeInTheDocument()
    expect(screen.getByText('leaderboard')).toBeInTheDocument()
  })

  it('选只读动作下发 → 透传命中 seed 镜像余额（联动）', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<BusinessSegment instanceId={1} />)

    // 选 balance（只读）→ 填 args → 直接下发（读动作无二次确认）。
    await user.click(await screen.findByText('balance'))
    await user.type(await screen.findByLabelText('player'), 'Steve')
    await user.type(screen.getByLabelText('currency'), 'coin')
    await user.click(screen.getByRole('button', { name: '下发' }))

    // 结果区透传 mock 输出（含 seed 镜像余额 1000.00）。
    expect(await screen.findByText(/1000\.00/)).toBeInTheDocument()
  })

  it('注入 manifest 500 → 显示业务能力不可用降级', async () => {
    mockInject('get', '/instances/:id/business/manifest', { kind: 'status', status: 500 })
    loginMockUser()
    renderWithProviders(<BusinessSegment instanceId={1} />)
    // 能力发现失败 → 渲染 unavailable 降级文案（非崩溃）。
    expect(await screen.findByText(/探针未连入或本服无业务 Provider/)).toBeInTheDocument()
  })
})
