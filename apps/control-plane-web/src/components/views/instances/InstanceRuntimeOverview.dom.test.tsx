import { describe, expect, it } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import type { InstanceInfo } from '@/lib/instances/instance-types'
import type { InstanceMetricSnapshot } from './InstanceRowMetrics'
import { InstanceRuntimeOverview } from './InstanceRuntimeOverview'

function instance(id: number, status = 'RUNNING', role = 'backend'): InstanceInfo {
  return {
    id, uuid: `instance-${id}`, nodeId: 1, name: `server-${id}`, status, role,
    type: 'minecraft_java', processType: 'daemon',
    startCommand: '', workDir: '', serverPort: 25565, tags: [],
    autoStart: false, autoRestart: false, createdAt: '2026-11-01T00:00:00Z',
  }
}

function sample(overrides: Partial<InstanceMetricSnapshot> = {}): InstanceMetricSnapshot {
  return { cpuPct: null, memUsedBytes: null, players: null, playersMax: null, tps: null, heartbeat: [], ...overrides }
}

const counts = { total: 20, running: 12, stopped: 6, crashed: 1 }

describe('服务器运行概览', () => {
  it('状态图使用全范围聚合，指标只汇总本页有效运行样本并避免代理重复人数', () => {
    const instances = [instance(1), instance(2), instance(3, 'STOPPED'), instance(4, 'RUNNING', 'proxy')]
    const metrics = new Map([
      ['instance-1', sample({ cpuPct: 20, memUsedBytes: 1024, players: 7, tps: 19 })],
      ['instance-3', sample({ cpuPct: 99, memUsedBytes: 99999, players: 99, tps: 0 })],
      ['instance-4', sample({ cpuPct: 10, memUsedBytes: 2048, players: 7, tps: 20 })],
    ])
    renderWithProviders(<InstanceRuntimeOverview counts={counts} instances={instances} metrics={metrics} />)

    expect(screen.getByRole('img', { name: /共 20 台，活动 12 台，停止 6 台，异常 1 台/ })).toBeInTheDocument()
    expect(within(screen.getByTestId('instances-overview-cpu')).getByText('15.0%')).toBeInTheDocument()
    expect(within(screen.getByTestId('instances-overview-cpu')).getByText('已采样 2/3 台')).toBeInTheDocument()
    expect(within(screen.getByTestId('instances-overview-memory')).getByText('3K')).toBeInTheDocument()
    expect(within(screen.getByTestId('instances-overview-players')).getByText('7')).toBeInTheDocument()
    expect(within(screen.getByTestId('instances-overview-players')).getByText('已采样 1/2 台')).toBeInTheDocument()
    expect(within(screen.getByTestId('instances-overview-tps')).getByText('19.0')).toBeInTheDocument()
  })

  it('无采样不报零，合法的零人数和零CPU正常展示', () => {
    const inst = instance(1)
    const { rerender } = renderWithProviders(<InstanceRuntimeOverview counts={counts} instances={[inst]} metrics={new Map()} />)
    expect(within(screen.getByTestId('instances-overview-cpu')).getByText('--')).toBeInTheDocument()
    expect(within(screen.getByTestId('instances-overview-players')).getByText('--')).toBeInTheDocument()
    rerender(<InstanceRuntimeOverview counts={counts} instances={[inst]} metrics={new Map([[inst.uuid, sample({ cpuPct: 0, players: 0, tps: Number.NaN })]])} />)
    expect(within(screen.getByTestId('instances-overview-cpu')).getByText('0.0%')).toBeInTheDocument()
    expect(within(screen.getByTestId('instances-overview-players')).getByText('0')).toBeInTheDocument()
    expect(within(screen.getByTestId('instances-overview-tps')).getByText('--')).toBeInTheDocument()
  })

  it('图表默认展开，用户可以收起以给卡片留空间', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstanceRuntimeOverview counts={counts} instances={[]} metrics={new Map()} />)
    const toggle = screen.getByRole('button', { name: '服务器运行概览' })
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    await user.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByRole('img')).not.toBeInTheDocument()
  })
})
