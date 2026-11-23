import { describe, expect, it, vi } from 'vitest'
import { act, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useStartInstance } from '@/api/instances'
import { http, HttpResponse } from 'msw'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import type { InstanceInfo } from '@/lib/instances/instance-types'
import { InstanceWorktableCard } from './InstanceWorktableCard'

const instance: InstanceInfo = {
  id: 42, uuid: 'card-metrics-live', nodeId: 1, name: 'survival-card',
  type: 'minecraft_java', role: 'backend', processType: 'daemon', status: 'RUNNING',
  startCommand: 'java -jar server.jar', workDir: '/srv/survival-card', serverPort: 25565,
  autoStart: false, autoRestart: false, tags: null, createdAt: '2026-01-01T00:00:00Z',
}

describe('InstanceWorktableCard 应用接线', () => {
  it('运行态从指标接口取得真实四项读数，并透传节点主机地址', async () => {
    const fetchMetrics = vi.fn(() => HttpResponse.json({
      cpuPercent: 18, memoryMb: 1536, heapMaxMb: 4096,
      onlinePlayers: 0, maxPlayers: 100, tps: 19.9,
      playersAvailable: true, maxPlayersAvailable: true, probeAvailable: true,
    }))
    server.use(http.get(API('/instances/42/metrics'), fetchMetrics))
    loginMockUser()
    renderWithProviders(
      <InstanceWorktableCard inst={instance} nodeName="host-a" nodeHost="10.0.0.42" roleBadge={null} menu={null} />,
    )

    expect(await screen.findByText('18%')).toBeInTheDocument()
    expect(screen.getByText('1.5 GiB / 4.0 GiB')).toBeInTheDocument()
    expect(screen.getByText('0 / 100')).toBeInTheDocument()
    expect(screen.getByText('19.9')).toBeInTheDocument()
    expect(screen.getByText('10.0.0.42:25565')).toBeInTheDocument()
    expect(fetchMetrics).toHaveBeenCalledTimes(1)
  })

  it('停机态不向 Worker 轮询指标，保留缺测占位', () => {
    const fetchMetrics = vi.fn(() => HttpResponse.json({ cpuPercent: 99 }))
    server.use(http.get(API('/instances/42/metrics'), fetchMetrics))
    loginMockUser()
    renderWithProviders(
      <InstanceWorktableCard inst={{ ...instance, status: 'STOPPED' }} nodeName="host-a" roleBadge={null} menu={null} />,
    )

    expect(screen.getAllByText('--').length).toBeGreaterThanOrEqual(2)
    expect(screen.queryByText('99%')).not.toBeInTheDocument()
    expect(fetchMetrics).not.toHaveBeenCalled()
  })
})

function LifecycleHarness() {
  const start = useStartInstance()
  return <InstanceWorktableCard inst={{ ...instance, status: 'STOPPED' }} nodeName="host-a" roleBadge={null} menu={null} onStart={() => start.mutate(42)} />
}

describe('InstanceWorktableCard 共享 mutation 状态', () => {
  it('页面级启动请求未返回时，卡片立即显示提交中并禁重复点击', async () => {
    const user = userEvent.setup()
    let release!: () => void
    const gate = new Promise<void>((resolve) => { release = resolve })
    const postStart = vi.fn(async () => { await gate; return HttpResponse.json({}) })
    server.use(http.post(API('/instances/42/start'), postStart))
    loginMockUser()
    renderWithProviders(<LifecycleHarness />)
    try {
      await user.click(screen.getByRole('button', { name: '启动' }))
      expect(await screen.findByText('启动请求提交中')).toBeInTheDocument()
      expect(screen.getByRole('button', { name: '启动' })).toBeDisabled()
      await user.click(screen.getByRole('button', { name: '启动' }))
      expect(postStart).toHaveBeenCalledTimes(1)
    } finally {
      await act(async () => { release(); await gate })
    }
    await waitFor(() => expect(screen.getByRole('button', { name: '启动' })).toBeEnabled())
  })
})
