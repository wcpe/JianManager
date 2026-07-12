import { describe, it, expect } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { useAuthStore } from '@/stores/auth'
import { mockInject } from '@jianmanager/devmock/inject'
import InventorySegment from './InventorySegment'

/** InventorySegment 背包定制页：manifest 发现 → 背包查看 → 基础属性写二次确认。 */
describe('InventorySegment（mock 假后端）', () => {
  it('查询玩家背包快照并展示物品清单', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<InventorySegment instanceId={1} />)

    await user.type(await screen.findByLabelText('玩家 UUID'), 'Steve')
    await user.click(screen.getByRole('button', { name: '查询背包' }))

    expect(await screen.findByText('DIAMOND_SWORD')).toBeInTheDocument()
    expect(screen.getByText('COOKED_BEEF')).toBeInTheDocument()
    expect(screen.getByText(/数据版本/)).toBeInTheDocument()
  })

  it('基础属性写经危险确认并显示离线待生效回执', async () => {
    const user = userEvent.setup()
    loginMockUser()
    useAuthStore.setState({ role: 1, isAuthenticated: true })
    renderWithProviders(<InventorySegment instanceId={1} />)

    await user.type(await screen.findByLabelText('玩家 UUID'), 'Steve')
    await user.click(screen.getByRole('button', { name: '查询背包' }))
    await user.click(await screen.findByRole('button', { name: '调整基础属性' }))

    const dialog = await screen.findByRole('dialog', { name: '调整基础属性' })
    await user.clear(within(dialog).getByLabelText('生命值'))
    await user.type(within(dialog).getByLabelText('生命值'), '18')
    await user.type(within(dialog).getByLabelText('原因（可选）'), '验证基础属性写入')
    await user.click(within(dialog).getByRole('button', { name: '提交写入' }))

    const confirm = (await screen.findByText('确认写入基础属性')).closest('[role="dialog"]') as HTMLElement
    await user.click(within(confirm).getByRole('button', { name: '确认写入' }))

    expect(await screen.findByText('基础属性已写入，等待玩家上线生效')).toBeInTheDocument()
  })

  it('manifest 无写动作时只展示背包查看，不展示基础属性写入口', async () => {
    const user = userEvent.setup()
    mockInject('get', '/instances/:id/business/manifest', {
      kind: 'status',
      status: 200,
      body: {
        instanceId: 1,
        domain: 'jbis',
        action: 'manifest',
        available: true,
        output: {
          domains: {
            inventory: {
              actions: [{ action: 'view', args: ['player'], readOnly: true }],
            },
          },
        },
        error: '',
      },
    })
    loginMockUser()
    renderWithProviders(<InventorySegment instanceId={1} />)

    await user.type(await screen.findByLabelText('玩家 UUID'), 'Steve')
    await user.click(screen.getByRole('button', { name: '查询背包' }))

    expect(await screen.findByText('DIAMOND_SWORD')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '调整基础属性' })).not.toBeInTheDocument()
  })
})
