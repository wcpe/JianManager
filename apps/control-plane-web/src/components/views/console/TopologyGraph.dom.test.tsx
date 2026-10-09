import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import { TopologyGraph } from '@/components/views/console/TopologyGraph'
import type { TopologyGraphData } from '@/components/views/console/TopologyGraph'
import type { ProxyRegistration } from '@/lib/instances/proxy-registration'
import type { NodeInfo } from '@/lib/nodes/node-types'
import type { InstanceGroupNode } from '@/lib/instances/instance-group'

/**
 * 拓扑图 · 受控视图测（ADR-097）。
 *
 * 补的是**展示契约**：加载态、空态、节点/连线渲染、工具条控件、维度切换时的取数通知。
 * 取数（单条聚合请求消 N+1）、搜索过滤交互、viewBox 复位由应用侧
 * `TopologyGraph.dom.test.tsx` / `TopologyGraph.fr453.dom.test.tsx` 走假后端覆盖。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        common: { loading: '加载中…' },
        grouping: { zoneNone: '未分区' },
        networks: {
          topoTitle: '群组服拓扑',
          topoFitView: '适应视图',
          topoSearch: '搜索节点',
          topoOnlyMatch: '仅显示匹配',
          topoLayer: '层级',
          topoLevel: '等级',
          topoShowDisabled: '显示禁用线',
          topoNoNodes: '暂无实例',
          topoNoProxy: '暂无代理',
          topoNoBackend: '暂无后端',
          topoUngrouped: '未分组',
          topoMultiHomed: '属于多个群组',
          topoLoadCpu: 'CPU',
          topoLoadMem: '内存',
          topoLoadNodeTag: '节点',
          topoLoadNodeHint: '节点负载',
          healthRunning: '运行',
          healthCrashed: '崩溃',
          healthTransitioning: '过渡',
          healthStopped: '停止',
        },
      },
    },
  },
  interpolation: { escapeValue: false },
})

/** 构造一条注册关系（backend 摘要随注册回填）。 */
function registration(
  proxyId: number,
  backendId: number,
  over: { name?: string; status?: string; enabled?: boolean } = {},
): ProxyRegistration {
  return {
    id: backendId,
    proxyId,
    backendId,
    alias: '',
    priority: 0,
    forcedHost: '',
    restricted: false,
    enabled: over.enabled ?? true,
    backend: {
      id: backendId,
      name: over.name ?? `backend-${backendId}`,
      role: 'backend',
      nodeId: 1,
      serverPort: 25566,
      status: over.status ?? 'RUNNING',
    },
  }
}

function proxy(id: number, name: string, registrations: ProxyRegistration[] = []) {
  return { id, name, status: 'RUNNING', serverPort: 25565, nodeId: 1, registrations }
}

/** 默认数据：一个代理 + 一个已注册后端。 */
function data(over: Partial<TopologyGraphData> = {}): TopologyGraphData {
  const reg = registration(1, 2)
  return { proxies: [proxy(1, 'proxy-a', [reg])], networks: [], instances: [], ...over }
}

function renderGraph(props: Partial<Parameters<typeof TopologyGraph>[0]> = {}) {
  const merged = { data: data(), isLoading: false, ...props }
  render(
    <I18nextProvider i18n={testI18n}>
      <TopologyGraph {...(merged as Parameters<typeof TopologyGraph>[0])} />
    </I18nextProvider> as ReactNode,
  )
  return merged
}

