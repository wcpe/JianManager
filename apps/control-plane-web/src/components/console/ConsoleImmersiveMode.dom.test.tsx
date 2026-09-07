import { beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, screen } from '@testing-library/react'

import { renderWithProviders } from '@/test/render'
import { terminalSessionManager } from '@/lib/terminal-session-manager'
import ConsoleImmersiveMode from './ConsoleImmersiveMode'

vi.mock('@/api/instances', () => ({
  useInstance: (id: number) => ({
    data: { id, name: id === 1 ? 'survival-01' : 'lobby', status: 'RUNNING' },
  }),
  useInstanceSearch: () => ({
    data: {
      items: [
        { id: 1, name: 'survival-01', status: 'RUNNING' },
        { id: 2, name: 'lobby', status: 'RUNNING' },
      ],
    },
    isFetching: false,
  }),
}))

vi.mock('@/api/metrics', () => ({
  useInstanceMetrics: () => ({ data: { probeAvailable: false } }),
}))

function press(key: string, init: KeyboardEventInit = {}) {
  fireEvent.keyDown(window, { key, bubbles: true, ...init })
}

function renderImmersive(onExit = vi.fn()) {
  renderWithProviders(
    <ConsoleImmersiveMode
      initialInstanceId={1}
      onExit={onExit}
      renderPane={({ instanceId, focused, onFocus }) => (
        <div>
          {/* 输入类元素：验证 ESC 分层——输入框内按 ESC 不得退出沉浸工作台。 */}
          <input data-testid="pane-input" aria-label="pane input" />
          <button type="button" onClick={onFocus} data-focused={focused ? 'true' : undefined}>
            pane-{instanceId}
          </button>
        </div>
      )}
    />,
  )
  return onExit
}

describe('FR-420/421 可视沉浸工作台', () => {
  beforeEach(() => {
    // 布局持久化会跨测试残留快照，统一清掉保证每个用例从单 pane 起步。
    sessionStorage.clear()
    terminalSessionManager.disposeAll()
  })

  it('只保留 F11/Esc 退出，不注册 Ctrl+B tmux 前缀', () => {
    const onExit = renderImmersive()

    press('b', { ctrlKey: true })
    press('?')
    expect(screen.queryByTestId('console-immersive-prefix')).not.toBeInTheDocument()
    expect(screen.queryByText('沉浸模式快捷键')).not.toBeInTheDocument()

    press('F11')
    expect(onExit).toHaveBeenCalledTimes(1)
  })

  it('ESC 分层：输入类元素内按 ESC 不退出；裸 ESC 双击才退出', () => {
    const onExit = renderImmersive()

    // 焦点在 input 里：ESC 归命令栏补全 / Ctrl+R 反向搜索等内层语义，绝不退出。
    fireEvent.keyDown(screen.getByTestId('pane-input'), { key: 'Escape', bubbles: true })
    expect(onExit).not.toHaveBeenCalled()

    // 裸 ESC 第一次：不退出（内层清选区/关菜单等照常跑）。
    press('Escape')
    expect(onExit).not.toHaveBeenCalled()

    // 窗口期内第二次：退出。
    press('Escape')
    expect(onExit).toHaveBeenCalledTimes(1)
  })

  it('通过可见的左右分屏按钮选择实例并创建第二个 pane', async () => {
    renderImmersive()

    fireEvent.click(screen.getByRole('button', { name: '左右分屏' }))
    expect(await screen.findByRole('dialog', { name: '选择要显示的实例' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /lobby/ }))

    expect(screen.getAllByRole('button', { name: /pane-/ })).toHaveLength(2)
    expect(screen.getByText('pane-2')).toBeInTheDocument()
  })

  it('工作台布局持久化：分屏后重进恢复 pane 数量与实例（id 重编防撞车）', async () => {
    renderImmersive()
    fireEvent.click(screen.getByRole('button', { name: '左右分屏' }))
    fireEvent.click(await screen.findByRole('button', { name: /lobby/ }))
    expect(screen.getAllByRole('button', { name: /pane-/ })).toHaveLength(2)
    cleanup()

    // 重新进入：从 sessionStorage 快照恢复，两个 pane 原样回来。
    renderImmersive()
    expect(await screen.findAllByRole('button', { name: /pane-/ })).toHaveLength(2)
    expect(screen.getByText('pane-2')).toBeInTheDocument()
  })

  it('pane 的可见操作可切换实例、最大化并恢复布局', async () => {
    renderImmersive()

    fireEvent.click(screen.getByRole('button', { name: '切换实例' }))
    expect(await screen.findByRole('dialog', { name: '选择要显示的实例' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /lobby/ }))
    expect(screen.getByText('pane-2')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: '放大面板' }))
    expect(screen.getByRole('button', { name: '恢复布局' })).toBeInTheDocument()
  })

  it('拾取器键盘化：↑↓ 移高亮、Enter 选中实例', async () => {
    renderImmersive()

    fireEvent.click(screen.getByRole('button', { name: '左右分屏' }))
    const input = await screen.findByLabelText('搜索实例')
    // 初始高亮在第一项（survival-01）；↓ 移到 lobby，Enter 选中。
    fireEvent.keyDown(input, { key: 'ArrowDown' })
    fireEvent.keyDown(input, { key: 'Enter' })

    expect(await screen.findByText('pane-2')).toBeInTheDocument()
  })
})
