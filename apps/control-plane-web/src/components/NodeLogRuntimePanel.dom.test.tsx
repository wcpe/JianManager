import { describe, expect, it } from 'vitest'
import { http, HttpResponse } from 'msw'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import NodeLogRuntimePanel from './NodeLogRuntimePanel'

describe('NodeLogRuntimePanel', () => {
  it('shows process health separately from query readiness and installs only a cached asset', async () => {
    loginMockUser()
    let installs = 0
    let migrations = 0
    let resolutions = 0
    server.use(
      http.get(API('/log-runtime/assets'), () => HttpResponse.json({ assets: [{ tag: 'v1.52.0', os: 'linux', arch: 'amd64', cached: true }] })),
      http.get(API('/nodes/1/log-runtime'), () => HttpResponse.json({ supported: true, instances: [
        { namespace: 'hot', state: 'RUNNING', port: 19441, listen_addr: '127.0.0.1:19441', asset_tag: 'v1.52.0', health_ok: true, query_ready: false },
        { namespace: 'cold', state: 'STOPPED', health_ok: false, query_ready: false },
      ] })),
      http.post(API('/nodes/1/log-runtime/install'), () => {
        installs++
        return HttpResponse.json({ state: 'installed' })
      }),
      http.post(API('/nodes/1/log-runtime/migrate'), async ({ request }) => {
        const body = await request.json() as { storageNamespace: string; utcDay: string }
        expect(body).toEqual({ storageNamespace: 'node:1', utcDay: '2026-09-23' })
        migrations++
        return HttpResponse.json({ state: 'migrated' })
      }),
      http.post(API('/nodes/1/log-runtime/ingest/resolve-gaps'), () => {
        resolutions++
        return HttpResponse.json({ state: 'resolved' })
      }),
    )
    const user = userEvent.setup()
    renderWithProviders(<NodeLogRuntimePanel nodeId={1} os="linux" arch="amd64" online />)
    expect(await screen.findByText(/审批包已缓存/)).toBeInTheDocument()
    expect(screen.getByText('进程健康')).toBeInTheDocument()
    expect(screen.queryByText('可查询')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '下发资产' }))
    await waitFor(() => expect(installs).toBe(1))
    // aria-label 更新为「迁移日期（该日分区）」，与新增的问号解释文案一致。
    fireEvent.change(screen.getByLabelText('迁移日期（该日分区）'), { target: { value: '2026-09-23' } })
    // HOT→COLD 目标改由相邻问号解释，按钮文案统一为「迁移分区」。
    await user.click(screen.getByRole('button', { name: '迁移分区' }))
    await waitFor(() => expect(migrations).toBe(1))
    await user.click(screen.getByRole('button', { name: '核销已覆盖缺口' }))
    await waitFor(() => expect(resolutions).toBe(1))
  })

  it('keeps asset deployment disabled while offline or uncached', async () => {
    loginMockUser()
    server.use(
      http.get(API('/log-runtime/assets'), () => HttpResponse.json({ assets: [{ tag: 'v1.52.0', os: 'linux', arch: 'amd64', cached: false }] })),
      http.get(API('/nodes/1/log-runtime'), () => HttpResponse.json({ error: { message: 'LOG_NOT_READY' }, instances: [] })),
    )
    renderWithProviders(<NodeLogRuntimePanel nodeId={1} os="linux" arch="amd64" online={false} />)
    expect(await screen.findByText(/审批包未缓存/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '下发资产' })).toBeDisabled()
    expect(await screen.findByText('LOG_NOT_READY')).toBeInTheDocument()
  })
})
