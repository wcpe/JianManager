import { describe, it, expect } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import EconomySegment from './EconomySegment'

/**
 * EconomySegment 强断言（FR-206 经济台，代表业务/经济域）：
 * manifest 驱动渲染 → 余额查询命中 seed 镜像 → 注入 500 显查询失败错误态。
 * 经济镜像 seed：Steve coin 1000.00、Alex coin 250.50（见 mocks/handlers/domains/plugin.ts）。
 */
describe('EconomySegment（mock 假后端）', () => {
  it('能力可用 → 余额查询命中 seed 镜像余额', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<EconomySegment instanceId={1} />)

    // manifest 加载后渲染余额子页（默认 tab）：填玩家 Steve → 查询 → 命中镜像余额 1000.00。
    await user.type(await screen.findByLabelText('玩家'), 'Steve')
    await user.click(screen.getByRole('button', { name: '查询' }))
    expect(await screen.findByText('1000.00')).toBeInTheDocument()
  })

  it('注入 500 → 余额查询显示查询失败错误态', async () => {
    const user = userEvent.setup()
    mockInject('get', '/business/economy/mirror', { kind: 'status', status: 500 })
    loginMockUser()
    renderWithProviders(<EconomySegment instanceId={1} />)

    await user.type(await screen.findByLabelText('玩家'), 'Steve')
    await user.click(screen.getByRole('button', { name: '查询' }))
    expect(await screen.findByText('查询失败')).toBeInTheDocument()
  })
})
