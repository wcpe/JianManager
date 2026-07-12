import { describe, it, expect, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import { useConsoleStore } from '@/stores/console'
import CommandPalette from './CommandPalette'

const instance = {
  id: 42,
  uuid: 'i-service-search',
  nodeId: 2,
  name: 'service-search-result',
  type: 'minecraft_java',
  role: 'backend',
  processType: 'daemon',
  status: 'RUNNING',
  startCommand: 'java -jar server.jar',
  workDir: '/servers/service-search-result',
  image: '',
  cpuLimit: 0,
  memLimitMb: 0,
  diskLimitMb: 0,
  serverPort: 25565,
  autoStart: false,
  autoRestart: true,
  tags: '[]',
  createdAt: '2026-01-01T00:00:00Z',
}

describe('CommandPalette DOM', () => {
  beforeEach(() => {
    loginMockUser()
    useConsoleStore.setState({ commandPaletteOpen: true, selectedNodeId: null })
  })

  it('输入关键字走服务端实例搜索，并跳转到 /instances/:id', async () => {
    const user = userEvent.setup()
    const seen: Record<string, string>[] = []
    server.use(
      http.get(API('/instances/search'), ({ request }) => {
        const params = Object.fromEntries(new URL(request.url).searchParams.entries())
        seen.push(params)
        return HttpResponse.json({ items: params.q === 'service' ? [instance] : [], total: params.q === 'service' ? 1 : 0, page: 1, pageSize: 8 })
      }),
    )

    renderWithProviders(<CommandPalette />, { route: '/' })
    await user.clear(screen.getByLabelText('搜索实例 / 节点 / 页面 / 操作…'))
    await user.type(screen.getByLabelText('搜索实例 / 节点 / 页面 / 操作…'), 'service')

    expect(await screen.findByText('service-search-result')).toBeInTheDocument()
    await waitFor(() => expect(seen.some((p) => p.q === 'service' && p.pageSize === '8')).toBe(true))

    await user.click(screen.getByText('service-search-result'))
    expect(window.location.pathname).toBe('/instances/42')
  })

  it('节点作用域只约束实例搜索结果，不约束节点结果', async () => {
    const user = userEvent.setup()
    useConsoleStore.setState({ commandPaletteOpen: true, selectedNodeId: 2 })
    const seen: Record<string, string>[] = []
    server.use(
      http.get(API('/instances/search'), ({ request }) => {
        seen.push(Object.fromEntries(new URL(request.url).searchParams.entries()))
        return HttpResponse.json({ items: [], total: 0, page: 1, pageSize: 8 })
      }),
    )

    renderWithProviders(<CommandPalette />, { route: '/' })
    await user.clear(screen.getByLabelText('搜索实例 / 节点 / 页面 / 操作…'))
    await user.type(screen.getByLabelText('搜索实例 / 节点 / 页面 / 操作…'), 'alpha')

    expect(await screen.findByText('alpha')).toBeInTheDocument()
    await waitFor(() => expect(seen.some((p) => p.q === 'alpha' && p.nodeId === '2')).toBe(true))
  })
})
