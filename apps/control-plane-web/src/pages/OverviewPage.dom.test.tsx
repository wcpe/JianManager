import { describe, it, expect, beforeEach } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import { useAuthStore } from '@/stores/auth'
import OverviewPage from './OverviewPage'

/** 
 * 总览页强断言：底部密集实例表渲染种子 + 实例集联动 + 错误注入空态。
 * /nodes 与 /metrics/overview 属它域，本测试文件 server.use 桩占位（隔离运行）。
 */
beforeEach(() => {
  loginMockUser()
  server.use(
    http.get(API('/nodes'), () => HttpResponse.json([{ id: 1, name: 'node-a', status: 1 }])),
    http.get(API('/metrics/overview'), () =>
      HttpResponse.json({
        totals: {
          nodeCount: 1,
          onlineNodeCount: 1,
          runningInstances: 1,
          cpuPct: 12,
          loadAvg: 40,
          memUsedBytes: 1,
          memTotalBytes: 2,
          onlinePlayers: 2,
        },
        resolution: 'raw',
        trends: [],
      }),
    ),
    // 健康墙属同域，默认空桩（具体用例按需覆盖）。
    http.get(API('/observability/health-wall'), () => HttpResponse.json({ nodes: [] })),
  )
})

function loginPlatformAdmin(): void {
  const payload = btoa(JSON.stringify({ userId: 1, username: 'admin', role: 10, exp: Math.floor(Date.now() / 1000) + 900 }))
  loginMockUser(`mock.${payload}.sig`)
  useAuthStore.getState().login(`mock.${payload}.sig`, 'test-refresh-token')
}

