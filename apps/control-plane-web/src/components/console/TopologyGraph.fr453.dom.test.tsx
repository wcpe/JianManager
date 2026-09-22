import { describe, it, expect, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import TopologyGraph from './TopologyGraph'

/**
 * FR-453 拓扑完整网络视图强断言：
 * 1) 未注册实例（beacon/配套服务）也上拓扑（不再只画已注册关系）；
 * 2) 层级随所选分组维度变化（默认 region 两级带）；
 * 3) 节点负载标签来自节点指标，离线节点不显示（不装 0）。
 * 消费 devmock 的 GET /topology（含 instances 全量投影）与 GET /nodes（含 cpu/mem）。
 */
beforeEach(() => {
  loginMockUser()
})

function bandLabels(): string[] {
  return screen.queryAllByTestId('topology-band-label').map((el) => (el.textContent ?? '').trim())
}

describe('TopologyGraph（FR-453 完整网络视图）', () => {
  it('未注册实例与配套服务上拓扑（beacon 可见）', async () => {
    renderWithProviders(<TopologyGraph />)
    await screen.findByText('survival-proxy')

    // beacon-1 无注册关系（配套服务），FR-453 前根本不在图上。
    expect(await screen.findByText('beacon-1')).toBeInTheDocument()
  })

  it('默认 region 层级：带标签为大区/小区两级', async () => {
    renderWithProviders(<TopologyGraph />)
    await screen.findByText('survival-proxy')

    await waitFor(() => expect(bandLabels().length).toBeGreaterThan(0))
    // 种子实例带 region:r1/r2 + zone:z1/z2 → 两级带（「大区 / 小区」）。
    expect(bandLabels().some((l) => l.includes('/'))).toBe(true)
    // 未注册实例（无 region 标签）落未分组带。
    expect(bandLabels()).toContain('未分组')
  })

  it('切换层级维度即时改变分带（region → 角色 → 群组 → 组织分组 → 无分组）', async () => {
    const user = userEvent.setup()
    renderWithProviders(<TopologyGraph />)
    await screen.findByText('survival-proxy')
    await waitFor(() => expect(bandLabels().length).toBeGreaterThan(0))

    await user.selectOptions(screen.getByTestId('topology-dimension'), 'role')
    await waitFor(() => {
      const labels = bandLabels()
      // 角色维度：带名取角色原值（backend/proxy/beacon/universal），不再按大区/小区。
      expect(labels).toContain('backend')
      expect(labels).toContain('proxy')
      expect(labels).not.toContain('未分组')
    })

    await user.selectOptions(screen.getByTestId('topology-dimension'), 'network')
    await waitFor(() => {
      const labels = bandLabels()
      expect(labels).toContain('creative')
      expect(labels).toContain('survival')
      // 未归属任何群组的实例落末尾「未分组」带。
      expect(labels[labels.length - 1]).toBe('未分组')
    })

    await user.selectOptions(screen.getByTestId('topology-dimension'), 'groupTree')
    await waitFor(() => {
      const labels = bandLabels()
      // 组织分组维度：按实例分组树分带（组名 + 祖先路径），**不是**群组名。
      // devmock：亚洲区 → 生存（含 survival-1）/ 创造（含 creative-1）。
      expect(labels).toContain('亚洲区 / 生存')
      expect(labels).toContain('亚洲区 / 创造')
      expect(labels).not.toContain('creative')
      expect(labels).not.toContain('survival')
      expect(labels[labels.length - 1]).toBe('未分组')
    })

    await user.selectOptions(screen.getByTestId('topology-dimension'), 'none')
    await waitFor(() => expect(bandLabels()).toEqual(['未分组']))
  })

  it('在线节点显示负载标签；离线节点不显示（不装 0）', async () => {
    renderWithProviders(<TopologyGraph />)
    await screen.findByText('survival-proxy')

    // devmock 仅 node 1 在线（cpuUsage 0.32 / memoryUsage 0.55）→ CPU 32% · 内存 55%。
    await waitFor(() => expect(screen.queryAllByTestId('topology-node-load').length).toBeGreaterThan(0))
    const texts = screen.queryAllByTestId('topology-node-load').map((el) => el.textContent ?? '')
    expect(texts.some((txt) => txt.includes('32%') && txt.includes('55%'))).toBe(true)
    // 离线节点（node 2）上的实例无负载标签 → 至少有一批节点不带标签。
    const totalNodes = screen.getAllByText(/server-\d{4}|survival-1|beacon-1/).length
    expect(screen.queryAllByTestId('topology-node-load').length).toBeLessThan(totalNodes)
  })
})
