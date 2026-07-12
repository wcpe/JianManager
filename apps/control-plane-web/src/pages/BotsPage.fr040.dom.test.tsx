import { beforeAll, describe, expect, it } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import BotsPage from './BotsPage'

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

describe('FR-040 全局 Bot 管理页重构', () => {
  it('默认展示聚合总览与实例分组，未展开时不逐个铺开 Bot', async () => {
    loginMockUser()
    renderWithProviders(<BotsPage />, { route: '/bots' })

    expect(await screen.findByRole('heading', { name: 'Bot 管理' })).toBeInTheDocument()
    expect(await screen.findByText('舰队健康')).toBeInTheDocument()
    expect(await screen.findByText('2 实例 · 2 节点')).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: '生存服' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '空岛服' })).toBeInTheDocument()
    expect(screen.queryByText('GuardBot')).not.toBeInTheDocument()
  })

  it('支持切换节点与行为分组维度，仍只显示聚合组', async () => {
    loginMockUser()
    renderWithProviders(<BotsPage />, { route: '/bots' })

    await screen.findByRole('button', { name: '生存服' })
    await userEvent.click(screen.getByRole('button', { name: '节点' }))
    expect(await screen.findByRole('button', { name: '主节点' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '边缘节点' })).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: '行为' }))
    expect(await screen.findByRole('button', { name: 'guard' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'follow' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'patrol' })).toBeInTheDocument()
    expect(screen.queryByText('GuardBot')).not.toBeInTheDocument()
  })

  it('展开分组才分页窥视 Bot，并可按该组筛选批量设行为', async () => {
    loginMockUser()
    renderWithProviders(<BotsPage />, { route: '/bots' })

    const survivalButton = await screen.findByRole('button', { name: '生存服' })
    const survivalCard = survivalButton.closest('div.flex-col') as HTMLElement
    await userEvent.click(survivalButton)
    expect(await screen.findByText('GuardBot')).toBeInTheDocument()
    expect(screen.getByText('FollowBot')).toBeInTheDocument()
    expect(screen.getByText('共 2 个 Bot')).toBeInTheDocument()

    await userEvent.click(within(survivalCard).getByRole('combobox'))
    await userEvent.click(await screen.findByRole('option', { name: '设为巡逻' }))

    await userEvent.click(screen.getByRole('button', { name: '行为' }))
    const patrolButton = await screen.findByRole('button', { name: 'patrol' })
    const patrolCard = patrolButton.closest('div.flex-col') as HTMLElement
    await waitFor(() => expect(within(patrolCard).getByText('3')).toBeInTheDocument())
  })
})
