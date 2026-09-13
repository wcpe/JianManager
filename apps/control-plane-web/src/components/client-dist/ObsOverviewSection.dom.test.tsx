import { describe, it, expect, beforeAll } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { useAuthStore } from '@/stores/auth'
import ObsOverviewSection from './ObsOverviewSection'
import ProtectionCenterPage from '@/pages/ProtectionCenterPage'

/** 平台管理员态（页头的统一时间筛选仅管理员可见，须以 role=10 登录）。 */
const ADMIN_TOKEN = `mock.${btoa(
  JSON.stringify({ userId: 1, username: 'admin', role: 10, exp: Math.floor(Date.now() / 1000) + 900 }),
)}.sig`

function loginAsAdmin() {
  loginMockUser(ADMIN_TOKEN)
  useAuthStore.getState().login(ADMIN_TOKEN, 'test-refresh-token')
}

/**
 * 分发观测总览区块（FR-426/427/428 mock 验收）：
 * - 洞察卡（同环比）/ 更新热力图 / 机器更新排行 三区块齐备
 * - 点机器行 → 抽屉出该机器更新事件时间线
 * - 自定义 from/to 深链：监控页 URL 窗口还原到时间筛选展示
 */
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

describe('ObsOverviewSection（FR-426/427/428 mock）', () => {
  it('洞察卡+热力图+机器排行渲染；点机器行出更新时间线', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<ObsOverviewSection channelId="skyblock-s1" window={{ range: '7d' }} />)

    // 三区块齐备。
    expect(await screen.findByTestId('client-dist-insight-cards')).toBeInTheDocument()
    expect(await screen.findByTestId('update-heatmap')).toBeInTheDocument()
    const rows = await screen.findAllByTestId('machine-row')
    expect(rows.length).toBeGreaterThan(0)

    // 钻取：点首行 → 抽屉时间线出事件条目。
    await user.click(rows[0])
    expect(await screen.findByTestId('machine-timeline')).toBeInTheDocument()
  })

  it('7d 预设窗落在明细保留窗内：无「近似」标注', async () => {
    loginMockUser()
    renderWithProviders(<ObsOverviewSection channelId="skyblock-s1" window={{ range: '7d' }} />)
    expect(await screen.findByTestId('client-dist-insight-cards')).toBeInTheDocument()
    expect(screen.queryByText('clientDistObs.approxWindow')).not.toBeInTheDocument()
  })
})

describe('FR-425 自定义时间深链（页面 B）', () => {
  it('URL from/to 还原到时间筛选按钮文案', async () => {
    loginAsAdmin()
    renderWithProviders(
      <ProtectionCenterPage />,
      { route: '/client-dist-ops?from=2026-09-01T00%3A00%3A00Z&to=2026-09-03T00%3A00%3A00Z' },
    )
    // 页头时间筛选按钮应展示自定义区间（09/01 08:00 ~ 09/03 08:00，本地时区）而非预设档名。
    const btn = await screen.findByTestId('obs-time-range')
    expect(btn.textContent).toMatch(/09\/01/)
    expect(btn.textContent).toMatch(/09\/03/)
  })
})
