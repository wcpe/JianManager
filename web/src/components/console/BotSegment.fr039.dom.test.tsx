import { beforeAll, describe, expect, it } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import InstanceTree from './InstanceTree'
import InstanceConsolePage from './InstanceConsolePage'
import BotSegment from './BotSegment'

beforeAll(() => {
  if (!Element.prototype.scrollIntoView) Element.prototype.scrollIntoView = () => {}
  if (!Element.prototype.hasPointerCapture) Element.prototype.hasPointerCapture = () => false
  if (!Element.prototype.setPointerCapture) Element.prototype.setPointerCapture = () => {}
  if (!Element.prototype.releasePointerCapture) Element.prototype.releasePointerCapture = () => {}
  if (!('ResizeObserver' in globalThis)) {
    globalThis.ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    } as unknown as typeof ResizeObserver
  }
})

describe('FR-039 控制台实例内 Bot 管理段', () => {
  it('实例树通过聚合摘要显示 Bot 徽标，不展开逐个 Bot', async () => {
    loginMockUser()
    renderWithProviders(<InstanceTree />)

    expect(await screen.findByText('survival-1')).toBeInTheDocument()
    expect(await screen.findByText('1/2')).toBeInTheDocument()
    expect(screen.getByText('0/1')).toBeInTheDocument()
    expect(screen.queryByText('GuardBot')).not.toBeInTheDocument()
  })

  it('统一服务器控制台可切到 Bot 分区并渲染实例内 Bot 段', async () => {
    loginMockUser()
    renderWithProviders(<InstanceConsolePage instanceId={1} />)

    await userEvent.click(await screen.findByRole('button', { name: 'Bot' }))

    expect(await screen.findByText('当前筛选 2 个 Bot')).toBeInTheDocument()
    expect(screen.getByText('GuardBot')).toBeInTheDocument()
    expect(screen.getByText('FollowBot')).toBeInTheDocument()
    expect(screen.getByText('共 2 个')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '+ 创建 Bot' })).toBeInTheDocument()
  })

  it('Bot 段支持勾选、批量设行为与聚合计数联动', async () => {
    loginMockUser()
    renderWithProviders(<BotSegment instanceId={1} />)

    expect(await screen.findByText('当前筛选 2 个 Bot')).toBeInTheDocument()
    expect(screen.getByText('GuardBot')).toBeInTheDocument()

    await userEvent.click(screen.getByRole('checkbox', { name: 'GuardBot' }))
    expect(screen.getByText('已选 1 个 Bot')).toBeInTheDocument()

    const batchBar = screen.getByText('已选 1 个 Bot').closest('div') as HTMLElement
    await userEvent.click(within(batchBar).getByRole('combobox'))
    await userEvent.click(await screen.findByRole('option', { name: '巡逻' }))
    await userEvent.click(within(batchBar).getByRole('button', { name: '批量设行为' }))

    await waitFor(() => {
      const guardRow = screen.getByText('GuardBot').closest('li') as HTMLElement
      expect(within(guardRow).getByText('巡逻')).toBeInTheDocument()
    })
  })
})
