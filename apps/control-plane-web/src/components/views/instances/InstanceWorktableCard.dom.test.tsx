import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import { InstanceWorktableCard } from '@/components/views/instances/InstanceWorktableCard'

/**
 * FR-136 实例工作台卡 · 受控视图测（ADR-097 b 范式）。
 *
 * 与迁移前的差异（**有意的取舍，勿当缺陷「修掉」**）：
 * 1. 「点卡片跳转深链」改为断言 `onOpen(42)`——路由由外壳注入（列表页可自定义跳转），
 *    视图不再碰 router，故测试也不再需要 route 与 `window.location` 断言。
 * 2. 指标经 props 注入，不再走 mockInject 打桩后端；因此没有异步等待，断言同步完成。
 * 3. 搭建中不再靠「statusReason 文案推断」，而是外壳注入的 `provisioning` 布尔——
 *    这正是把判定权上移的意义：卡内不再猜。
 *
 * 文案取自 zh.json 原值（`metrics.unavailable` 与两个启动相关文案都参与断言）。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        metrics: { unavailable: '不可用' },
        nodes: { cpu: 'CPU', memory: '内存' },
        instances: {
          start: '启动',
          stop: '停止',
          restart: '重启',
          stopped: '已停止',
          running: '运行中',
          provisioningBlocked: '搭建/导入/克隆进行中（工作目录尚未就绪），暂不可启动；进度见任务中心',
        },
      },
    },
  },
  interpolation: { escapeValue: false },
})

const stoppedInst = {
  id: 42,
  name: 'survival-x',
  status: 'STOPPED',
  type: 'minecraft_java',
  serverPort: 0,
}

type Props = Parameters<typeof InstanceWorktableCard>[0]

function renderCard(props: Partial<Props> = {}) {
  const handlers = {
    onStart: vi.fn(),
    onStop: vi.fn(),
    onRestart: vi.fn(),
    ...props,
  }
  function Wrapper({ children }: { children: ReactNode }) {
    return <I18nextProvider i18n={testI18n}>{children}</I18nextProvider>
  }
  const result = render(
    <InstanceWorktableCard inst={stoppedInst} nodeName="node-a" roleBadge={null} menu={null} {...handlers} />,
    { wrapper: Wrapper },
  )
  return { ...handlers, ...result }
}

describe('InstanceWorktableCard 点击卡片打开实例（FIX-9 · ADR-097 b 范式）', () => {
  it('点击卡片主体（非按钮区）上报打开意图', async () => {
    const user = userEvent.setup()
    const onOpen = vi.fn()
    renderCard({ onOpen })
    // 「类型 · 节点」文本是卡片主体的非交互区——旧实现点这里无反应。
    await user.click(screen.getByText(/minecraft_java/))
    expect(onOpen).toHaveBeenCalledWith(42)
  })

  it('点击菜单区不会误开实例（stopPropagation）', async () => {
    const user = userEvent.setup()
    const onOpen = vi.fn()
    renderCard({ onOpen, menu: <button aria-label="more-menu">⋯</button> })
    await user.click(screen.getByLabelText('more-menu'))
    expect(onOpen).not.toHaveBeenCalled()
  })

  it('statusReason 非空即显失败原因，不再要求 CRASHED 前置（FR-312）', () => {
    // Worker 心跳会把 CRASHED 冲回 STOPPED——STOPPED + statusReason 也必须可见。
    renderCard({ inst: { ...stoppedInst, statusReason: '实例未绑定 JDK，启动委托失败' } })
    expect(screen.getByText('实例未绑定 JDK，启动委托失败')).toBeInTheDocument()
  })

  it('搭建中硬性禁启（FR-331）：启动按钮禁用 + tooltip 引导看任务中心 + 琥珀提示不落红', () => {
    renderCard({
      inst: { ...stoppedInst, statusReason: '搭建中：正在下载核心（完成前请勿启动）' },
      provisioning: true,
    })

    const startBtn = screen.getByRole('button', { name: '启动' })
    expect(startBtn).toBeDisabled()
    expect(startBtn).toHaveAttribute('title', expect.stringContaining('任务中心'))
    // 禁用按钮 pointer-events-none，tooltip 由外层 span 兜底承载。
    expect(startBtn.closest('span')).toHaveAttribute('title', expect.stringContaining('任务中心'))
    // 搭建中是进行时状态：琥珀行显示 reason，不再同时落红色失败行（只 render 一处）。
    expect(screen.getAllByText('搭建中：正在下载核心（完成前请勿启动）')).toHaveLength(1)
  })

  it('非搭建中的 STOPPED 实例启动按钮可点（FR-331 不误伤）', async () => {
    const user = userEvent.setup()
    const { onStart } = renderCard()
    const startBtn = screen.getByRole('button', { name: '启动' })
    expect(startBtn).toBeEnabled()
    await user.click(startBtn)
    expect(onStart).toHaveBeenCalledTimes(1)
  })

  it('启动在途时本卡按钮禁用', () => {
    renderCard({ starting: true })
    expect(screen.getByRole('button', { name: '启动' })).toBeDisabled()
  })
})

/** 运行态可用性渲染（FR-446/447）：缺测显「不可用」，有值显真实值，不再以 0 冒充「0 人在线」。 */
describe('InstanceWorktableCard 卡内在线数/TPS 可用性', () => {
  const runningInst = { ...stoppedInst, status: 'RUNNING' }

  it('三源皆无：在线数与 TPS 均显「不可用」，不出现 0', () => {
    renderCard({
      inst: runningInst,
      metrics: { tps: 0, onlinePlayers: 0, memoryMb: 1024, cpuPercent: 12, heapMaxMb: 2048, probeAvailable: false, playersAvailable: false },
    })

    // 玩家 + TPS 各一处「不可用」。
    expect(screen.getAllByText('不可用')).toHaveLength(2)
  })

  it('仅直探（SLP）有在线数：在线数显真实值，TPS 仍「不可用」', () => {
    renderCard({
      inst: runningInst,
      metrics: { tps: 0, onlinePlayers: 7, memoryMb: 1024, cpuPercent: 12, heapMaxMb: 2048, probeAvailable: false, playersAvailable: true },
    })

    expect(screen.getByText('7')).toBeInTheDocument()
    // TPS 仅探针可得 → 仍「不可用」（仅一处）。
    expect(screen.getAllByText('不可用')).toHaveLength(1)
  })

  it('停机态保留「--」（明确的不适用），不显示「不可用」', () => {
    renderCard()
    expect(screen.queryByText('不可用')).not.toBeInTheDocument()
    expect(screen.getAllByText('--').length).toBeGreaterThanOrEqual(2)
  })
})
