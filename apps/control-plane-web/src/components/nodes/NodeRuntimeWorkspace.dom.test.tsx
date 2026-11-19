import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import NodeRuntimeWorkspace from './NodeRuntimeWorkspace'
import { readRuntimeSub } from '@/lib/nodes/runtime-sub'
import type { NodeInfo } from '@/lib/nodes/node-types'

/**
 * 运行时工作台强断言：
 *  ① 就绪带给出**结论**（不是原子事实罗列）：无 JDK 时 java 卡为 blocked 并给出安装动作；
 *  ② 二级 sub 可寻址：点击切 sub、非法值回退默认；
 *  ③ 清单表把 JDK 与 Node 画在同一张表里（不再互斥），且引用实例列可见；
 *  ④ 删除被引用项时**前置禁用**并写明原因（不再「先点后 409」）；
 *  ⑤ 只有 log sub 才发日志运行时请求（改造前无条件 10s 轮询）。
 *
 * 取数层整模块 mock：这些用例关心的是「给定数据后页面怎么表现」，
 * 而不是 React Query 的取数行为（那条链路由 api 层自己的测试覆盖）。
 */
const state = {
  jdks: [] as Array<Record<string, unknown>>,
  runtimes: [] as Array<Record<string, unknown>>,
  logEnabled: false,
  pm: null as string | null,
  /** 当前 registry 列表（摘要里的源条数）。 */
  registries: [] as Array<{ url: string; scope?: string }>,
  /** 采集面板收到的 active（用来断言「弹窗关着就不取数」）。 */
  logAcqActive: null as boolean | null,
  /** 日志运行时状态：默认「未启用」（无实例），用例可覆盖。 */
  logRuntime: { instances: [] as Array<Record<string, unknown>> } as
    | { instances: Array<Record<string, unknown>> }
    | undefined,
}

vi.mock('@/api/jdks', () => ({
  useNodeJDKsWithRefs: () => ({ data: state.jdks, isLoading: false }),
  useDeleteJDK: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useUpdateJDK: () => ({ mutateAsync: vi.fn(), isPending: false }),
}))
vi.mock('@/api/runtimes', () => ({
  useNodeRuntimes: () => ({ data: state.runtimes, isLoading: false }),
  useScanRuntimes: () => ({ mutateAsync: vi.fn() }),
  useRegisterRuntime: () => ({ mutateAsync: vi.fn() }),
  useInstallRuntime: () => ({ mutateAsync: vi.fn() }),
  useDeleteRuntime: () => ({ mutateAsync: vi.fn(), isPending: false }),
}))
vi.mock('@/api/pmConfig', () => ({
  useNodePMConfig: () => ({
    data: state.pm ? { pm: state.pm, registries: state.registries } : undefined,
    isLoading: false,
  }),
  useSetNodePMConfig: () => ({ mutate: vi.fn() }),
  useGlobalPackages: () => ({ data: undefined, isLoading: false, isFetching: false, refetch: vi.fn(), error: null }),
  useInstallGlobalPackage: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useRemoveGlobalPackage: () => ({ mutateAsync: vi.fn(), isPending: false }),
}))
vi.mock('@/api/logRuntime', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/logRuntime')>()
  return {
    ...actual,
    useLogRuntime: (_id: number, active: boolean) => {
      // 记录调用时的 active，用来断言「未进入 log sub 时不发日志请求」。
      state.logEnabled = active
      return { data: state.logRuntime, isLoading: false, isFetching: false, isError: false, refetch: vi.fn() }
    },
    useApprovedLogAssets: () => ({ data: [], refetch: vi.fn() }),
    useControlLogRuntime: () => ({ mutateAsync: vi.fn(), isPending: false }),
    useInstallLogAsset: () => ({ mutateAsync: vi.fn(), isPending: false }),
    useUploadLogAsset: () => ({ mutateAsync: vi.fn(), isPending: false }),
    useMigrateLogPartition: () => ({ mutateAsync: vi.fn(), isPending: false }),
    useResolveLogIngestGaps: () => ({ mutateAsync: vi.fn(), isPending: false }),
  }
})
vi.mock('@/api/metrics', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/metrics')>()
  return {
    ...actual,
    useMetricSeries: () => ({ data: { series: [] }, refetch: vi.fn() }),
  }
})
// 日志采集面板自带取数（settings 域），本用例不关心其内容，给一个轻量替身。
// 替身要透出 active：弹窗关闭 = 不挂载 = 不取数，这是「关闭即停止请求」的直接证据。
vi.mock('@/components/logs/NodeLogAcqPanel', () => ({
  default: ({ active = true }: { active?: boolean }) => {
    state.logAcqActive = active
    return <div data-testid="log-acq-panel" data-active={String(active)} />
  },
}))

