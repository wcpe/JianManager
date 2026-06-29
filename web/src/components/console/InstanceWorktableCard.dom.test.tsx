import { describe, it, expect, beforeEach } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { renderWithProviders } from '@/test/render'
import { useConsoleStore } from '@/stores/console'
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
  useConsoleStore.getState().closeInstance()
})

describe('InstanceWorktableCard 点击卡片打开实例（FIX-9）', () => {
  it('点击卡片主体（非按钮区）即打开实例工作区', async () => {
    const user = userEvent.setup()
    renderWithProviders(
      <InstanceWorktableCard inst={stoppedInst} nodeName="node-a" roleBadge={null} menu={null} />,
    )
    expect(useConsoleStore.getState().openInstanceId).toBeNull()
    // 「类型 · 节点」文本是卡片主体的非交互区——旧实现点这里无反应。
    await user.click(screen.getByText(/minecraft_java/))
    expect(useConsoleStore.getState().openInstanceId).toBe(42)
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
    )
    await user.click(screen.getByLabelText('more-menu'))
    expect(useConsoleStore.getState().openInstanceId).toBeNull()
  })
})
