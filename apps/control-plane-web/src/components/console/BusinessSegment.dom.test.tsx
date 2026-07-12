import { describe, it, expect } from 'vitest'
import { fireEvent, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { useAuthStore } from '@/stores/auth'
import { mockInject } from '@jianmanager/devmock/inject'
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

  it('对象型入参下发为真嵌套对象（inventory.writeBasicAttrs 的 edited 还原为对象）', async () => {
    const user = userEvent.setup()
    loginMockUser()
    // 写动作二次确认需组管理员及以上（FR-059 角色门禁）。
    useAuthStore.setState({ role: 1, isAuthenticated: true })
    renderWithProviders(<BusinessSegment instanceId={1} />)

    // 选 inventory 域写动作 writeBasicAttrs → 渲染 player/base/edited 三个入参框。
    await user.click(await screen.findByText('writeBasicAttrs'))
    await user.type(await screen.findByLabelText('player'), 'Steve')
    // 对象型入参：运维在文本框粘 JSON（用 fireEvent 直填避免 userEvent 把 { 当特殊按键）。
    // 契约为 {dataVersion,basicAttrs:{...}} 嵌套对象，下发前须还原为真对象，探针 getObject 才拿得到。
    fireEvent.change(screen.getByLabelText('base'), {
      target: { value: JSON.stringify({ dataVersion: 42, basicAttrs: { health: 20 } }) },
    })
    fireEvent.change(screen.getByLabelText('edited'), {
      target: { value: JSON.stringify({ dataVersion: 42, basicAttrs: { health: 18 } }) },
    })

    // 写动作走二次确认后下发。
    await user.click(screen.getByRole('button', { name: '下发' }))
    const confirm = (await screen.findByText('确认高危业务写')).closest('[role="dialog"]') as HTMLElement
    await user.click(within(confirm).getByRole('button', { name: '确认下发' }))

    // edited 被当真嵌套对象收下 → mock 命中 basicAttrs 写入，回执 success 且版本自增（42→43）。
    // 若被当字符串下发，mock 侧 edited.basicAttrs 取不到 → 回 PLAYER_NOT_FOUND，断言失败即复现 bug。
    expect(await screen.findByText(/"success": true/)).toBeInTheDocument()
    expect(screen.getByText(/"newDataVersion": 43/)).toBeInTheDocument()
  })

  it('注入 manifest 500 → 显示业务能力不可用降级', async () => {
    mockInject('get', '/instances/:id/business/manifest', { kind: 'status', status: 500 })
    loginMockUser()
    renderWithProviders(<BusinessSegment instanceId={1} />)
    // 能力发现失败 → 渲染 unavailable 降级文案（非崩溃）。
    expect(await screen.findByText(/探针未连入或本服无业务 Provider/)).toBeInTheDocument()
  })
})
