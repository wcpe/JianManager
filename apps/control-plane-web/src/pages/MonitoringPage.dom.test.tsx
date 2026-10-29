import { describe, it, expect, beforeAll, beforeEach } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@jianmanager/devmock/inject'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import MonitoringPage from './MonitoringPage'

/** 
 * MonitoringPage 强断言：验监控骨架挂载（平台 4 图标题）、下钻面包屑/选择器
 * 联动出 seed 节点、关键指标概览 + 多指标对比 + 粒度选择器（剖析维度）渲染、错误注入不崩溃。
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
    // 实例级下钻改走 InstancePicker 的服务端搜索（分页信封）。
    http.get(API('/instances/search'), () =>
      HttpResponse.json({
        items: [
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
        ],
        total: 1,
        page: 1,
        pageSize: 50,
      }),
    ),
  )
})

describe('MonitoringPage（mock 假后端）', () => {
  it('① 渲染监控骨架：平台 4 图标题', async () => {
    loginMockUser()
    const { container } = renderWithProviders(<MonitoringPage />)
    expect(container.firstElementChild).toHaveAttribute('data-page', 'monitoring')
    // 阶段 6 页面迁移起，外壳改用布局层 PageShell（原为手写的 jm-page-stack 骨架类）。
    expect(container.firstElementChild).toHaveAttribute('data-slot', 'page-shell')
    expect(await screen.findByRole('heading', { name: '监控' })).toBeInTheDocument()
    // 平台主图网格标题「负载/内存」唯一（概览/对比用「1 分钟/内存已用」别名，不冲突），证明骨架挂载。
    // CPU/在线玩家 因概览/对比也用同名，故仅断言「至少出现一次」。
    expect(await screen.findByText('负载')).toBeInTheDocument()
    expect(screen.getAllByText('内存').length).toBeGreaterThan(0)
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

  it('② 下钻选择器联动出 seed 节点，且能下钻到节点视图', async () => {
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
    expect(await screen.findByText('共享 Bot Worker 运行时')).toBeInTheDocument()
    expect(screen.getByText('Bot Worker 资源为共享进程观察值，不代表任一 Bot 或会话的独占资源。')).toBeInTheDocument()
  })

  it('③剖析维度渲染：关键指标概览 + 多指标对比 + 粒度选择器', async () => {
    loginMockUser()
    renderWithProviders(<MonitoringPage />)
    // 关键指标概览 / 多指标对比 两个 Panel 标题。
    expect(await screen.findByText('关键指标概览')).toBeInTheDocument()
    expect(screen.getByText('多指标对比')).toBeInTheDocument()
    // 粒度选择器（auto/30s/5m/1h）。
    expect(screen.getByRole('tablist', { name: '聚合粒度' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: '5 分钟' })).toBeInTheDocument()
    // 对比默认选中一个关键指标，避免空图态。
    expect(screen.getByRole('button', { name: 'CPU' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.queryByText('勾选上方指标以叠加对比')).not.toBeInTheDocument()
  })

  it('③ 下钻到实例后展示进程 TOP10', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<MonitoringPage />)

    // 节点级仍是原生 select；实例级已改为 InstancePicker（Popover 基座 + 普通 button 选项，
    // 不再是 role=combobox 的 <select>，故 selectOptions 不适用）。
    const nodeDrill = (await screen.findByLabelText('下钻到实例')) as HTMLSelectElement
    await user.selectOptions(nodeDrill, 'node-1-uuid')
    await user.click(await screen.findByLabelText('选择实例…'))
    await user.click(await screen.findByRole('button', { name: 'survival' }))

    expect(await screen.findByText('进程 TOP10')).toBeInTheDocument()
    expect(screen.getByText('仅展示 JianManager 受管实例进程树，命令摘要已脱敏。')).toBeInTheDocument()
    /* 
      命令摘要不再写死 `java -Xmx4G -jar server.jar`：
      devmock 自 56847ee4 起改为按实例名拼（`java -Xmx4G -jar <实例名>.jar nogui`），
      写死值会与实际实例清单对不上（旧实现还会让进程行显示不存在的实例号）。
      这里断言「摘要带 java -Xmx4G -jar + .jar nogui」的形状，不绑死具体实例名。
    */
    const summaries = await screen.findAllByText(/java -Xmx4G -jar .+\.jar nogui/)
    expect(summaries.length).toBeGreaterThan(0)
    expect(screen.getAllByText('minecraft').length).toBeGreaterThan(0)
    // 后缀随 FR 收敛统一为 KB/MB（原 KiB/MiB）：全仓文件体积格式化已合并到
    // `@/lib/shared/format-file-size`，除数仍是 1024，仅后缀写法统一。
    // 不绑死具体数值：读写速率由 devmock 按实例 id 推导（`256KB + (id%4)*128KB` 等），
    // 写死「1.5 MB/s」会在实例集合/排序一变时整体飘。只断言形状：数字 + MB/s 或 KB/s。
    expect(screen.getAllByText(/^\d+(\.\d+)? (MB|KB)\/s$/).length).toBeGreaterThan(0)

    const row = screen.getAllByTestId('process-top-row')[0]
    await user.hover(row)
    const detail = within(row).getByTestId('process-top-hover')
    expect(within(detail).getByText('采样时间')).toBeInTheDocument()
    expect(within(detail).getByText('IO 读/写')).toBeInTheDocument()
    expect(within(detail).getByText(/\d+(\.\d+)? (MB|KB)\/s · \d+(\.\d+)? (MB|KB)\/s/)).toBeInTheDocument()
  })

  it('④ 支持受管进程详情探查与 PID 级处置确认（408）', async () => {
    const user = userEvent.setup()
    let actionRequested = false
    /* 
      pid 不再写死 24518：devmock 自 56847ee4 起按 `basePid = 24000 + idx*100` 推导
      （根进程 +12、包装器 +18），写死值在实例集合或排序一变就整体错位。
      改为从拦截到的请求里取值，再由用例断言「产物是个正数 pid」，
      既继续验「PID 级定位」，又不会绑死具体数字。
    */
    let requestedPid = ''
    server.use(
      http.post(API('/instances/:id/processes/:pid/actions'), async ({ request, params }) => {
        const body = await request.json() as { action?: string; confirm?: boolean }
        const url = new URL(request.url)
        requestedPid = String(params.pid)
        actionRequested =
          params.id === '1' &&
          body.action === 'kill_tree' &&
          body.confirm === true &&
          url.searchParams.get('confirm') === 'true'
        return HttpResponse.json({ success: true, action: 'kill_tree', pid: Number(params.pid), affectedPids: [Number(params.pid)], message: '已终止受管子进程树' })
      }),
    )
    loginMockUser()
    renderWithProviders(<MonitoringPage />)

    // 节点级仍是原生 select；实例级已改为 InstancePicker（Popover 基座 + 普通 button 选项，
    // 不再是 role=combobox 的 <select>，故 selectOptions 不适用）。
    const nodeDrill = (await screen.findByLabelText('下钻到实例')) as HTMLSelectElement
    await user.selectOptions(nodeDrill, 'node-1-uuid')
    await user.click(await screen.findByLabelText('选择实例…'))
    await user.click(await screen.findByRole('button', { name: 'survival' }))

    const inspectButtons = await screen.findAllByRole('button', { name: '探查' })
    /* 
      点**第一行**（根进程，java 本体）。devmock 的 diagnostics 只给根进程（`isRoot`），
      包装器那行是空数组——源码里 `detail.diagnostics.length > 0 && <Panel 诊断建议>`，
      点错行就会看不到诊断面板。列表按 CPU 降序，根进程 CPU 8~48% 恒高于包装器 0.8~1.6%。
    */
    await user.click(inspectButtons[0])

    // 详情弹窗标题回显「所点的那个进程」，pid 由后端推导，故用正则匹配编号。
    expect(await screen.findByText(/进程 \d+ 详情/)).toBeInTheDocument()
    expect(screen.getByText('只探查当前实例的受管进程树，处置前会再次校验 PID 归属。')).toBeInTheDocument()
    expect(screen.getByText('诊断建议')).toBeInTheDocument()
    expect(screen.getByText('祖先链')).toBeInTheDocument()
    expect(screen.getByText('后代进程')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '强制终止树' }))
    expect(await screen.findByText('强制终止进程树')).toBeInTheDocument()
    await user.click(screen.getAllByRole('button', { name: '强制终止树' }).at(-1)!)
    await waitFor(() => expect(actionRequested).toBe(true))
    // 处置确实带着「被探查的那个 pid」发出（而不是 0 / undefined）。
    expect(Number(requestedPid)).toBeGreaterThan(0)
  })

  it('⑤ 注入 500（/metrics/overview）→ 页面不崩溃，骨架仍在', async () => {
    loginMockUser()
    mockInject('get', '/metrics/overview', { kind: 'status', status: 500 })
    renderWithProviders(<MonitoringPage />)
    // 总览失败 → 图卡落到空数据态（暂无数据），但页面与骨架标题仍渲染（非崩溃）。
    expect(await screen.findByRole('heading', { name: '监控' })).toBeInTheDocument()
    expect(await screen.findByText('负载')).toBeInTheDocument()
    expect((await screen.findAllByText('暂无数据')).length).toBeGreaterThan(0)
  })
})