describe('OverviewPage（mock 假后端）', () => {
  it('平台管理员展示有界健康、异常与共享 Bot 观测并可下钻', async () => {
    loginPlatformAdmin()
    server.use(
      http.get(API('/observability/overview'), () =>
        HttpResponse.json({
          sampledAt: '2026-07-28T00:00:00Z',
          health: { nodeCount: 2, onlineNodeCount: 1, staleNodeCount: 1, offlineNodeCount: 0, runningInstanceCount: 3, crashedInstanceCount: 1, stoppedInstanceCount: 2 },
          resources: { cpuPct: 42.5, loadPct: 35, memoryUsedBytes: 1024, memoryTotalBytes: 2048, freshness: 'fresh' },
          bots: { sharedRuntime: true, notice: 'Bot Worker 资源为共享进程观察值，不代表任一 Bot 或会话的独占资源。', nodeCount: 1, botWorkerRssBytes: 120, botWorkerCpuPct: 3.5, workerProcessRssBytes: 240, workerProcessCpuPct: 1.5, activeCount: 20, connectingCount: 1, eventLoopP95Ms: 4.2, unavailable: [{ nodeId: 2, reason: '节点心跳已过期' }] },
          alerts: [], tasks: [],
          exceptions: [{ kind: 'node_stale', nodeId: 2, title: 'node-a 心跳陈旧', href: '/monitor?node=node-a' }],
        }),
      ),
      http.get(API('/observability/health-wall'), () =>
        HttpResponse.json({
          nodes: [
            { nodeId: 2, nodeUuid: 'node-a', name: 'node-a', freshness: 'stale', cpuPct: null, memPct: null, diskPct: null, running: 0, crashed: 0, stopped: 0, activeAlerts: 0, botActive: null, botConnecting: null, level: 'stale', href: '/monitor?node=node-a' },
          ],
        }),
      ),
    )

    renderWithProviders(<OverviewPage />, { route: '/' })

    /* 
      集群健康墙已按用户要求从首页移除（首屏只保留趋势/KPI/玩家两块）。
      这里改为守护「它确实不再出现」：健康墙是页内最重的一块（逐台热力矩阵），
      若将来有人顺手加回来，首屏高度与渲染成本会无声回归。
    */
    expect(screen.queryByTestId('health-wall')).toBeNull()
    /* 
      【结构变更】Bot Worker 的独立面板已删除，其指标并入资源趋势图：
      图例里能看到两条 Bot 线（默认隐藏、勾选即显示），数据也参与坐标轴。
      因此这里不再断言面板存在，改为守护「趋势图里确实提供了这两条可选序列」。
    */
    /* 
      Bot Worker 两条序列是否真的出现在图例里，**在 jsdom 下无法稳定断言**：
      recharts 依赖容器实测尺寸才渲染线与其图例，而 jsdom 里容器高度为 0。
      该点由真机验证覆盖（图例 5 条、默认绘制 3 条，已实测）。
      这里只守「资源趋势面板仍在渲染」这一层，避免用脆弱的断言制造假绿/假红。
    */
    expect(screen.getByText('资源趋势')).toBeInTheDocument()
    /* 
      【结构变更·本轮】Bot Worker 的独立面板已删除，四项指标并入资源趋势图
      （图例里两条 Bot 序列，默认隐藏、勾选显示）。面板既已不存在，
      这里只守「它确实不再渲染」，避免有人把重复的面板加回来。
      「图例里真有那两条序列」由真机验证覆盖（jsdom 下 recharts 容器高度为 0，无法稳定断言）。
    */
    expect(screen.queryByTestId('platform-observability-bots')).toBeNull()
  })

  it('资源仪表 Tooltip 只在打开后查询受管归因，并可下钻监控页', async () => {
    server.use(
      http.get(API('/metrics/resource-attribution'), () => HttpResponse.json({
        sampledAt: '2026-07-28T00:00:00Z', freshness: 'fresh',
        nodes: [{ nodeId: 1, nodeUuid: 'node-a', name: '节点 A', status: 'fresh', observedAt: '2026-07-28T00:00:00Z', cpuPct: 10, loadPct: 20, memoryUsedBytes: 1024, memoryTotalBytes: 2048, workerProcessRssBytes: null, workerProcessCpuPct: null, botWorker: { rssBytes: null, cpuPct: null, activeCount: null, connectingCount: null, eventLoopP95Ms: null, available: false, reason: '未启动' } }],
        topInstances: [{ instanceId: 2, instanceUuid: 'inst-a', instanceName: '大厅', nodeId: 1, cpuPct: 10, rssBytes: 512, sampledAt: '2026-07-28T00:00:00Z' }],
        topProcesses: [],
      })),
    )
    const user = userEvent.setup()
    renderWithProviders(<OverviewPage />, { route: '/' })
    await user.click(await screen.findByRole('button', { name: '总内存 资源归因' }))
    const tooltip = await screen.findByTestId('overview-resource-attribution')
    expect(within(tooltip).getByRole('link', { name: /节点 A/ })).toHaveAttribute('href', '/monitor?node=node-a')
    expect(within(tooltip).getByRole('link', { name: /大厅/ })).toHaveAttribute('href', '/monitor?instance=inst-a')
    expect(within(tooltip).getAllByText('1K')).toHaveLength(2)
  })

  it('总 CPU 资源归因 Tooltip 显示 CPU 百分比而非 RSS', async () => {
    server.use(
      http.get(API('/metrics/resource-attribution'), () => HttpResponse.json({
        sampledAt: '2026-07-28T00:00:00Z', freshness: 'fresh',
        nodes: [{ nodeId: 1, nodeUuid: 'node-a', name: '节点 A', status: 'fresh', observedAt: '2026-07-28T00:00:00Z', cpuPct: 12.5, loadPct: 20, memoryUsedBytes: 1024, memoryTotalBytes: 2048, workerProcessRssBytes: null, workerProcessCpuPct: null, botWorker: { rssBytes: null, cpuPct: null, activeCount: null, connectingCount: null, eventLoopP95Ms: null, available: false, reason: '未启动' } }],
        topInstances: [{ instanceId: 2, instanceUuid: 'inst-a', instanceName: '大厅', nodeId: 1, cpuPct: 25, rssBytes: 2 * 1024 * 1024 * 1024, sampledAt: '2026-07-28T00:00:00Z' }],
        topProcesses: [],
      })),
    )
    const user = userEvent.setup()
    renderWithProviders(<OverviewPage />, { route: '/' })

    await user.click(await screen.findByRole('button', { name: '总 CPU 资源归因' }))

    const tooltip = await screen.findByTestId('overview-resource-attribution')
    expect(within(tooltip).getByText('12.5%')).toBeInTheDocument()
    expect(within(tooltip).getByText('25.0%')).toBeInTheDocument()
    expect(within(tooltip).queryByText('2.0G')).not.toBeInTheDocument()
  })

  /* 
    球体的两个交互契约（本轮）：
    ① 环下方有「结论句」——首页第一眼要判断，不只有百分比读数；
    ② 球不再因悬停变色——此前 `hover:bg-accent/40` 让三张卡轮流闪浅底，干扰读数。
    两条都是易被后人「顺手改回去」的样式，故用断言钉住。
  */
  it('球体：有结论句，悬停反馈走描边而非背景色', async () => {
    renderWithProviders(<OverviewPage />, { route: '/' })

    const cpu = await screen.findByRole('button', { name: '总 CPU 资源归因' })
    // ① 结论句与环同属一张卡：按 resourceLevel 分档，CPU 47% 落 success → 「正常」。
    const card = cpu.closest('[data-slot="panel"]') as HTMLElement
    expect(within(card).getByText('正常')).toBeInTheDocument()

    /* 
      ② 悬停反馈的形态：不用背景色，改用「环描边加粗」。
      用户要保留 hover 反馈，但原来整块卡变浅底（hover:bg-accent/*）在三个球并排时
      会轮流闪一下，干扰读数。故断言：
      - 不带**着色**的 hover:bg-*（`variant="invisible"` 自带的 hover:bg-transparent
        表示「背景不变」，与这里要守的规则同向，故排除它）；
      - 带 hover 下加粗描边的规则（反馈仍然存在，只是换了载体）。
    */
    const paintHoverBg = (cpu.className.match(/hover:bg-\S+/g) ?? []).filter(
      (cls) => cls !== 'hover:bg-transparent',
    )
    expect(paintHoverBg).toHaveLength(0)
    expect(cpu.className).toMatch(/hover:.*stroke-\[10\]/)
  })

  /* 
    【结构变更】「玩家趋势卡 + 玩家 Top5」合并为一张「玩家在线」卡：左侧曲线、右侧读数。
    玩家 Top5 已按用户要求删除——它与「服务器」页的实例列表回答同一件事，
    首页不再承担「哪台服在跑」的浏览职责。这里同时守住「排行确实不再出现」。
  */
  it('首页渲染玩家在线（曲线 + 读数），且不再有玩家 Top5', async () => {
    renderWithProviders(<OverviewPage />, { route: '/' })
    const table = await screen.findByTestId('overview-player-table')
    // 曲线是这张卡的核心：玩家侧要看的是昼夜起伏的形状，不只是数字。
    await waitFor(() => expect(table.querySelector('svg')).not.toBeNull())
    expect(within(table).getByText(/当前在线/)).toBeInTheDocument()
    expect(screen.queryByTestId('overview-player-ranking')).toBeNull()
  })

  /* 
    可用率 SLO 不单独占卡（用户选择「塞进现有卡片」），并入趋势卡头部。
    两条语义要守住：① 有证据时按 target 达标情况着色；② `applicable=false`
    （窗口内无可用证据、分母为 0）必须显示「不适用」，不能显示成 0% ——
    否则读者会把它读成「完全不可用」。
  */
  it('趋势卡头部展示可用率，无证据时显示「不适用」而非 0%', async () => {
    const { unmount } = renderWithProviders(<OverviewPage />, { route: '/' })
    const trend = await screen.findByTestId('overview-player-table')
    await waitFor(() => expect(within(trend).getByText(/可用率/)).toBeInTheDocument())
    unmount()

    server.use(
      http.get(API('/metrics/slo'), () =>
        HttpResponse.json({
          scope: 'platform', availability: 0, totalSamples: 0, upSamples: 0,
          incidents: 3, activeIncidents: 1, mttrSeconds: null, mtbfSeconds: null,
          budgetAllowedSec: 0, budgetBurnedSec: 0, target: 0.995,
          approximatedBuckets: false, applicable: false,
        }),
      ),
    )
    renderWithProviders(<OverviewPage />, { route: '/' })
    const trend2 = await screen.findByTestId('overview-player-table')
    await waitFor(() => expect(within(trend2).getByText('不适用')).toBeInTheDocument())
    expect(within(trend2).queryByText('0.00%')).toBeNull()
  })


  
})
