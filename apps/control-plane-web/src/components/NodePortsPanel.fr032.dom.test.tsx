import { describe, it, expect, beforeEach } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
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

  it('实例名过滤收敛端口行', async () => {
    const user = userEvent.setup()
    server.use(
      http.get(API('/nodes/42/ports'), () =>
        HttpResponse.json({
          nodeId: 42,
          ranges: { serverPortBase: 25565, rangeSize: 2000 },
          occupied: [
            { instanceId: 1, name: 'survival-proxy', role: 'proxy', serverPort: 25565, queryPort: 0 },
            { instanceId: 2, name: 'creative-lobby', role: 'backend', serverPort: 25566, queryPort: 25566 },
          ],
        }),
      ),
    )
    renderWithProviders(<NodePortsPanel nodeId={42} />)
    await screen.findByText('survival-proxy')

    // 过滤 'creative' → 仅 creative-lobby 命中，survival-proxy 被过滤掉。
    await user.type(screen.getByRole('searchbox'), 'creative')
    await waitFor(() => {
      expect(screen.queryByText('survival-proxy')).not.toBeInTheDocument()
    })
    expect(screen.getByText('creative-lobby')).toBeInTheDocument()
  })

  it('大量端口占用只挂可视窗口（虚拟化后 DOM 行受限）', async () => {
    const occupied = Array.from({ length: 300 }, (_, i) => ({
      instanceId: i + 1,
      name: `srv-${i + 1}`,
      role: 'backend',
      serverPort: 25565 + i,
      queryPort: 0,
    }))
    server.use(
      http.get(API('/nodes/42/ports'), () =>
        HttpResponse.json({ nodeId: 42, ranges: { serverPortBase: 25565, rangeSize: 2000 }, occupied }),
      ),
    )
    renderWithProviders(<NodePortsPanel nodeId={42} />)

    const surface = await screen.findByTestId('node-ports-virtual')
    await screen.findByText('srv-1')
    // 虚拟化：数据行（含名称单元格）远少于 300；只挂可视窗口 + overscan。
    const dataRows = within(surface)
      .getAllByRole('row')
      .filter((r) => within(r).queryByText(/^srv-\d+$/))
    expect(dataRows.length).toBeLessThan(50)
  })
})
