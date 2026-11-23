import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import { InstanceWorktableCard } from '@/components/views/instances/InstanceWorktableCard'

/** 
 *实例工作台卡 · 受控视图测。
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
  // 身份徽章需要 role 来渲染角色徽标（与表格身份列同源）。
  role: 'backend',
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

describe('InstanceWorktableCard 点击卡片打开实例（FIX-9）', () => {
  it('点击卡片主体（非按钮区）上报打开意图', async () => {
    const user = userEvent.setup()
    const onOpen = vi.fn()
    renderCard({ onOpen })
    /* 
      点卡片主体（名称所在的非交互区）应打开实例。
      注意：卡片现在显示的是**本地化类型徽章**（「插件服」），不再是原始 type 值
      （`minecraft_java`）——那是本轮「身份徽章与表格对齐」的结果，测试也一并跟上。
    */
    await user.click(screen.getByText('survival-x'))
    expect(onOpen).toHaveBeenCalledWith(42)
  })

  it('点击菜单区不会误开实例（stopPropagation）', async () => {
    const user = userEvent.setup()
    const onOpen = vi.fn()
    renderCard({ onOpen, menu: <button aria-label="more-menu">⋯</button> })
    await user.click(screen.getByLabelText('more-menu'))
    expect(onOpen).not.toHaveBeenCalled()
  })

  it('statusReason 非空即显失败原因，不再要求 CRASHED 前置', () => {
    // Worker 心跳会把 CRASHED 冲回 STOPPED——STOPPED + statusReason 也必须可见。
    renderCard({ inst: { ...stoppedInst, statusReason: '实例未绑定 JDK，启动委托失败' } })
    expect(screen.getByText('实例未绑定 JDK，启动委托失败')).toBeInTheDocument()
  })

  it('搭建中硬性禁启：启动按钮禁用 + tooltip 引导看任务中心 + 琥珀提示不落红', () => {
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

  it('非搭建中的 STOPPED 实例启动按钮可点（不误伤）', async () => {
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

describe('InstanceWorktableCard 生命周期状态', () => {
  it('HTTP 启动提交中立刻显示进行中说明并禁用重复启动', () => {
    renderCard({ starting: true })
    expect(screen.getByText('启动请求提交中')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '启动' })).toBeDisabled()
    expect(screen.getByTestId('instance-lifecycle-status')).toHaveAttribute('aria-busy', 'true')
  })

  it('STARTING 没有原因仍提供等待节点确认说明', () => {
    renderCard({ inst: { ...stoppedInst, status: 'STARTING' } })
    expect(screen.getByText('正在启动，等待节点状态确认')).toBeInTheDocument()
    expect(screen.queryByText(/\d+%|READY/)).not.toBeInTheDocument()
  })

  it('待确认原因使用琥珀且完整原文可通过 title 读取', () => {
    const statusReason = '操作结果待确认，状态查询/心跳收敛: RPC canceled，等待后续观测'
    renderCard({ inst: { ...stoppedInst, status: 'STARTING', statusReason } })
    expect(screen.getByText('操作结果待确认')).toBeInTheDocument()
    expect(screen.getByTestId('instance-lifecycle-status')).toHaveAttribute('title', expect.stringContaining(statusReason))
    expect(screen.getByTestId('instance-lifecycle-detail')).toHaveClass('text-status-warning')
  })

  it('明确启动失败保留红色完整原因', () => {
    const statusReason = '启动失败：上一代进程未在预算内退出'
    renderCard({ inst: { ...stoppedInst, status: 'CRASHED', statusReason } })
    expect(screen.getByTestId('instance-lifecycle-detail')).toHaveClass('text-status-danger')
    expect(screen.getByTestId('instance-lifecycle-status')).toHaveAttribute('title', expect.stringContaining(statusReason))
  })
})

/** 运行态可用性渲染（447）：缺测显「不可用」，有值显真实值，不再以 0 冒充「0 人在线」。 */
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

describe('InstanceWorktableCard 指标与连接信息', () => {
  const runningInst = { ...stoppedInst, status: 'RUNNING', serverPort: 25565 }

  it('CPU 未取到时不冒充 0%，真实零值仍显示', () => {
    const { rerender } = renderCard({ inst: runningInst })
    expect(screen.queryByText('0%')).not.toBeInTheDocument()
    rerender(
      <I18nextProvider i18n={testI18n}>
        <InstanceWorktableCard
          inst={runningInst}
          nodeName="node-a"
          roleBadge={null}
          menu={null}
          onStart={vi.fn()}
          onStop={vi.fn()}
          onRestart={vi.fn()}
          metrics={{ cpuPercent: 0, memoryMb: 0 }}
        />
      </I18nextProvider>,
    )
    expect(screen.getByText('0%')).toBeInTheDocument()
    expect(screen.getByText('0 MiB')).toBeInTheDocument()
  })

  it('显示真实资源读数、内存上限、在线人数/上限与 TPS', () => {
    renderCard({
      inst: runningInst,
      nodeHost: '192.0.2.102',
      metrics: {
        cpuPercent: 12.5, memoryMb: 1024, heapMaxMb: 4096,
        onlinePlayers: 7, maxPlayers: 20, tps: 19.8,
        playersAvailable: true, maxPlayersAvailable: true, probeAvailable: true,
      },
    })
    expect(screen.getByText('13%')).toBeInTheDocument()
    expect(screen.getByText('1.0 GiB / 4.0 GiB')).toBeInTheDocument()
    expect(screen.getByText('在线人数')).toBeInTheDocument()
    expect(screen.getByText('7 / 20')).toBeInTheDocument()
    expect(screen.getByText('TPS')).toBeInTheDocument()
    expect(screen.getByText('19.8')).toBeInTheDocument()
    expect(screen.getByText('192.0.2.102:25565')).toBeInTheDocument()
    expect(screen.getByText('node-a')).toBeInTheDocument()
  })

  it('不可用或非有限指标不显示伪造数字，并说明接入来源', () => {
    renderCard({
      inst: runningInst,
      metrics: {
        cpuPercent: Number.NaN, memoryMb: Number.POSITIVE_INFINITY,
        onlinePlayers: Number.NaN, tps: Number.POSITIVE_INFINITY,
        playersAvailable: true, probeAvailable: true,
      },
    })
    expect(screen.queryByText(/NaN|Infinity|0%/)).not.toBeInTheDocument()
    expect(screen.getByTitle('在线人数需要 ServerProbe、SLP 或 Query 上报')).toBeInTheDocument()
    expect(screen.getByTitle('TPS 需要 ServerProbe 上报')).toBeInTheDocument()
  })

  it('未知玩家上限不拼成 /0，真实 0 人在线与 0 TPS 可见', () => {
    renderCard({
      inst: runningInst,
      metrics: { onlinePlayers: 0, maxPlayers: 0, tps: 0, playersAvailable: true, maxPlayersAvailable: false, probeAvailable: true },
    })
    expect(screen.getByText('0')).toBeInTheDocument()
    expect(screen.getByText('0.0')).toBeInTheDocument()
    expect(screen.queryByText('0 / 0')).not.toBeInTheDocument()
  })

  it('IPv6 使用方括号，缺失 IP 或端口保留明确占位', () => {
    const { unmount } = renderCard({ inst: runningInst, nodeHost: '2001:db8::1' })
    expect(screen.getByText('[2001:db8::1]:25565')).toBeInTheDocument()
    unmount()
    renderCard({ nodeHost: '192.0.2.102' })
    expect(screen.getByText('192.0.2.102 · 端口未分配')).toBeInTheDocument()
  })

  it('没有 IP 时不把节点名称当作地址，也不拼 undefined:port', () => {
    renderCard({ inst: runningInst })
    expect(screen.getByText('IP 未知 · 端口 25565')).toBeInTheDocument()
    expect(screen.queryByText(/undefined:|node-a:25565/)).not.toBeInTheDocument()
  })

  it('正常卡片不保留空白故障提示槽，角色只展示一处', () => {
    renderCard({ roleBadge: <span>重复角色徽标</span> })
    expect(screen.queryByTestId('instance-card-extra')).not.toBeInTheDocument()
    expect(screen.queryByText('重复角色徽标')).not.toBeInTheDocument()
  })

  it('代理卡片不显示 TPS', () => {
    renderCard({ inst: runningInst, isProxy: true, metrics: { tps: 20, probeAvailable: true } })
    expect(screen.queryByText('TPS')).not.toBeInTheDocument()
  })
})
