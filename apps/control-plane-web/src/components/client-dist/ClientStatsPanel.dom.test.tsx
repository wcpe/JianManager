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

    // 等观测数据解析后断言结构与口径标注（FR-425 起 mock 按真实时间确定性生成，数值随时间变化，
    // 故断言标签/形态而非写死数值——旧断言 512/360/330 随生成器升级失效）。
    // 默认窗 30d 超明细保留窗 → 标注「人次近似」（obs 异步解析后才出现，需 findBy；并行负载下放宽超时）。
    expect(await screen.findByText('活跃客户端')).toBeInTheDocument()
    expect(await screen.findByText('人次近似', {}, { timeout: 5000 })).toBeInTheDocument()

    // 更新绝对数与率并列，且下载 bytes 有独立趋势。
    expect(screen.getByText('更新总次数')).toBeInTheDocument()
    expect(screen.getByText('更新成功')).toBeInTheDocument()
    // FR-428 洞察卡与 FR-356 口径网格都有「更新成功率」卡。
    expect(screen.getAllByText('更新成功率').length).toBeGreaterThanOrEqual(1)
    expect(screen.getByText('fail-static 率')).toBeInTheDocument()
    expect(screen.getByText('下载字节趋势')).toBeInTheDocument()

    // FR-428 洞察卡 + FR-427 热力图 + FR-426 机器排行（新增区块一并在统计 Tab 呈现）。
    expect(screen.getByTestId('client-dist-insight-cards')).toBeInTheDocument()
    expect(screen.getByText('更新活动热力图')).toBeInTheDocument()
    expect(screen.getByText('机器更新排行')).toBeInTheDocument()

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

  it('默认 30d 窗外近似时展示「窗外」空态提示', async () => {
    loginMockUser()
    renderWithProviders(<ClientStatsPanel channelId="skyblock-s1" />)
    const banner = await screen.findByTestId('client-stats-empty-kind')
    expect(banner).toHaveAttribute('data-empty-kind', 'out_of_window')
    expect(banner).toHaveTextContent(/窗外|精确去重不可用|人次近似/)
  })
})
