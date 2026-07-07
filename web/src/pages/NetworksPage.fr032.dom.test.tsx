import { describe, it, expect, beforeEach } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@/mocks/server'
import NetworksPage from './NetworksPage'

const instances = [
  { id: 10, uuid: 'i-survival-proxy', nodeId: 1, name: 'survival-proxy', type: 'minecraft_proxy', role: 'proxy', processType: 'daemon', status: 'RUNNING', startCommand: '', workDir: '/servers/survival-proxy', serverPort: 25570, autoStart: true, autoRestart: true, tags: '[]', createdAt: '2026-06-01T00:00:00Z' },
  { id: 11, uuid: 'i-survival-lobby', nodeId: 1, name: 'survival-lobby', type: 'minecraft_java', role: 'backend', processType: 'daemon', status: 'RUNNING', startCommand: '', workDir: '/servers/survival-lobby', serverPort: 25566, autoStart: true, autoRestart: true, tags: '[]', createdAt: '2026-06-01T00:00:00Z' },
  { id: 12, uuid: 'i-survival-world', nodeId: 2, name: 'survival-world', type: 'minecraft_java', role: 'backend', processType: 'daemon', status: 'CRASHED', startCommand: '', workDir: '/servers/survival-world', serverPort: 25567, autoStart: false, autoRestart: true, tags: '[]', createdAt: '2026-06-01T00:00:00Z' },
  { id: 20, uuid: 'i-creative-proxy', nodeId: 1, name: 'creative-proxy', type: 'minecraft_proxy', role: 'proxy', processType: 'daemon', status: 'RUNNING', startCommand: '', workDir: '/servers/creative-proxy', serverPort: 25580, autoStart: true, autoRestart: true, tags: '[]', createdAt: '2026-06-01T00:00:00Z' },
]

beforeEach(() => {
  loginMockUser()
  server.use(
    http.get(/\/api\/v1\/instances(\?.*)?$/, ({ request }) => {
      const role = new URL(request.url).searchParams.get('role')
      return HttpResponse.json(role ? instances.filter((i) => i.role === role) : instances)
    }),
  )
})

describe('NetworksPage（FR-032 群组服关系模型）', () => {
  it('管理 Network 软标签成员并执行批量操作，证明成员非独占且可按标签运维', async () => {
    const user = userEvent.setup()
    renderWithProviders(<NetworksPage />, { route: '/networks' })

    expect(await screen.findByText('survival')).toBeInTheDocument()
    expect(screen.getByText('3 个成员')).toBeInTheDocument()
    await user.click(screen.getAllByRole('button', { name: '管理' })[0])

    expect(await screen.findByRole('heading', { name: 'survival' })).toBeInTheDocument()
    expect(screen.getByText('survival-proxy')).toBeInTheDocument()
    expect(screen.getByText('survival-lobby')).toBeInTheDocument()
    expect(screen.getByText('survival-world')).toBeInTheDocument()

    await user.click(await screen.findByRole('checkbox', { name: 'creative-proxy' }))
    await user.click(screen.getByRole('button', { name: '加入所选 (1)' }))
    await waitFor(() => expect(screen.getAllByText('4 个成员').length).toBeGreaterThan(0))

    await user.click(screen.getByRole('button', { name: '全部停止' }))
    await waitFor(() => expect(screen.getAllByText('停止').length).toBeGreaterThanOrEqual(4))
  })

  it('拓扑视图渲染 proxy↔backend M:N 注册关系', async () => {
    renderWithProviders(<NetworksPage />, { route: '/networks/topology' })

    expect(await screen.findByText('群组服拓扑')).toBeInTheDocument()
    expect(await screen.findByText('survival-proxy')).toBeInTheDocument()
    expect(screen.getByRole('img', { name: '群组服拓扑' })).toBeInTheDocument()
    expect(screen.getByText('creative-proxy')).toBeInTheDocument()
    expect(screen.getByText('survival-lobby')).toBeInTheDocument()
    expect(screen.getByText('survival-world')).toBeInTheDocument()
  })
})
