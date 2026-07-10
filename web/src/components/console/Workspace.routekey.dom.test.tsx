import { describe, it, expect, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import Workspace from './Workspace'

// 用带本地状态 + 同页 query 切换能力的桩替换发布页，专测「路由过渡容器 key」是否会因同页
// query 变化误 remount 路由子树。桩故意最小化：一个本地计数器（模拟 drafts 等页内状态）+
// 一个把 ?step 切到 configure 的按钮（模拟发布向导 setStep 的 setSearchParams(replace)）。
vi.mock('@/pages/ClientPublishPage', async () => {
  const { useState } = await import('react')
  const { useSearchParams } = await import('react-router')
  return {
    default: function PublishStub() {
      const [n, setN] = useState(0)
      const [, setSp] = useSearchParams()
      return (
        <div>
          <span data-testid="stub-count">{n}</span>
          <button type="button" onClick={() => setN((v) => v + 1)}>
            stub-inc
          </button>
          <button
            type="button"
            onClick={() => setSp((p) => { p.set('step', 'configure'); return p }, { replace: true })}
          >
            stub-next
          </button>
        </div>
      )
    },
  }
})

/**
 * 路由过渡容器 key 回归（FR-191/250 发布向导）：同一路由页内仅 query 变化（如 `?step=` 步骤切换）
 * 不得 remount 路由子树，否则页内本地状态（发布向导 drafts）被清空、向导过不了第一步。
 * 根因：Workspace 的 route-transition 容器 key 一度含 location.search。
 */
describe('Workspace 路由过渡容器：同页 query 变化不 remount 页内状态', () => {
  it('切换 ?step 后本地状态保留（不 remount）', async () => {
    const user = userEvent.setup()
    renderWithProviders(<Workspace />, { route: '/client-channels/ota-e2e/publish' })

    // 页面就绪后自增本地计数器到 1。
    await user.click(await screen.findByRole('button', { name: 'stub-inc' }))
    expect(screen.getByTestId('stub-count')).toHaveTextContent('1')

    // 触发同页 query 变化（等价发布向导「下一步」的 setSearchParams(replace)）。
    await user.click(screen.getByRole('button', { name: 'stub-next' }))

    // query 已变，但同一路由页不应 remount：本地状态必须仍在。
    expect(window.location.search).toContain('step=configure')
    expect(screen.getByTestId('stub-count')).toHaveTextContent('1')
  })
})
