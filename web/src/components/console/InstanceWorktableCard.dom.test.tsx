import { describe, it, expect, beforeEach } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { renderWithProviders } from '@/test/render'
import type { InstanceInfo } from '@/api/instances'
import { InstanceWorktableCard } from './InstanceWorktableCard'

const stoppedInst = {
  id: 42,
  name: 'survival-x',
  status: 'STOPPED',
  role: 'universal',
  type: 'minecraft_java',
  nodeId: 1,
  serverPort: 0,
} as unknown as InstanceInfo

beforeEach(() => {
  window.history.pushState({}, '', '/instances')
})

describe('InstanceWorktableCard 点击卡片打开实例（FIX-9）', () => {
  it('点击卡片主体（非按钮区）即跳转实例深链', async () => {
    const user = userEvent.setup()
    renderWithProviders(
      <InstanceWorktableCard inst={stoppedInst} nodeName="node-a" roleBadge={null} menu={null} />,
      { route: '/instances' },
    )
    // 「类型 · 节点」文本是卡片主体的非交互区——旧实现点这里无反应。
    await user.click(screen.getByText(/minecraft_java/))
    expect(window.location.pathname).toBe('/instances/42')
  })

  it('点击菜单区不会误开实例（stopPropagation）', async () => {
    const user = userEvent.setup()
    renderWithProviders(
      <InstanceWorktableCard
        inst={stoppedInst}
        nodeName="node-a"
        roleBadge={null}
        menu={<button aria-label="more-menu">⋯</button>}
      />,
      { route: '/instances' },
    )
    await user.click(screen.getByLabelText('more-menu'))
    expect(window.location.pathname).toBe('/instances')
  })

  it('传入 onOpen 时由调用方处理深链跳转', async () => {
    const opened: number[] = []
    const user = userEvent.setup()
    renderWithProviders(
      <InstanceWorktableCard
        inst={stoppedInst}
        nodeName="node-a"
        roleBadge={null}
        menu={null}
        onOpen={(id) => opened.push(id)}
      />,
      { route: '/instances' },
    )

    await user.click(screen.getByText(/minecraft_java/))

    expect(opened).toEqual([42])
    expect(window.location.pathname).toBe('/instances')
  })
})
