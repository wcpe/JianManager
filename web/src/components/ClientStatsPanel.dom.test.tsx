import { describe, it, expect, beforeAll } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import ClientStatsPanel from './ClientStatsPanel'

/**
 * ClientStatsPanel 强断言（FR-219）：复用 FR-217 观测端点扩充的维度都渲染出来——
 * 活跃客户端（含去重口径标注）/ 更新成功率 + fail-static 率 / 版本分布 + 滞后 / 平台分布。
 * 渲染前 loginMockUser() 让 requireAuth 放行；observability handler 提供 seed 数据。
 *
 * 下载趋势图（recharts）依赖 ResizeObserver 实测宽度，jsdom 无之 → 补桩使组件不崩；
 * 本测试只断言数字卡与分布条（不依赖容器尺寸），曲线像素不断言（同 MonitoringPage.dom.test）。
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
describe('ClientStatsPanel（mock 假后端，FR-219）', () => {
  it('渲染观测扩充的统计维度（活跃客户端/成功率/fail-static/平台/滞后）', async () => {
    loginMockUser()
    renderWithProviders(<ClientStatsPanel channelId="skyblock-s1" />)

    // 等观测数据解析后断言：活跃客户端取观测去重计数（512）。
    // 默认窗 30d 超明细保留窗 → 标注「人次近似」。
    expect(await screen.findByText('512')).toBeInTheDocument()
    expect(screen.getByText('活跃客户端')).toBeInTheDocument()
    expect(screen.getByText('人次近似')).toBeInTheDocument()

    // 更新成功率（91.7%）与 fail-static 率（2.8%）数字卡。
    expect(screen.getByText('fail-static 率')).toBeInTheDocument()
    expect(screen.getByText('91.7%')).toBeInTheDocument()
    expect(screen.getByText('2.8%')).toBeInTheDocument()

    // 平台分布段落渲染并出现 Windows 行。
    expect(screen.getByText('平台分布')).toBeInTheDocument()
    expect(screen.getByText('Windows')).toBeInTheDocument()

    // 版本滞后分布：lag=0 → 「已最新」。
    expect(screen.getByText('版本滞后分布')).toBeInTheDocument()
    expect(screen.getByText('已最新')).toBeInTheDocument()
  })

  it('观测端点 500 → 回退 FR-095 看板维度，不崩溃', async () => {
    loginMockUser()
    mockInject('get', '/client-dist/observability', { kind: 'status', status: 500 })
    renderWithProviders(<ClientStatsPanel channelId="skyblock-s1" />)

    // FR-095 stats 仍可用：活跃机器码回退值（42）与成功率（93.0%）出现。
    await waitFor(() => expect(screen.getByText('42')).toBeInTheDocument())
    expect(screen.getByText('93.0%')).toBeInTheDocument()
    // 来源 IP（FR-095）段落仍渲染。
    expect(screen.getByText('来源 IP（Top 10）')).toBeInTheDocument()
  })
})
