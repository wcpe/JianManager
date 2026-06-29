import { describe, it, expect, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'

// 替身 Terminal：xterm 在 jsdom 下 term.open 会崩（无 canvas/WS，见 mocks/realtime/terminal-ws.ts 备注），
// 仅以标记元素证明「是否挂载了真实终端」。FIX-B 关注的是 TerminalPane 的「停机不挂终端」分流，
// 终端内部交互由 E2E 真机覆盖。
vi.mock('@/components/Terminal', () => ({
  default: ({ instanceId, readOnly }: { instanceId: string; readOnly?: boolean }) => (
    <div data-testid="terminal-mounted" data-instance={instanceId} data-readonly={String(!!readOnly)} />
  ),
}))

import TerminalPane from './TerminalPane'

/**
 * TerminalPane FIX-B 回归：停机（STOPPED）实例打开终端必须呈现「实例未运行」静态占位、
 * 不挂载终端、不发起 WS（杜绝死循环刷断连）；运行中实例放行终端。
 * seed：id=1 RUNNING、id=2 STOPPED（见 mocks/handlers/domains/instance.ts）。
 */
describe('TerminalPane（mock 假后端）', () => {
  it('停机实例：显示「实例未运行」占位、不挂载终端', async () => {
    loginMockUser()
    renderWithProviders(<TerminalPane instanceId={2} hideHeader />)

    // 占位文案出现（status 注入到 terminalNotRunning）。
    expect(await screen.findByText(/实例未运行（STOPPED）/)).toBeInTheDocument()
    expect(screen.getByText('启动实例后即可使用终端')).toBeInTheDocument()
    // 不挂载真实终端 → 不发起 WS → 不会刷「连接已断开」。
    expect(screen.queryByTestId('terminal-mounted')).not.toBeInTheDocument()
  })

  it('运行中实例：挂载终端（不显示停机占位）', async () => {
    loginMockUser()
    renderWithProviders(<TerminalPane instanceId={1} hideHeader />)

    // 状态拉到 RUNNING 后挂载终端、且非只读。
    const term = await screen.findByTestId('terminal-mounted')
    expect(term).toHaveAttribute('data-instance', '1')
    expect(term).toHaveAttribute('data-readonly', 'false')
    await waitFor(() => {
      expect(screen.queryByText(/实例未运行/)).not.toBeInTheDocument()
    })
  })
})
