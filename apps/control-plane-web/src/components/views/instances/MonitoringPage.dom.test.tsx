import { useState } from 'react'
import { describe, it, expect, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import '@/i18n'
import { PLATFORM_CHART_DEFS } from '@jianmanager/ui/lib/monitor-metrics'
import type { ManagedProcessDetail, ProcessTopItem } from '@/lib/console/managed-process'
import type { ManagedProcessAction } from '@/lib/console/managed-process'
import MonitoringPageView from './MonitoringPage'

/**
 * 监控页视图的针对性用例（受控视图，不取数——目标/序列/进程数据全由 props 注入）。
 *
 * 走视图而非页面：这三条性质都只由视图内的渲染与回调决定，注入 props 才能精确构造
 * 「后端拒绝」「采样时间刷新」等页面级测试难以稳定造出的输入。
 */

/** 一行进程 TOP 采样（`sampledAt` 每轮刷新都会变，`pid` 不变）。 */
function processRow(overrides: Partial<ProcessTopItem> = {}): ProcessTopItem {
  return {
    instanceId: 1,
    instanceUuid: 'inst-1-uuid',
    pid: 24518,
    name: 'java',
    user: 'mc',
    commandSummary: 'java -Xmx4G -jar server.jar',
    cpuPercent: 12.5,
    rssBytes: 1024 * 1024,
    readBytesPerSec: 512,
    writeBytesPerSec: 256,
    sampledAt: '2025-01-01T00:00:00Z',
    ...overrides,
  }
}

/** 视图必需的注入项（与页面接线层同形，此处只给最小可用值）。 */
function baseProps(overrides: Record<string, unknown> = {}) {
  return {
    target: { kind: 'platform' as const },
    range: '24h' as const,
    onRangeChange: () => {},
    resolution: 'auto' as const,
    onResolutionChange: () => {},
    compareSel: [],
    onCompareToggle: () => {},
    raw: [],
    processTop: [],
    selectedProcess: null,
    onSelectedProcessChange: () => {},
    pendingAction: null,
    onPendingActionChange: () => {},
    onProcessAction: async () => {},
    onRefreshProcessDetail: () => {},
    useSeries: () => ({ series: [], isLoading: false }),
    actionDescription: '描述',
    chartDefs: PLATFORM_CHART_DEFS,
    rankingSlot: null,
    forecastSlot: null,
    sloSlot: null,
    drillSlot: null,
    ...overrides,
  }
}

const processDetail: ManagedProcessDetail = {
  instance: {
    id: 1,
    uuid: 'inst-1-uuid',
    name: 'survival',
    nodeId: 1,
    nodeUuid: 'node-1-uuid',
    nodeName: 'node-a',
  },
  rootPid: 24518,
  target: {
    pid: 24518,
    parentPid: 1,
    name: 'java',
    isRoot: false,
    cpuPercent: 12.5,
    rssBytes: 1024 * 1024,
    readBytesPerSec: 512,
    writeBytesPerSec: 256,
    user: 'mc',
    commandSummary: 'java -Xmx4G -jar server.jar',
    uptimeSeconds: 3600,
    threadCount: 42,
    sampledAt: '2025-01-01T00:00:00Z',
    unavailableReason: '',
  },
  ancestors: [],
  children: [],
  diagnostics: [],
  history: {
    windowSeconds: 300,
    sampleCount: 3,
    latestSampledAt: '2025-01-01T00:00:00Z',
    rssDeltaBytes: 0,
    avgCpuPercent: 10,
    avgWriteBytesPerSec: 128,
  },
}

describe('MonitoringPage 视图（受控注入）', () => {
  /**
   * 进程 TOP 的行 key 只能用 pid。
   *
   * 原实现是 `key={`${row.pid}-${row.sampledAt}`}`：`sampledAt` 每轮采样（30s）都会变，
   * 于是每次数据刷新都把整张表的每一行卸载重挂——行内焦点、悬停态与 DOM 身份全丢，
   * 而这只是一次数据刷新，不是换了一行。
   */
  it('进程 TOP 行在采样时间刷新后保持同一 DOM 节点（key 只用 pid）', () => {
    const { rerender } = render(
      <MonitoringPageView {...baseProps({ processTop: [processRow()] })} />,
    )
    const before = screen.getAllByTestId('process-top-row')[0]
    expect(before).toBeDefined()

    // 同一 pid、新的采样时间（下一轮 30s 采样的样子）。
    rerender(
      <MonitoringPageView
        {...baseProps({ processTop: [processRow({ sampledAt: '2025-01-01T00:00:30Z' })] })}
      />,
    )

    const after = screen.getAllByTestId('process-top-row')[0]
    expect(after).toBe(before)
  })

  /**
   * 处置被后端拒绝时：确认框必须收起，并给出失败反馈。
   *
   * 原实现 `void onProcessAction(...).then(...)` 没有 catch：拒绝时 `pendingAction` 永不为 null
   * → 确认框卡在打开态、本视图内没有任何失败反馈，且 promise 落成未处理拒绝。
   */
  it('处置被后端拒绝：收起确认框并给出失败反馈（不再卡在打开态）', async () => {
    const user = userEvent.setup()
    const onProcessAction = vi.fn().mockRejectedValue(new Error('PID 归属校验失败'))

    // 用与页面接线层同形的受控宿主持有 pendingAction：确认框的开合由 onPendingActionChange 驱动，
    // 才能验出「拒绝后是否真的收起」。
    function Harness() {
      const [pending, setPending] = useState<ManagedProcessAction | null>(null)
      const selected = processRow()
      return (
        <MonitoringPageView
          {...baseProps({
            processTop: [selected],
            selectedProcess: selected,
            processDetail,
            pendingAction: pending,
            onPendingActionChange: setPending,
            onProcessAction,
          })}
        />
      )
    }

    render(<Harness />)

    // 详情弹窗里的处置入口 → 打开确认框。
    await user.click(await screen.findByRole('button', { name: '强制终止树' }))
    expect(await screen.findByText('强制终止进程树')).toBeInTheDocument()

    await user.click(screen.getAllByRole('button', { name: '强制终止树' }).at(-1)!)

    await waitFor(() => expect(onProcessAction).toHaveBeenCalledTimes(1))
    // 关键：确认框收起（pendingAction 被清空），而不是永远停在打开态。
    await waitFor(() => expect(screen.queryByText('强制终止进程树')).not.toBeInTheDocument())
    // 失败反馈可见。
    expect(await screen.findByTestId('process-action-error')).toBeInTheDocument()
  })

  /**
   * 进程 TOP 行在窄屏不得横向溢出（否则要横滚才点得到「探查」）。
   *
   * jsdom 没有布局引擎，量不到真实宽度，故这里守的是**类名契约**：
   * 行容器必须允许换行（flex-wrap）且不再使用固定像素的 grid 列模板。
   */
  it('进程 TOP 行容器允许换行、不再用固定像素列模板', () => {
    render(<MonitoringPageView {...baseProps({ processTop: [processRow()] })} />)

    const row = screen.getAllByTestId('process-top-row')[0]
    expect(row).toBeDefined()
    expect(row!.className).toContain('flex-wrap')
    expect(row!.className).not.toContain('grid-cols-[')
    // 命令摘要格可收缩截断（min-w-0 + truncate），探查按钮不被挤出视口。
    expect(row!.querySelector('.min-w-0.flex-1.truncate')).not.toBeNull()
    expect(screen.getAllByRole('button', { name: '探查' }).length).toBeGreaterThan(0)
  })
})
