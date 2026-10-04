import { afterEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient } from '@tanstack/react-query'
import { db } from '@jianmanager/devmock/db'
import {
  MOCK_DATA_SCALE_STORAGE_KEY,
  MOCK_DEFAULT_DATA_SCALE,
  MOCK_DEFAULT_POLL_INTERVAL,
  MOCK_POLL_FOLLOW_ORIGINAL,
  MOCK_LATENCY_STORAGE_KEY,
  MOCK_POLL_INTERVAL_STORAGE_KEY,
  getMockDataScale,
  getMockLatency,
  getMockPollInterval,
  resetMockData,
  setMockDataScale,
  setMockLatency,
  setMockPollInterval,
  setMockRequestLogEnabled,
} from '@jianmanager/devmock/runtime-control'
import { MOCK_PANEL_COLLAPSED_KEY, MockControlPanel } from './MockControlPanel'
import { installMockPollingControl, isPollingQueryExcluded } from './mock-polling'

/**
 * 「请求日志」开关的落地动作是重启 MSW worker（jsdom 里没有 Service Worker），
 * 所以替身只负责记录调用；开关状态本身仍走真实的运行时配置 API，断言不受替身影响。
 */
vi.mock('@jianmanager/devmock/browser', async () => {
  const runtime = await import('@jianmanager/devmock/runtime-control')
  return {
    setMockRequestLog: vi.fn(async (enabled: boolean) => {
      runtime.setMockRequestLogEnabled(enabled)
    }),
  }
})

/** 装一套轮询控制（面板的「作用范围」芯片需要先有「本来在轮询」的查询）。 */
function installPollingWith(labels: string[] = ['instances/search']): void {
  const client = new QueryClient()
  installMockPollingControl(client, { exclude: [] })
  for (const label of labels) {
    client.getQueryCache().build(client, {
      queryKey: label.split('/'),
      refetchInterval: 30_000,
    } as never)
  }
}

/**
 * Mock 调试悬浮面板（FR-496 阶段 6 补丁）。
 * 断言分两层：① 交互与持久化（DOM / localStorage）；② 真的调到了假后端的运行时配置 API ——
 * 用 `getMockLatency()` 与实例集合行数验证，否则「点了按钮改了本地 state」也能骗过测试。
 */

/** 假后端实例集合当前行数：换数据量档位会按新规模重播种，行数是「确实生效」的硬证据。 */
function instanceRowCount(): number {
  return db<{ id: number }>('instances').list().length
}

afterEach(() => {
  // 面板改的是 devmock 的模块级运行时状态，不还原会污染同文件后续用例
  setMockLatency(0)
  setMockDataScale(MOCK_DEFAULT_DATA_SCALE)
  setMockPollInterval(MOCK_DEFAULT_POLL_INTERVAL)
  setMockRequestLogEnabled(false)
})

describe('MockControlPanel', () => {
  it('默认展开，显示当前生效的延迟与数据量档位', () => {
    render(<MockControlPanel />)

    expect(screen.getByTestId('mock-control-panel')).toBeInTheDocument()
    expect(screen.getByTestId('mock-latency-value')).toHaveTextContent('0 ms')
    // 默认档 = 改造前规模，不调档位时 mock 行为与既有基线一致
    expect(screen.getByTestId('mock-scale-value')).toHaveTextContent('大 · 约 1200 实例 / 12000 日志')
    expect(screen.getByRole('button', { name: '收起为悬浮球' })).toBeInTheDocument()
  })

  it('点延迟档位即时写入假后端运行时配置并持久化', async () => {
    const user = userEvent.setup()
    render(<MockControlPanel />)

    await user.click(screen.getByTestId('mock-latency-preset-800'))

    expect(getMockLatency()).toBe(800)
    expect(screen.getByTestId('mock-latency-value')).toHaveTextContent('800 ms')
    expect(window.localStorage.getItem(MOCK_LATENCY_STORAGE_KEY)).toBe('800')
    expect(screen.getByTestId('mock-latency-preset-800')).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByTestId('mock-latency-preset-0')).toHaveAttribute('aria-pressed', 'false')
  })

  it('滑杆可自由设置延迟（不受预设档位限制）', () => {
    render(<MockControlPanel />)

    fireEvent.change(screen.getByTestId('mock-latency-range'), { target: { value: '1750' } })

    expect(getMockLatency()).toBe(1750)
    expect(screen.getByTestId('mock-latency-value')).toHaveTextContent('1750 ms')
  })

  it('切换数据量档位后按新规模重播种假后端', async () => {
    const user = userEvent.setup()
    render(<MockControlPanel />)

    expect(instanceRowCount()).toBe(1200)

    await user.click(screen.getByTestId('mock-scale-small'))

    expect(getMockDataScale()).toBe('small')
    expect(window.localStorage.getItem(MOCK_DATA_SCALE_STORAGE_KEY)).toBe('small')
    // 60 = 小档的 instanceCount；行数变了才说明 seedFn 读的是运行时档位而非 import 时的常量
    expect(instanceRowCount()).toBe(60)
    expect(screen.getByTestId('mock-scale-value')).toHaveTextContent('小 · 约 60 实例 / 600 日志')
    // 列表已持有旧数据，提示需要刷新才看得见新规模
    expect(screen.getByTestId('mock-reload')).toBeInTheDocument()
  })

  it('重复点当前档位不重置数据（避免误清手工造的数据）', async () => {
    const user = userEvent.setup()
    // setMockDataScale 只改配置、不重播种（这正是它的语义），故此处显式重播种一次
    setMockDataScale('small')
    resetMockData()
    render(<MockControlPanel />)

    expect(instanceRowCount()).toBe(60)
    db<{ id: number }>('instances').insert({ id: 999999 })

    await user.click(screen.getByTestId('mock-scale-small'))

    expect(instanceRowCount()).toBe(61)
    expect(screen.queryByTestId('mock-reload')).not.toBeInTheDocument()
  })

  it('收起为小球并把收起状态持久化到 localStorage，刷新后仍收起', async () => {
    const user = userEvent.setup()
    const first = render(<MockControlPanel />)

    await user.click(screen.getByTestId('mock-collapse'))

    expect(screen.queryByTestId('mock-control-panel')).not.toBeInTheDocument()
    expect(screen.getByTestId('mock-control-ball')).toBeInTheDocument()
    expect(window.localStorage.getItem(MOCK_PANEL_COLLAPSED_KEY)).toBe('1')

    // 模拟刷新：重新挂载后仍应是收起态，而展开后的设置值也还在
    first.unmount()
    render(<MockControlPanel />)
    expect(screen.getByTestId('mock-control-ball')).toBeInTheDocument()
    expect(screen.queryByTestId('mock-control-panel')).not.toBeInTheDocument()

    await user.click(screen.getByTestId('mock-control-ball'))
    expect(screen.getByTestId('mock-control-panel')).toBeInTheDocument()
    expect(screen.getByTestId('mock-latency-value')).toHaveTextContent('0 ms')
    expect(window.localStorage.getItem(MOCK_PANEL_COLLAPSED_KEY)).toBe('0')
  })

  it('刷新后按 localStorage 还原收起态与已保存的档位', () => {
    window.localStorage.setItem(MOCK_PANEL_COLLAPSED_KEY, '1')
    window.localStorage.setItem(MOCK_LATENCY_STORAGE_KEY, '300')
    setMockLatency(300)

    render(<MockControlPanel />)

    expect(screen.getByTestId('mock-control-ball')).toBeInTheDocument()
  })

  it('一键重置回默认值（延迟 0 / 数据量默认档）', async () => {
    const user = userEvent.setup()
    render(<MockControlPanel />)

    await user.click(screen.getByTestId('mock-latency-preset-800'))
    await user.click(screen.getByTestId('mock-scale-small'))
    await user.click(screen.getByTestId('mock-reset'))

    expect(getMockLatency()).toBe(0)
    expect(getMockDataScale()).toBe(MOCK_DEFAULT_DATA_SCALE)
    expect(instanceRowCount()).toBe(1200)
    expect(screen.getByTestId('mock-latency-value')).toHaveTextContent('0 ms')
    expect(screen.getByTestId('mock-scale-value')).toHaveTextContent('大 · 约 1200 实例 / 12000 日志')
  })
})