describe('TopologyGraph（FR-145/335 拓扑图受控视图）', () => {
  it('加载态显示加载文案，不渲染视口。', () => {
    renderGraph({ isLoading: true })
    expect(screen.getByText('加载中…')).toBeInTheDocument()
    expect(screen.queryByRole('img')).not.toBeInTheDocument()
  })

  it('无任何实例时显示空态。', () => {
    renderGraph({ data: data({ proxies: [], instances: [] }) })
    expect(screen.getByText('暂无实例')).toBeInTheDocument()
  })

  it('渲染代理与已注册后端节点，并给出可访问的 SVG 名称。', () => {
    renderGraph()
    expect(screen.getByRole('img', { name: '群组服拓扑' })).toBeInTheDocument()
    expect(screen.getByText('proxy-a')).toBeInTheDocument()
    expect(screen.getByText('backend-2')).toBeInTheDocument()
  })

  it('未注册实例作为孤立节点上拓扑（FR-453）。', () => {
    renderGraph({
      data: data({
        proxies: [],
        instances: [
          { id: 9, name: 'standalone-svc', status: 'STOPPED', serverPort: 0, nodeId: 1, role: 'universal', type: 'generic', tags: null },
        ],
      }),
    })
    expect(screen.getByText('standalone-svc')).toBeInTheDocument()
  })

  it('工具条提供适应视图、搜索、仅显示匹配、层级选择与禁用线开关。', () => {
    renderGraph()
    expect(screen.getByRole('button', { name: '适应视图' })).toBeInTheDocument()
    expect(screen.getByRole('textbox', { name: '搜索节点' })).toBeInTheDocument()
    expect(screen.getByLabelText('仅显示匹配')).toBeInTheDocument()
    expect(screen.getByLabelText('层级')).toBeInTheDocument()
    expect(screen.getByLabelText('显示禁用线')).toBeInTheDocument()
    // 状态筛选 pill：复用健康分布文案，初始均未选中。
    expect(screen.getByRole('button', { name: '运行' })).toHaveAttribute('aria-pressed', 'false')
  })

  it('默认 region 维度不请求分组树；切到 groupTree 时通知取数。', async () => {
    const user = userEvent.setup()
    const calls: boolean[] = []
    renderGraph({ onGroupTreeNeeded: (needed: boolean) => calls.push(needed) })
    // 挂载即通知一次（region → false）。
    expect(calls).toEqual([false])

    await user.selectOptions(screen.getByLabelText('层级'), 'groupTree')
    expect(calls.at(-1)).toBe(true)
  })

  it('传入分组树数据源后，groupTree 维度渲染分组分带标签。', async () => {
    const user = userEvent.setup()
    const groupNodes: InstanceGroupNode[] = [
      { id: 7, uuid: 'g7', name: '生产大区', parentId: null, sort: 0, instanceCount: 2, memberInstanceIds: [1, 2] },
    ]
    renderGraph({
      groupNodes,
      data: data({
        // 分组键映射按全量实例投影建立（FR-452），故补上两个实例。
        instances: [
          { id: 1, name: 'proxy-a', status: 'RUNNING', serverPort: 25565, nodeId: 1, role: 'proxy', type: 'minecraft_java', tags: null },
          { id: 2, name: 'backend-2', status: 'RUNNING', serverPort: 25566, nodeId: 1, role: 'backend', type: 'minecraft_java', tags: null },
        ],
      }),
    })
    await user.selectOptions(screen.getByLabelText('层级'), 'groupTree')
    expect(screen.getByText(/生产大区/)).toBeInTheDocument()
  })

  it('节点负载标签：在线且有数值才显示，离线节点不显示。', () => {
    const nodes: Pick<NodeInfo, 'id' | 'status' | 'cpuUsage' | 'memoryUsage'>[] = [
      { id: 1, status: 1, cpuUsage: 0.42, memoryUsage: 0.5 },
    ]
    renderGraph({ nodes })
    // 两个节点（proxy/backend）同属 node 1，各带一份负载标签。
    expect(screen.getAllByText(/CPU/).length).toBeGreaterThan(0)
    expect(screen.getAllByText(/内存/).length).toBeGreaterThan(0)
  })

  it('离线节点不渲染负载标签（不装 0）。', () => {
    const nodes: Pick<NodeInfo, 'id' | 'status' | 'cpuUsage' | 'memoryUsage'>[] = [
      { id: 1, status: 0, cpuUsage: 0.42, memoryUsage: 0.5 },
    ]
    renderGraph({ nodes })
    expect(screen.queryByText(/CPU/)).not.toBeInTheDocument()
  })
})
