import { describe, it, expect, beforeEach } from 'vitest'
import { useState } from 'react'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import ResourceExplorer from './ResourceExplorer'
import { resetClipboardBusForTests } from './explorer-clipboard-bus'

/**
 * FR-377：同页双 ResourceExplorer 剪贴板互通（主区↔浮动等价）。
 */
function DualExplorers() {
  const [ready] = useState(true)
  if (!ready) return null
  return (
    <div>
      <div data-testid="pane-a">
        <ResourceExplorer instanceId={1} draftKey="pane-a" />
      </div>
      <div data-testid="pane-b">
        <ResourceExplorer instanceId={1} draftKey="pane-b" />
      </div>
    </div>
  )
}

describe('跨 Explorer 剪贴板总线（FR-377）', () => {
  beforeEach(() => {
    resetClipboardBusForTests()
    loginMockUser()
  })

  it('A 复制后 B 粘贴按钮可用并可执行', async () => {
    const user = userEvent.setup()
    renderWithProviders(<DualExplorers />)

    const paneA = await screen.findByTestId('pane-a')
    await within(paneA).findByText('server.properties')

    // 在 A 勾选 server.properties 并复制
    await user.click(within(paneA).getByRole('checkbox', { name: 'server.properties' }))
    const serverRow = within(paneA).getByText('server.properties').closest('li') as HTMLElement
    await user.pointer({ keys: '[MouseRight]', target: serverRow })
    await user.click(await screen.findByRole('menuitem', { name: /复制/ }))

    // B 的粘贴应可用（总线镜像）。FR-422 后粘贴收在 B 自己工具栏的「更多操作」下拉里，
    // 故经该下拉断言可用性——下拉内容走 portal，用 role=menu 定位（同时只会开一个）。
    const paneB = screen.getByTestId('pane-b')
    await user.click(within(paneB).getByRole('button', { name: '更多操作' }))
    const pasteB = within(await screen.findByRole('menu')).getByRole('menuitem', { name: /粘贴/ })
    await waitFor(() => expect(pasteB).not.toHaveAttribute('aria-disabled', 'true'))
    await user.keyboard('{Escape}')

    // 导航 B 到 plugins 再粘贴，避免 same-dir 跳过
    for (const el of within(paneB).getAllByText('plugins')) {
      await user.dblClick(el)
    }
    await within(paneB).findByText('config.yml')
    await user.click(within(paneB).getByRole('button', { name: '更多操作' }))
    await user.click(within(await screen.findByRole('menu')).getByRole('menuitem', { name: /粘贴/ }))
    // 粘贴成功后列表应出现 server.properties（mock rename/copy）
    await waitFor(() => {
      expect(within(paneB).queryAllByText('server.properties').length).toBeGreaterThan(0)
    })
  })
})