describe('MockControlPanel · 轮询与请求日志（FR-496 阶段 6 补丁追加）', () => {
  it('默认「跟随原始」（不干预），切换档位写入运行时配置并持久化', async () => {
    const user = userEvent.setup()
    render(<MockControlPanel />)

    // 默认档必须等于改造前行为，否则「用 mock 复现真机现象」就失真了
    expect(screen.getByTestId('mock-poll-value')).toHaveTextContent('跟随原始')
    expect(screen.getByTestId('mock-poll-passive')).toBeInTheDocument()

    await user.click(screen.getByTestId('mock-poll-preset-15000'))
    expect(getMockPollInterval()).toBe(15000)
    expect(screen.getByTestId('mock-poll-value')).toHaveTextContent('15 s')
    expect(window.localStorage.getItem(MOCK_POLL_INTERVAL_STORAGE_KEY)).toBe('15000')

    await user.click(screen.getByTestId('mock-poll-preset-0'))
    expect(getMockPollInterval()).toBe(0)
    expect(screen.getByTestId('mock-poll-value')).toHaveTextContent('已关闭')

    await user.click(screen.getByTestId('mock-poll-preset--1'))
    expect(getMockPollInterval()).toBe(MOCK_POLL_FOLLOW_ORIGINAL)
    expect(screen.getByTestId('mock-poll-value')).toHaveTextContent('跟随原始')
  })

  it('列出在轮询的查询，点芯片即排除该查询', async () => {
    const user = userEvent.setup()
    installPollingWith(['instances/search', 'nodes/list'])
    render(<MockControlPanel />)

    expect(screen.getByTestId('mock-poll-scope')).toHaveTextContent('作用于 2 个轮询查询')
    const chip = screen.getByTestId('mock-poll-chip-instances/search')
    expect(chip).toHaveAttribute('aria-pressed', 'true')

    await user.click(chip)
    expect(isPollingQueryExcluded('instances/search')).toBe(true)
    expect(screen.getByTestId('mock-poll-chip-instances/search')).toHaveAttribute('aria-pressed', 'false')
    // 只排除点掉的那个，其余仍在范围内
    expect(isPollingQueryExcluded('nodes/list')).toBe(false)
  })

  it('请求日志默认静默，可切到打印（调用 worker 重启动作）', async () => {
    const user = userEvent.setup()
    const { setMockRequestLog } = await import('@jianmanager/devmock/browser')
    render(<MockControlPanel />)

    expect(screen.getByTestId('mock-request-log-off')).toHaveAttribute('aria-pressed', 'true')

    await user.click(screen.getByTestId('mock-request-log-on'))
    expect(setMockRequestLog).toHaveBeenCalledWith(true)
    expect(screen.getByTestId('mock-request-log-on')).toHaveAttribute('aria-pressed', 'true')

    await user.click(screen.getByTestId('mock-request-log-off'))
    expect(setMockRequestLog).toHaveBeenCalledWith(false)
    expect(screen.getByTestId('mock-request-log-off')).toHaveAttribute('aria-pressed', 'true')
  })
})
