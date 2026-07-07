import { describe, it, expect, beforeEach } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen } from '@testing-library/react'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@/mocks/server'
import { API } from '@/mocks/api'
import NodePortsPanel from './NodePortsPanel'

beforeEach(() => {
  loginMockUser()
})

describe('NodePortsPanel（FR-032 端口占用）', () => {
  it('渲染节点端口分配范围与 proxy/backend 占用行', async () => {
    server.use(
      http.get(API('/nodes/42/ports'), () =>
        HttpResponse.json({
          nodeId: 42,
          ranges: { serverPortBase: 25565, rangeSize: 2000 },
          occupied: [
            { instanceId: 1, name: 'survival-proxy', role: 'proxy', serverPort: 25565, queryPort: 0 },
            { instanceId: 2, name: 'survival-lobby', role: 'backend', serverPort: 25566, queryPort: 25566 },
          ],
        }),
      ),
    )

    renderWithProviders(<NodePortsPanel nodeId={42} />)

    expect(await screen.findByText('分配范围：server 25565+（每段 2000 个）')).toBeInTheDocument()
    expect(screen.getByText('实例')).toBeInTheDocument()
    expect(screen.getByText('角色')).toBeInTheDocument()
    expect(screen.getByText('监听端口')).toBeInTheDocument()
    expect(screen.getByText('survival-proxy')).toBeInTheDocument()
    expect(screen.getByText('survival-lobby')).toBeInTheDocument()
    expect(screen.getByText('代理')).toBeInTheDocument()
    expect(screen.getByText('后端')).toBeInTheDocument()
    expect(screen.getByText('25565')).toBeInTheDocument()
    expect(screen.getAllByText('25566').length).toBeGreaterThanOrEqual(1)
  })
})