const node = (over: Partial<NodeInfo> = {}): NodeInfo =>
  ({
    id: 1,
    uuid: 'node-uuid-1',
    name: 'alpha',
    host: '10.0.0.1',
    grpcPort: 1,
    wsPort: 2,
    status: 1,
    maintenance: false,
    tunnelConnected: true,
    os: 'linux',
    arch: 'amd64',
    cpuCores: 8,
    memoryMb: 16384,
    diskTotalMb: 102400,
    cpuUsage: 0.2,
    memoryUsage: 0.4,
    diskUsage: 0.5,
    networkBytesSent: 0,
    networkBytesRecv: 0,
    loadAvg1: 0.5,
    lastHeartbeat: new Date().toISOString(),
    createdAt: new Date().toISOString(),
    ...over,
  }) as NodeInfo

beforeEach(() => {
  state.jdks = []
  state.runtimes = []
  state.logEnabled = false
  state.pm = null
  state.registries = []
  state.logAcqActive = null
  state.logRuntime = { instances: [] }
})

describe('readRuntimeSub', () => {
  it('非法/缺省值回退 inventory，合法值原样返回', () => {
    expect(readRuntimeSub(null)).toBe('inventory')
    expect(readRuntimeSub('bogus')).toBe('inventory')
    expect(readRuntimeSub('log')).toBe('log')
    expect(readRuntimeSub('logAcq')).toBe('logAcq')
  })
})

