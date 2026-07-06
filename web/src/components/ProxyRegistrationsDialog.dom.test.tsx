import { describe, it, expect, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import ProxyRegistrationsDialog from './ProxyRegistrationsDialog'

// 对话框内 Combobox（Radix Popover）依赖 ResizeObserver，jsdom 未实现，需垫片。
if (!('ResizeObserver' in globalThis)) {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
}

describe('ProxyRegistrationsDialog（共享 Dialog）', () => {
  it('具备共享 Dialog 语义并支持 Esc 关闭', async () => {
    const onClose = vi.fn()
    const user = userEvent.setup()
    loginMockUser()
    localStorage.setItem('refreshToken', 'test-refresh-token')

    renderWithProviders(
      <ProxyRegistrationsDialog proxyId={10} proxyName="survival-proxy" onClose={onClose} />,
    )

    const dialog = await screen.findByRole('dialog', { name: '管理后端 — survival-proxy' })
    expect(dialog).toBeInTheDocument()

    await user.keyboard('{Escape}')

    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
  })
})
