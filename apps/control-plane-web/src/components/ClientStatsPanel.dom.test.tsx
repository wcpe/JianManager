import { describe, it, expect, beforeAll } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@jianmanager/devmock/inject'
import ClientStatsPanel from './ClientStatsPanel'

/**
 * ClientStatsPanel（FR-219 + FR-356）：
 * - 观测可用时：活跃精确/近似脚注 + 更新成功率（遥测）
 * - 观测失败时：活跃可回退 stats，但禁止用 HTTP 请求成功率冒充更新成功率
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
describe('ClientStatsPanel（mock 假后端，FR-219/356）', () => {
  it('渲染观测扩充的统计维度（活跃客户端/成功率/fail-static/平台/滞后）', async () => {
    loginMockUser()
    renderWithProviders(<ClientStatsPanel channelId="skyblock-s1" />)

    // 等观测数据解析后断言：活跃客户端取观测去重计数（512）。
    // 默认窗 30d 超明细保留窗 → 标注「人次近似」。
    expect(await screen.findByText('512')).toBeInTheDocument()
    expect(screen.getByText('活跃客户端')).toBeInTheDocument()
    expect(screen.getByText('人次近似')).toBeInTheDocument()

    // 更新绝对数与率并列，且下载 bytes 有独立趋势。
    expect(screen.getByText('更新总次数')).toBeInTheDocument()
    expect(screen.getByText('360')).toBeInTheDocument()
    expect(screen.getByText('更新成功')).toBeInTheDocument()
    expect(screen.getByText('330')).toBeInTheDocument()
    expect(screen.getByText('更新成功率')).toBeInTheDocument()
    expect(screen.getByText('fail-static 率')).toBeInTheDocument()
    expect(screen.getByText('91.7%')).toBeInTheDocument()
    expect(screen.getByText('2.8%')).toBeInTheDocument()
    expect(screen.getByText('下载字节趋势')).toBeInTheDocument()

    // 平台分布段落渲染并出现 Windows 行。
    expect(screen.getByText('平台分布')).toBeInTheDocument()
    expect(screen.getByText('Windows')).toBeInTheDocument()

    // 版本滞后分布：lag=0 → 「已最新」。
    expect(screen.getByText('版本滞后分布')).toBeInTheDocument()
    expect(screen.getByText('已最新')).toBeInTheDocument()
  })

  it('观测端点 500 → 活跃回退 stats，更新率不冒充请求成功率', async () => {
    loginMockUser()
    mockInject('get', '/client-dist/observability', { kind: 'status', status: 500 })
    renderWithProviders(<ClientStatsPanel channelId="skyblock-s1" />)

    // FR-095 stats 仍可用：活跃机器码回退值。
    await waitFor(() => expect(screen.getByText('3')).toBeInTheDocument())
    expect(screen.getByText('活跃客户端')).toBeInTheDocument()
    expect(screen.getByText('来自请求明细')).toBeInTheDocument()
    // FR-356：stats.successRate 是 HTTP 请求率，不得显示为更新成功率数字。
    expect(screen.queryByText('66.7%')).not.toBeInTheDocument()
    expect(screen.getByText('更新成功率')).toBeInTheDocument()
    expect(screen.getAllByText('—').length).toBeGreaterThanOrEqual(1)
    // 来源 IP（FR-095）段落仍渲染。
    expect(screen.getByText('来源 IP（Top 10）')).toBeInTheDocument()
  })
})
