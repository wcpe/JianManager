import { describe, it, expect, beforeAll, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import type { ClientRuntimeOverview } from '@/api/clientRuntimeStates'
import OpsClientsTab from './OpsClientsTab'

/**
 * OpsClientsTab（FR-430，迁自旧监控页用例④与 ObsOverviewSection 测试）：
 * - 更新侧总览（洞察卡 + 热力图 + 机器排行）随 Tab 呈现（ObsOverviewSection 自取 devmock）；
 * - 运行态 KPI/明细由 `overview` prop 驱动，机器行「看日志」触发 onLink 联动回调。
 */
const OVERVIEW: ClientRuntimeOverview = {
  channelId: '',
  from: '2026-06-21T10:00:00Z',
  to: '2026-06-28T10:00:00Z',
  summary: { recentStarted: 2, todayStarted: 3, recentStarts: 2, todayStarts: 3, updateSuccessRate: 0.8, updateFailureRate: 0.2 },
  items: [
    { id: 1, channelId: 'skyblock-s1', machineId: 'm-aaaa', ip: '203.0.113.1', platform: 'windows', javaVersion: '21', launcher: 'Prism', coreVersion: '2.1.0', localVersion: 2, firstSeenAt: '2026-06-01T00:00:00Z', lastHeartbeatAt: '2026-06-28T09:59:00Z' },
  ],
  runtimeVersionDist: [{ version: 2, count: 1 }],
  coreVersionDist: [{ value: '2.1.0', count: 1 }],
  platformDist: [{ value: 'windows', count: 1 }],
  launcherDist: [{ value: 'Prism', count: 1 }],
  lagDist: [{ lag: 0, count: 1 }],
  updateResultSeries: [{ ts: '2026-06-28T00:00:00Z', success: 8, failStatic: 0, rolledBack: 1, error: 1 }],
}

beforeAll(() => {
  if (!('ResizeObserver' in globalThis)) {
    class RO {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
    ;(globalThis as { ResizeObserver?: unknown }).ResizeObserver = RO
  }
})

describe('OpsClientsTab（机器 / 客户端）', () => {
  it('渲染更新侧总览 + 运行态指标，机器行「看日志」回调联动', async () => {
    loginMockUser()
    const onLink = vi.fn()
    const user = userEvent.setup()
    renderWithProviders(
      <OpsClientsTab channelId={undefined} window={{ range: '7d' }} overview={OVERVIEW} isError={false} onLink={onLink} />,
    )

    // 更新侧总览（FR-426/427/428）。
    expect(await screen.findByTestId('client-dist-insight-cards')).toBeInTheDocument()
    expect(await screen.findByTestId('update-heatmap')).toBeInTheDocument()
    expect((await screen.findAllByTestId('machine-row')).length).toBeGreaterThan(0)

    // 运行态 KPI + 明细（由 prop 驱动，无需等待）。
    expect(screen.getByText('近 5 分钟启动')).toBeInTheDocument()
    expect(screen.getByText('今日启动')).toBeInTheDocument()
    expect(screen.getByText('客户端运行态')).toBeInTheDocument()

    const matches = await screen.findAllByText('m-aaaa')
    const row = matches
      .map((el) => el.closest('tr'))
      .find((tr) => tr && within(tr).queryByRole('button', { name: '看日志' }))
    expect(row).not.toBeNull()
    await user.click(within(row as HTMLElement).getByRole('button', { name: '看日志' }))
    expect(onLink).toHaveBeenCalledWith({ machineId: 'm-aaaa' })
  })

  it('端点错误：当前 Tab 降级为错误态、不崩溃', async () => {
    loginMockUser()
    renderWithProviders(
      <OpsClientsTab channelId={undefined} window={{ range: '7d' }} isError onLink={() => {}} />,
    )
    expect(await screen.findByText('加载客户端运行态失败')).toBeInTheDocument()
  })
})
