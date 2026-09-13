import { describe, it, expect, beforeAll } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { useAuthStore } from '@/stores/auth'
import OpsOverviewTab from './OpsOverviewTab'

/**
 * 总览 Tab（全面融合：分发健康 + 运行态 + 请求侧 + 安全态势）。
 * 验收：分区齐备；请求成功率与更新成功率分标（FR-356）；安全排行深链可点。
 */

const ADMIN_TOKEN = `mock.${btoa(
  JSON.stringify({ userId: 1, username: 'admin', role: 10, exp: Math.floor(Date.now() / 1000) + 900 }),
)}.sig`

function loginAsAdmin() {
  loginMockUser(ADMIN_TOKEN)
  useAuthStore.getState().login(ADMIN_TOKEN, 'test-refresh-token')
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
  if (!Element.prototype.scrollIntoView) Element.prototype.scrollIntoView = () => {}
})

describe('OpsOverviewTab', () => {
  it('渲染五分区：健康 / 速览 / 趋势 / 分布 / 安全排行', async () => {
    loginAsAdmin()
    renderWithProviders(<OpsOverviewTab window={{ range: '7d' }} onLink={() => {}} />)

    expect(await screen.findByTestId('ops-overview')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByTestId('ops-overview-health')).toBeInTheDocument())
    expect(screen.getByTestId('ops-overview-glance')).toBeInTheDocument()
    expect(screen.getByTestId('ops-overview-trends')).toBeInTheDocument()
    expect(screen.getByTestId('ops-overview-dists')).toBeInTheDocument()
    expect(screen.getByTestId('ops-overview-ranks')).toBeInTheDocument()
  })

  it('请求成功率与更新成功率分标出现（口径不混用）', async () => {
    loginAsAdmin()
    renderWithProviders(<OpsOverviewTab window={{ range: '7d' }} onLink={() => {}} />)

    expect(await screen.findByText('请求成功率')).toBeInTheDocument()
    // 更新成功率只在 InsightCards（速览区已与健康区去重）。
    expect(await screen.findByText('更新成功率')).toBeInTheDocument()
  })

  it('InsightCards 洞察卡在观测数据就绪后渲染', async () => {
    loginAsAdmin()
    renderWithProviders(<OpsOverviewTab window={{ range: '7d' }} onLink={() => {}} />)

    expect(await screen.findByTestId('client-dist-insight-cards')).toBeInTheDocument()
    expect(await screen.findByText('活跃下载')).toBeInTheDocument()
  })

  it('安全排行 IP 深链指向全量日志 request 视图', async () => {
    loginAsAdmin()
    renderWithProviders(<OpsOverviewTab window={{ range: '7d' }} onLink={() => {}} />)

    expect(await screen.findByTestId('ops-overview-ranks')).toBeInTheDocument()
    await waitFor(() => {
      const links = screen.getAllByRole('link')
      const ipLink = links.find((a) => a.getAttribute('href')?.includes('tab=logs'))
      expect(ipLink).toBeTruthy()
      expect(ipLink?.getAttribute('href')).toMatch(/type=request/)
    })
  })
})