describe('NodeRuntimeWorkspace', () => {
  it('无 JDK 时就绪带给出 blocked 结论与安装动作（不是「暂无数据」）', async () => {
    renderWithProviders(<NodeRuntimeWorkspace node={node()} sub="inventory" onSub={() => {}} />)
    const javaCard = await screen.findByTestId('runtime-readiness-java')
    expect(javaCard.dataset.level).toBe('blocked')
    expect(within(javaCard).getByText('无可用 JDK')).toBeInTheDocument()
    expect(within(javaCard).getByTestId('runtime-readiness-action-java')).toBeInTheDocument()
  })

  it('JDK 与 Node 同表呈现：类型作为列值，引用实例数可见', async () => {
    state.jdks = [
      {
        id: 11,
        vendor: 'Temurin',
        majorVersion: 21,
        version: '21.0.4',
        arch: 'x64',
        path: '/opt/jdks/temurin-21',
        managed: true,
        instances: [{ id: 7, uuid: 'u7', name: 'survival', status: 'RUNNING', binding: 'direct' }],
        refCount: 1,
        deleteBlocked: true,
        deleteBlockedReason: 'direct',
      },
    ]
    state.runtimes = [
      { id: 21, type: 'nodejs', name: 'Node.js 22', majorVersion: 22, version: '22.11.0', arch: 'x64', path: '/opt/node22', managed: true },
    ]
    renderWithProviders(<NodeRuntimeWorkspace node={node()} sub="inventory" onSub={() => {}} />)

    const table = await screen.findByTestId('runtime-inventory-table')
    // 同一张表里两行：这是「互斥过滤」被消除的证据。
    expect(within(table).getByText('Temurin')).toBeInTheDocument()
    expect(within(table).getByText('Node.js 22')).toBeInTheDocument()
    // 引用列可点，且带绑定方式摘要。
    expect(within(table).getByTestId('runtime-refs-jdk-11')).toHaveTextContent('1')
  })

  it('被引用的 JDK：删除菜单项前置禁用并写明原因', async () => {
    state.jdks = [
      {
        id: 11,
        vendor: 'Temurin',
        majorVersion: 21,
        version: '21.0.4',
        arch: 'x64',
        path: '/opt/jdks/temurin-21',
        managed: true,
        instances: [{ id: 7, uuid: 'u7', name: 'survival', status: 'RUNNING', binding: 'direct' }],
        refCount: 1,
        deleteBlocked: true,
        deleteBlockedReason: 'direct',
      },
    ]
    const user = userEvent.setup()
    renderWithProviders(<NodeRuntimeWorkspace node={node()} sub="inventory" onSub={() => {}} />)
    await user.click(await screen.findByTestId('runtime-menu-jdk-11'))
    const item = await screen.findByTestId('runtime-menu-delete-jdk-11')
    expect(item).toHaveAttribute('aria-disabled', 'true')
    expect(item).toHaveTextContent('被 1 个实例直接绑定')
  })

  it('未被引用的 JDK：删除菜单可用（文案回到「删除」）', async () => {
    state.jdks = [
      {
        id: 12,
        vendor: 'Zulu',
        majorVersion: 17,
        version: '17.0.9',
        arch: 'x64',
        path: '/opt/jdks/zulu-17',
        managed: false,
        instances: [],
        refCount: 0,
        deleteBlocked: false,
        deleteBlockedReason: '',
      },
    ]
    const user = userEvent.setup()
    renderWithProviders(<NodeRuntimeWorkspace node={node()} sub="inventory" onSub={() => {}} />)
    await user.click(await screen.findByTestId('runtime-menu-jdk-12'))
    const item = await screen.findByTestId('runtime-menu-delete-jdk-12')
    expect(item).not.toHaveAttribute('aria-disabled', 'true')
  })

  it('就绪带的「安装 Node.js」跳到 Node 分段，不是 JDK 分段', async () => {
    /*
      【这条盯的是同一类错误的第二个入口】抽屉里曾经"点 Node 装 JDK"，
      就绪带上的动作按钮也犯过同样的错（installNode → jdk 分段）。
      两者是同一个 bug 的两处表现，所以两处都要有回归防线。
    */
    const user = userEvent.setup()
    // 无 JDK 时 java 卡为 blocked、给出安装动作；这里换成"没有 Node"的场景触发 installNode。
    state.runtimes = []
    state.jdks = [{ id: 1, vendor: 'Temurin', majorVersion: 21, version: '21.0.4', arch: 'x64', path: '/opt/jdk', managed: true, refCount: 0, instances: [] }]
    renderWithProviders(<NodeRuntimeWorkspace node={node()} sub="inventory" onSub={vi.fn()} />)

    const action = await screen.findByTestId('runtime-readiness-action-node')
    await user.click(action)
    // 抽屉打开后应停在 Node 段：出现 Node 安装提交按钮，而不是 JDK 的安装表单。
    expect(await screen.findByTestId('node-install-submit')).toBeInTheDocument()
    expect(screen.queryByTestId('jdk-install-submit')).not.toBeInTheDocument()
  })

  it('点击二级 sub 上报切换；切到 log 时才开始取日志运行时', async () => {
    const onSub = vi.fn()
    const { rerender } = renderWithProviders(
      <NodeRuntimeWorkspace node={node()} sub="inventory" onSub={onSub} />,
    )
    // 停在 inventory 时日志运行时查询是关的（改造前无条件 10s 轮询）。
    expect(state.logEnabled).toBe(false)

    const user = userEvent.setup()
    await user.click(screen.getByTestId('runtime-sub-log'))
    expect(onSub).toHaveBeenCalledWith('log')

    rerender(<NodeRuntimeWorkspace node={node()} sub="log" onSub={onSub} />)
    await waitFor(() => expect(state.logEnabled).toBe(true))
    // 日志 sub 渲染单实例卡片；冷热双层（HOT/COLD/Rehydrate）已废除，不应再出现分层节点。
    expect(await screen.findByTestId('log-runtime-flow')).toBeInTheDocument()
    expect(screen.queryByTestId('log-layer-hot')).not.toBeInTheDocument()
    expect(screen.queryByTestId('log-layer-cold')).not.toBeInTheDocument()
    expect(screen.queryByTestId('log-layer-rehydrate')).not.toBeInTheDocument()
  })

  it('有受管实例时渲染单实例卡片（含启停与容量），不出现分层', async () => {
    state.logRuntime = {
      instances: [
        {
          namespace: 'hot',
          state: 'RUNNING',
          health_ok: true,
          query_ready: true,
          listen_addr: '127.0.0.1:9428',
          asset_tag: 'v1.52.0',
        },
      ],
    }
    renderWithProviders(<NodeRuntimeWorkspace node={node()} sub="log" onSub={() => {}} />)
    const card = await screen.findByTestId('log-runtime-instance')
    expect(within(card).getByTestId('log-runtime-state')).toHaveTextContent('RUNNING')
    expect(within(card).getByTestId('log-runtime-health')).toBeInTheDocument()
    expect(within(card).getByTestId('log-runtime-queryready')).toBeInTheDocument()
    expect(within(card).getByTestId('log-runtime-start')).toBeDisabled() // 已在运行
    expect(within(card).getByTestId('log-runtime-stop')).toBeEnabled()
  })

  it('无实例时给出可执行的下一步，而不是空表格', async () => {
    state.logRuntime = { instances: [] }
    renderWithProviders(<NodeRuntimeWorkspace node={node()} sub="log" onSub={() => {}} />)
    expect(await screen.findByText('本节点尚未启用日志平台运行时')).toBeInTheDocument()
  })

  it('采集规则 sub 只放摘要与编辑按钮，原本零引用的 NodeLogAcqPanel 收进弹窗', async () => {
    const user = userEvent.setup()
    renderWithProviders(<NodeRuntimeWorkspace node={node()} sub="logAcq" onSub={() => {}} />)

    // 页面上常驻的是摘要与入口，不是规则表本体。
    expect(screen.getByTestId('runtime-logacq-summary')).toBeInTheDocument()
    expect(screen.getByTestId('runtime-logacq-edit')).toBeInTheDocument()
    expect(screen.queryByTestId('log-acq-panel')).not.toBeInTheDocument()
    expect(screen.queryByTestId('runtime-logacq-dialog')).not.toBeInTheDocument()

    // 点开才出现——原本零引用的面板现在经弹窗可达。
    await user.click(screen.getByTestId('runtime-logacq-edit'))
    const dialog = await screen.findByTestId('runtime-logacq-dialog')
    const panel = within(dialog).getByTestId('log-acq-panel')
    expect(panel).toBeInTheDocument()
    expect(panel.dataset.active).toBe('true')
  })

  it('包管理器改成摘要 + 编辑按钮：弹窗打开才渲染 NodePMConfigSection', async () => {
    state.pm = 'pnpm'
    state.registries = [{ url: 'https://registry.npmmirror.com' }, { url: 'https://a.example', scope: '@a' }]
    const user = userEvent.setup()
    renderWithProviders(<NodeRuntimeWorkspace node={node()} sub="packages" onSub={() => {}} />)

    // 摘要常驻：不用开弹窗也知道当前 pm 与配了几个源。
    const summary = screen.getByTestId('runtime-pm-summary')
    expect(summary).toHaveTextContent('pnpm')
    expect(summary).toHaveTextContent('2')

    // 弹窗关着时表单不在文档里（不渲染 = 不发请求）。
    expect(screen.queryByTestId('runtime-pm-dialog')).not.toBeInTheDocument()
    expect(screen.queryByRole('textbox', { name: 'registry 地址' })).not.toBeInTheDocument()

    await user.click(screen.getByTestId('runtime-pm-edit'))
    const dialog = await screen.findByTestId('runtime-pm-dialog')
    // NodePMConfigSection 的标志性元素：PM 选择与 registry 输入框。
    expect(within(dialog).getByText('包管理器与下载源')).toBeInTheDocument()
    expect(within(dialog).getAllByRole('textbox', { name: 'registry 地址' })).toHaveLength(2)
    expect(within(dialog).getByRole('button', { name: '保存配置' })).toBeInTheDocument()
  })

  it('弹窗关闭后编辑内容不渲染（关闭即停止取数）', async () => {
    const user = userEvent.setup()
    renderWithProviders(<NodeRuntimeWorkspace node={node()} sub="packages" onSub={() => {}} />)

    await user.click(screen.getByTestId('runtime-pm-edit'))
    expect(await screen.findByTestId('runtime-pm-dialog')).toBeInTheDocument()

    // 关闭：编辑内容必须整个消失，不能只是藏起来继续挂载（否则后台仍在轮询）。
    // 关闭键文案取自 common.close（中文「关闭」），不用 Escape —— 后者依赖焦点落在 content 内。
    await user.click(screen.getByRole('button', { name: '关闭' }))
    await waitFor(() => expect(screen.queryByTestId('runtime-pm-dialog')).not.toBeInTheDocument())
    expect(screen.queryByRole('textbox', { name: 'registry 地址' })).not.toBeInTheDocument()
    // 摘要仍在：收起的是表单，不是状态。
    expect(screen.getByTestId('runtime-pm-summary')).toBeInTheDocument()
  })

  it('日志平台运行时是常驻状态卡，不进弹窗', async () => {
    state.logRuntime = {
      instances: [
        {
          namespace: 'hot',
          state: 'RUNNING',
          health_ok: true,
          query_ready: true,
          listen_addr: '127.0.0.1:9428',
          asset_tag: 'v1.52.0',
        },
      ],
    }
    renderWithProviders(<NodeRuntimeWorkspace node={node()} sub="log" onSub={() => {}} />)

    // 状态卡直接在页面上（排障入口不能藏在弹窗后面）。
    const flow = await screen.findByTestId('log-runtime-flow')
    expect(flow).toBeInTheDocument()
    // 它不在任何弹窗内容容器内。
    expect(screen.queryByTestId('runtime-pm-dialog')).not.toBeInTheDocument()
    expect(screen.queryByTestId('runtime-logacq-dialog')).not.toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    // 本 sub 也不该出现采集面板（规则属于 logAcq 的弹窗）。
    expect(screen.queryByTestId('log-acq-panel')).not.toBeInTheDocument()
  })

  it('节点离线：就绪带把可用项压成 blocked 并说明原因', async () => {
    state.jdks = [
      { id: 11, vendor: 'Temurin', majorVersion: 21, version: '21.0.4', arch: 'x64', path: '/opt/j', managed: true, instances: [], refCount: 0, deleteBlocked: false, deleteBlockedReason: '' },
    ]
    renderWithProviders(<NodeRuntimeWorkspace node={node({ status: 0 })} sub="inventory" onSub={() => {}} />)
    const card = await screen.findByTestId('runtime-readiness-java')
    expect(card.dataset.level).toBe('blocked')
    expect(within(card).getByText(/节点离线/)).toBeInTheDocument()
  })
})
