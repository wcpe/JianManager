import { describe, it, expect, beforeAll, beforeEach } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import { server } from '@/mocks/server'
import { API } from '@/mocks/api'
import MonitoringPage from './MonitoringPage'

/**
 * MonitoringPage 强断言（FR-208 + FR-221）：验监控骨架挂载（平台 4 图标题）、下钻面包屑/选择器
 * 联动出 seed 节点、关键指标概览 + 多指标对比 + 粒度选择器（FR-221 剖析维度）渲染、错误注入不崩溃。
 *
 * 说明：图表（recharts）依赖 ResizeObserver 实测宽度，jsdom 无布局/无 ResizeObserver，
 * 故曲线像素不可断言——本测试在 jsdom 补 ResizeObserver 桩使组件不崩，断言 Panel 标题与选择器
 * 选项（这些不依赖容器尺寸），即可证明 /metrics/overview（平台）与 /nodes、/instances 数据路径接通。
 * /nodes、/instances 非本域 endpoint，用 server.use 就地桩供选择器，不在 domains/ 重定义别域。
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

beforeEach(() => {
  server.use(
    http.get(API('/nodes'), () =>
      HttpResponse.json([
        {
          id: 1,
          uuid: 'node-1-uuid',
          name: '北京节点',
          host: '10.0.0.1',
          grpcPort: 9100,
          wsPort: 9200,
          status: 1,
          maintenance: false,
          os: 'linux',
          arch: 'amd64',
          cpuCores: 8,
          memoryMb: 16384,
          diskTotalMb: 524288,
          cpuUsage: 40,
          memoryUsage: 50,
          diskUsage: 30,
          networkBytesSent: 0,
          networkBytesRecv: 0,
          loadAvg1: 1.2,
          lastHeartbeat: new Date().toISOString(),
          createdAt: new Date().toISOString(),
        },
      ]),
    ),
    http.get(API('/instances'), () =>
      HttpResponse.json([
        {
          id: 1,
          uuid: 'inst-1-uuid',
          nodeId: 1,
          name: 'survival',
          type: 'minecraft',
          role: 'backend',
          processType: 'daemon',
          status: 'RUNNING',
          startCommand: '',
          workDir: '/srv/survival',
          serverPort: 25565,
          autoStart: false,
          autoRestart: false,
          tags: '',
          createdAt: new Date().toISOString(),
        },
      ]),
    ),
  )
})

describe('MonitoringPage（mock 假后端）', () => {
  it('① 渲染监控骨架：平台 4 图标题', async () => {
    loginMockUser()
    renderWithProviders(<MonitoringPage />)
    expect(await screen.findByRole('heading', { name: '监控' })).toBeInTheDocument()
    // 平台主图网格标题「负载/内存」唯一（概览/对比用「1 分钟/已用」别名，不冲突），证明骨架挂载。
    // CPU/在线玩家 因 FR-221 概览/对比也用同名，故仅断言「至少出现一次」。
    expect(await screen.findByText('负载')).toBeInTheDocument()
    expect(screen.getByText('内存')).toBeInTheDocument()
    expect(screen.getAllByText('CPU').length).toBeGreaterThan(0)
    expect(screen.getAllByText('在线玩家').length).toBeGreaterThan(0)
  })

  it('① 平台总览数据到达后退出加载态', async () => {
    loginMockUser()
    renderWithProviders(<MonitoringPage />)
    await screen.findByText('负载')
    // /metrics/overview 成功 → 各图卡退出「加载中...」（数据路径接通）。
    await waitFor(() => expect(screen.queryByText('加载中...')).not.toBeInTheDocument())
  })

  it('② 下钻选择器联动出 seed 节点，且能下钻到节点视图（FR-221）', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<MonitoringPage />)
    // 平台层：面包屑起点「平台」+「节点…」下钻下拉列出 seed 节点。
    expect(await screen.findByText('平台')).toBeInTheDocument()
    const drill = (await screen.findByLabelText('下钻到实例')) as HTMLSelectElement
    await waitFor(() => {
      expect(within(drill).getByRole('option', { name: '北京节点' })).toBeInTheDocument()
    })
    // 下钻到该节点 → 面包屑出现节点名（变为节点视图）。
    await user.selectOptions(drill, 'node-1-uuid')
    expect(await screen.findByText('北京节点')).toBeInTheDocument()
  })

  it('③ FR-221 剖析维度渲染：关键指标概览 + 多指标对比 + 粒度选择器', async () => {
    loginMockUser()
    renderWithProviders(<MonitoringPage />)
    // 关键指标概览 / 多指标对比 两个 Panel 标题。
    expect(await screen.findByText('关键指标概览')).toBeInTheDocument()
    expect(screen.getByText('多指标对比')).toBeInTheDocument()
    // 粒度选择器（auto/30s/5m/1h）。
    expect(screen.getByRole('tablist', { name: '聚合粒度' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: '5 分钟' })).toBeInTheDocument()
    // 对比初始空选 → 提示语在位。
    expect(screen.getByText('勾选上方指标以叠加对比')).toBeInTheDocument()
  })

  it('④ 注入 500（/metrics/overview）→ 页面不崩溃，骨架仍在', async () => {
    loginMockUser()
    mockInject('get', '/metrics/overview', { kind: 'status', status: 500 })
    renderWithProviders(<MonitoringPage />)
    // 总览失败 → 图卡落到空数据态（暂无数据），但页面与骨架标题仍渲染（非崩溃）。
    expect(await screen.findByRole('heading', { name: '监控' })).toBeInTheDocument()
    expect(await screen.findByText('负载')).toBeInTheDocument()
    expect((await screen.findAllByText('暂无数据')).length).toBeGreaterThan(0)
  })
})
