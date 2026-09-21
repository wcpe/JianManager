import { describe, it, expect, beforeEach } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { mockInject } from '@jianmanager/devmock/inject'
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

  it('statusReason 非空即显失败原因，不再要求 CRASHED 前置（FR-312）', () => {
    // Worker 心跳会把 CRASHED 冲回 STOPPED——STOPPED + statusReason 也必须可见。
    renderWithProviders(
      <InstanceWorktableCard
        inst={{ ...stoppedInst, statusReason: '实例未绑定 JDK，启动委托失败' } as InstanceInfo}
        nodeName="node-a"
        roleBadge={null}
        menu={null}
      />,
      { route: '/instances' },
    )
    expect(screen.getByText('实例未绑定 JDK，启动委托失败')).toBeInTheDocument()
  })

  it('搭建中硬性禁启（FR-331）：启动按钮禁用 + tooltip 引导看任务中心 + 琥珀提示不落红', () => {
    renderWithProviders(
      <InstanceWorktableCard
        inst={{ ...stoppedInst, statusReason: '搭建中：正在下载核心（完成前请勿启动）' } as InstanceInfo}
        nodeName="node-a"
        roleBadge={null}
        menu={null}
      />,
      { route: '/instances' },
    )

    const startBtn = screen.getByRole('button', { name: '启动' })
    expect(startBtn).toBeDisabled()
    expect(startBtn).toHaveAttribute('title', expect.stringContaining('任务中心'))
    // 禁用按钮 pointer-events-none，tooltip 由外层 span 兜底承载。
    expect(startBtn.closest('span')).toHaveAttribute('title', expect.stringContaining('任务中心'))
    // 搭建中是进行时状态：琥珀行显示 reason，不再同时落红色失败行（只render一处）。
    expect(screen.getAllByText('搭建中：正在下载核心（完成前请勿启动）')).toHaveLength(1)
  })

  it('非搭建中的 STOPPED 实例启动按钮可点（FR-331 不误伤）', () => {
    renderWithProviders(
      <InstanceWorktableCard inst={stoppedInst} nodeName="node-a" roleBadge={null} menu={null} />,
      { route: '/instances' },
    )
    expect(screen.getByRole('button', { name: '启动' })).toBeEnabled()
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

/** 运行态可用性渲染（FR-446/447）：缺测显「不可用」，有值显真实值，不再以 0 冒充「0 人在线」。 */
describe('InstanceWorktableCard 卡内在线数/TPS 可用性', () => {
  const runningInst = { ...stoppedInst, status: 'RUNNING' } as InstanceInfo

  beforeEach(() => {
    window.history.pushState({}, '', '/instances')
  })

  function injectMetrics(body: Record<string, unknown>) {
    mockInject('get', '/instances/:id/metrics', { kind: 'status', status: 200, body })
  }

  it('三源皆无：在线数与 TPS 均显「不可用」，不出现 0', async () => {
    injectMetrics({
      tps: 0,
      onlinePlayers: 0,
      memoryMb: 1024,
      msptMillis: 0,
      threads: 0,
      cpuPercent: 12,
      heapMaxMb: 2048,
      uptimeSeconds: 60,
      worlds: [],
      probeAvailable: false,
      playersAvailable: false,
      slpAvailable: false,
      queryAvailable: false,
      sourceMask: 0,
    })
    renderWithProviders(<InstanceWorktableCard inst={runningInst} nodeName="node-a" roleBadge={null} menu={null} />, { route: '/instances' })

    // 玩家 + TPS 各一处「不可用」。
    expect(await screen.findAllByText('不可用')).toHaveLength(2)
  })

  it('仅直探（SLP）有在线数：在线数显真实值，TPS 仍「不可用」', async () => {
    injectMetrics({
      tps: 0,
      onlinePlayers: 7,
      memoryMb: 1024,
      msptMillis: 0,
      threads: 0,
      cpuPercent: 12,
      heapMaxMb: 2048,
      uptimeSeconds: 60,
      worlds: [],
      probeAvailable: false,
      playersAvailable: true,
      slpAvailable: true,
      queryAvailable: false,
      sourceMask: 2,
    })
    renderWithProviders(<InstanceWorktableCard inst={runningInst} nodeName="node-a" roleBadge={null} menu={null} />, { route: '/instances' })

    expect(await screen.findByText('7')).toBeInTheDocument()
    // TPS 仅探针可得 → 仍「不可用」（仅一处）。
    expect(screen.getAllByText('不可用')).toHaveLength(1)
  })
})
