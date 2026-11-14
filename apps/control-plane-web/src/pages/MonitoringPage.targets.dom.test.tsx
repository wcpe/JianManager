import React, { type ComponentProps } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, useLocation, useNavigate } from 'react-router'
import type { NodeInfo } from '@/api/nodes'
import type { ProcessTopItem } from '@/api/metrics'
import type MonitoringView from '@/components/views/instances/MonitoringPage'
import type { DrillTargetPicker as TargetPicker } from '@/components/charts/DrillTargetPicker'

type ViewProps = ComponentProps<typeof MonitoringView>
type PickerProps = ComponentProps<typeof TargetPicker>

const state = vi.hoisted(() => ({
  nodes: [{ id: 7, uuid: 'node-n', name: 'node N' }] as Pick<NodeInfo, 'id' | 'uuid' | 'name'>[] | undefined,
}))
const globalProcess: ProcessTopItem = {
  instanceId: 101, instanceUuid: 'instance-a', nodeUuid: 'node-n', pid: 1234,
  name: 'global-java', cpuPercent: 10, rssBytes: 1024, readBytesPerSec: 0,
  writeBytesPerSec: 0, user: 'minecraft', commandSummary: 'server.jar', sampledAt: '2026-11-14T00:00:00Z',
}

vi.mock('@/lib/assistant/page-context', () => ({ usePageContext: () => {} }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/nodes', () => ({ useNodes: () => ({ data: state.nodes }) }))
vi.mock('@/api/client', () => ({
  default: {
    get: vi.fn(async (path: string, options?: { params?: { uuid?: string } }) => {
      if (path.startsWith('/metrics/processes/top?')) return { data: [globalProcess] }
      const uuid = options?.params?.uuid
      return {
        data: {
          items: uuid && uuid !== 'missing' ? [{ id: uuid === 'instance-a' ? 101 : 202, uuid }] : [],
          total: uuid && uuid !== 'missing' ? 1 : 0, page: 1, pageSize: 1,
        },
      }
    }),
  },
}))
// 保留真实 useProcessTop 与 QueryClient 缓存语义；enabled=false 仍可能读到同 key 的数据/错误。
vi.mock('@/api/metrics', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/metrics')>()
  return {
    ...actual,
    useProcessTop: vi.fn(actual.useProcessTop),
    useManagedProcessDetail: vi.fn(() => ({ data: undefined, refetch: () => {}, isError: false, isFetching: false })),
    useManagedProcessAction: () => ({ isPending: false, mutateAsync: async () => {} }),
    useBotRuntimeMetrics: () => ({}),
  }
})
vi.mock('@/components/charts/use-target-series', () => ({
  useTargetSeries: () => ({ series: [], isLoading: false, isError: false }),
}))
vi.mock('@/components/charts/DrillTargetPicker', () => ({ DrillTargetPicker: vi.fn(() => null) }))
vi.mock('@/components/metrics/InstanceRankingPanel', () => ({ InstanceRankingPanel: () => null }))
vi.mock('@/components/metrics/CapacityForecastCard', () => ({ CapacityForecastCard: () => null }))
vi.mock('@/components/metrics/SLOSection', () => ({ SLOSection: () => null }))
vi.mock('@/components/views/instances/MonitoringPage', () => ({
  default: vi.fn((props: ViewProps) => (
    <>
      {props.drillSlot}
      {props.processTop.map((row) => (
        <button key={`${row.nodeUuid}:${row.instanceUuid}:${row.pid}`} onClick={() => props.onSelectedProcessChange(row)}>
          Inspect {row.name}
        </button>
      ))}
      {props.selectedProcess && <div role="dialog" aria-label="process detail">
        {props.selectedProcess.pid}
        <button onClick={() => props.onPendingActionChange('terminate')}>Terminate</button>
      </div>}
      {props.pendingAction && <div role="dialog" aria-label="confirm action">Confirm {props.pendingAction}</div>}
    </>
  )),
}))

import MonitoringPage from '@/pages/MonitoringPage'
import View from '@/components/views/instances/MonitoringPage'
import { DrillTargetPicker } from '@/components/charts/DrillTargetPicker'
import { useManagedProcessDetail, useProcessTop } from '@/api/metrics'
import api from '@/api/client'

const clients: QueryClient[] = []
const view = () => vi.mocked(View).mock.calls.at(-1)![0]
const processParams = () => vi.mocked(useProcessTop).mock.calls.at(-1)![0]
const pick = (next: PickerProps['target']) => act(() => vi.mocked(DrillTargetPicker).mock.calls.at(-1)![0].onChange(next))

function Probe() {
  const navigate = useNavigate()
  const location = useLocation()
  return <>
    <output data-testid="location">{location.pathname}{location.search}</output>
    <button onClick={() => navigate('/monitor?node=missing')}>Unknown node</button>
    <button onClick={() => navigate('/monitor?instance=missing')}>Unresolved instance</button>
    <button onClick={() => navigate('/monitor?instance=instance-b')}>Instance B</button>
    <button onClick={() => navigate('/monitor?node=node-n')}>Node N</button>
    <button onClick={() => navigate(-1)}>Back</button>
    <button onClick={() => navigate(1)}>Forward</button>
    <MonitoringPage />
  </>
}
function mount(route: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  clients.push(client)
  render(
    <MemoryRouter initialEntries={[route]}>
      <QueryClientProvider client={client}><Probe /></QueryClientProvider>
    </MemoryRouter>,
  )
  return client
}
function openAction() {
  fireEvent.click(screen.getByRole('button', { name: 'Inspect global-java' }))
  fireEvent.click(screen.getByRole('button', { name: 'Terminate' }))
  expect(screen.getByRole('dialog', { name: 'confirm action' })).toBeInTheDocument()
}
function expectProcessCleared() {
  expect(view().selectedProcess).toBeNull()
  expect(view().pendingAction).toBeNull()
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  expect(vi.mocked(useManagedProcessDetail).mock.calls.at(-1)).toEqual([undefined, undefined, false])
}

afterEach(() => {
  cleanup()
  clients.splice(0).forEach((client) => client.clear())
  state.nodes = [{ id: 7, uuid: 'node-n', name: 'node N' }]
  vi.clearAllMocks()
})

describe('MonitoringPage 目标与范围（回归）', () => {
  it('切到平台后不再带着已停用实例查询的旧 instanceId', async () => {
    mount('/monitor?instance=instance-a')
    await waitFor(() => expect(processParams().instanceId).toBe(101))
    pick({ kind: 'platform' })
    expect(view().target.kind).toBe('platform')
    expect(processParams().instanceId).toBeUndefined()
    expect(processParams().nodeId).toBeUndefined()
  })

  it('选择器切换实体清理弹窗、按 kind 重置比较指标并更新 URL', async () => {
    mount('/monitor?instance=instance-a&range=24h')
    await screen.findByRole('button', { name: 'Inspect global-java' })
    openAction()
    pick({ kind: 'node', uuid: 'node-n' })
    expect(screen.getByTestId('location')).toHaveTextContent('/monitor?range=24h&node=node-n')
    expect(view().target).toEqual({ kind: 'node', uuid: 'node-n' })
    expect(view().compareSel).toEqual(['node_cpu_pct'])
    expectProcessCleared()
  })

  it('浏览器导航、后退和前进切换实体时均清理旧弹窗，并按 kind 重置比较指标', async () => {
    mount('/monitor?instance=instance-a')
    await screen.findByRole('button', { name: 'Inspect global-java' })
    act(() => view().onCompareToggle('inst_msp'))
    openAction()
    fireEvent.click(screen.getByRole('button', { name: 'Node N' }))
    expect(view().target).toEqual({ kind: 'node', uuid: 'node-n' })
    expectProcessCleared()
    expect(view().compareSel).toEqual(['node_cpu_pct'])
    await screen.findByRole('button', { name: 'Inspect global-java' })
    openAction()
    fireEvent.click(screen.getByRole('button', { name: 'Back' }))
    expect(view().target).toEqual({ kind: 'instance', uuid: 'instance-a' })
    expectProcessCleared()
    expect(view().compareSel).toEqual(['inst_tps'])
    await screen.findByRole('button', { name: 'Inspect global-java' })
    openAction()
    fireEvent.click(screen.getByRole('button', { name: 'Forward' }))
    expect(view().target).toEqual({ kind: 'node', uuid: 'node-n' })
    expectProcessCleared()
    expect(view().compareSel).toEqual(['node_cpu_pct'])
  })

  it('同 kind 的实例导航清理旧弹窗但保留比较选择', async () => {
    mount('/monitor?instance=instance-a')
    await screen.findByRole('button', { name: 'Inspect global-java' })
    act(() => view().onCompareToggle('inst_msp'))
    const compare = view().compareSel
    openAction()
    fireEvent.click(screen.getByRole('button', { name: 'Instance B' }))
    expect(view().target).toEqual({ kind: 'instance', uuid: 'instance-b' })
    expectProcessCleared()
    expect(view().compareSel).toEqual(compare)
  })

  it('同实例切换 world 保留进程选择、处置弹窗、比较指标和来源节点', async () => {
    mount('/monitor?instance=instance-a')
    await screen.findByRole('button', { name: 'Inspect global-java' })
    pick({ kind: 'instance', uuid: 'instance-a', nodeUuid: 'node-n' })
    openAction()
    act(() => view().onCompareToggle('inst_msp'))
    const compare = view().compareSel
    pick({ kind: 'instance', uuid: 'instance-a', nodeUuid: 'node-n', world: 'world_nether' })
    expect(view().target).toEqual({ kind: 'instance', uuid: 'instance-a', nodeUuid: 'node-n', world: 'world_nether' })
    expect(view().selectedProcess).toEqual(globalProcess)
    expect(view().pendingAction).toBe('terminate')
    expect(view().compareSel).toEqual(compare)
    expect(screen.getByRole('dialog', { name: 'confirm action' })).toBeInTheDocument()
    expect(screen.getByTestId('location')).toHaveTextContent('/monitor?instance=instance-a')
  })

  it.each(['Unknown node', 'Unresolved instance'])('平台缓存预热后切到 %s 不展示或选中全局进程', async (destination) => {
    const client = mount('/monitor')
    await screen.findByRole('button', { name: 'Inspect global-java' })
    expect(client.getQueryData(['processTop', '', '', 'cpu', 10, 'latest'])).toEqual([globalProcess])
    openAction()
    const requestsBefore = vi.mocked(api.get).mock.calls.filter(([path]) => path.startsWith('/metrics/processes/top?')).length
    fireEvent.click(screen.getByRole('button', { name: destination }))
    await waitFor(() => expect(processParams().enabled).toBe(false))
    expect(view().processTop).toEqual([])
    expect(view().processTopError).toBe(false)
    expect(screen.queryByRole('button', { name: 'Inspect global-java' })).not.toBeInTheDocument()
    expectProcessCleared()
    expect(client.getQueryData(['processTop', '', '', 'cpu', 10, 'latest'])).toEqual([globalProcess])
    expect(vi.mocked(api.get).mock.calls.filter(([path]) => path.startsWith('/metrics/processes/top?'))).toHaveLength(requestsBefore)
  })

  it('节点列表尚未加载时屏蔽预热的平台缓存及其错误状态', async () => {
    const client = mount('/monitor')
    await screen.findByRole('button', { name: 'Inspect global-java' })
    act(() => {
      client.getQueryCache().find({ queryKey: ['processTop', '', '', 'cpu', 10, 'latest'] })!.setState({
        status: 'error', error: new Error('global process request failed'),
      })
    })
    state.nodes = undefined
    fireEvent.click(screen.getByRole('button', { name: 'Node N' }))
    expect(processParams().enabled).toBe(false)
    expect(view().processTop).toEqual([])
    expect(view().processTopError).toBe(false)
    expect(screen.queryByRole('button', { name: 'Inspect global-java' })).not.toBeInTheDocument()
  })
})
