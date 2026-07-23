import { describe, it, expect, beforeEach } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import ExplorerTabHost from './ExplorerTabHost'
import { resetTabIdSeq } from './explorer-tabs'

/**
 * FR-376：多标签宿主 DOM——新标签、切换、弹出/收回。
 * ResourceExplorer 走 mock 假后端，需 loginMockUser。
 * 注意：「新标签」不可用子串正则，会误匹配「在浏览器新标签打开」。
 */
describe('ExplorerTabHost（FR-376）', () => {
  beforeEach(() => {
    resetTabIdSeq(0)
    loginMockUser()
  })

  it('默认一签，可新建第二签并切换', async () => {
    const user = userEvent.setup()
    renderWithProviders(<ExplorerTabHost instanceId={1} />)

    expect(await screen.findByTestId('explorer-tab-host')).toBeInTheDocument()
    expect(await screen.findByText('server.properties')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '新标签' }))
    const host = screen.getByTestId('explorer-tab-host')
    const tabButtons = within(host).getAllByRole('button', { name: /^\// })
    expect(tabButtons.length).toBeGreaterThanOrEqual(2)
  })

  it('弹出后 data-floated=1，收回后回停靠', async () => {
    const user = userEvent.setup()
    renderWithProviders(<ExplorerTabHost instanceId={1} />)
    await screen.findByText('server.properties')

    await user.click(screen.getByRole('button', { name: '弹出浮动窗' }))
    const floated = await screen.findByTestId(/explorer-float-/)
    expect(floated).toHaveAttribute('data-floated', '1')

    await user.click(screen.getByRole('button', { name: '收回' }))
    await waitFor(() => {
      expect(screen.queryByTestId(/explorer-float-/)).not.toBeInTheDocument()
    })
    expect(screen.getByTestId(/explorer-pane-/)).toHaveAttribute('data-floated', '0')
  })

  it('达标签上限 toast 不崩', async () => {
    const user = userEvent.setup()
    renderWithProviders(<ExplorerTabHost instanceId={1} />)
    await screen.findByText('server.properties')
    const newBtn = screen.getByRole('button', { name: '新标签' })
    for (let i = 0; i < 8; i++) {
      await user.click(newBtn)
    }
    expect(screen.getByTestId('explorer-tab-host')).toBeInTheDocument()
  })
})
