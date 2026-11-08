import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AxiosError } from 'axios'
import { useState } from 'react'
import { http, HttpResponse } from 'msw'
import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import api from '@/api/client'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import NodeLogRuntimePanel from './NodeLogRuntimePanel'
import { usePermissionsStore } from '@/stores/permissions'

beforeEach(() => {
  const payload = btoa(JSON.stringify({ userId: 1, username: 'admin', role: 10, exp: Math.floor(Date.now() / 1000) + 900 }))
  loginMockUser(`mock.${payload}.sig`)
  server.use(
    http.get(API('/log-runtime/assets'), () => HttpResponse.json({ assets: [] })),
    http.get(API('/nodes/:id/log-runtime'), () => HttpResponse.json({ supported: true, instances: [] })),
  )
})
afterEach(() => vi.restoreAllMocks())

async function openMigration() {
  fireEvent.change(screen.getByLabelText('迁移日期（该日分区）'), { target: { value: '2026-10-21' } })
  await userEvent.setup().click(screen.getByRole('button', { name: '迁移分区' }))
  return screen.findByRole('dialog', { name: '确认迁移日志分区' })
}

describe('迁移危险确认', () => {
  it('首击/取消零POST，确认固定目标与UTC日、在途重复点击仍只有一POST', async () => {
    const posts: unknown[] = []
    let release!: () => void
    server.use(http.post(API('/nodes/1/log-runtime/migrate'), async ({ request }) => {
      posts.push(await request.json())
      await new Promise<void>((done) => { release = done })
      return HttpResponse.json({ state: 'migrated' })
    }))
    renderWithProviders(<NodeLogRuntimePanel nodeId={1} os="linux" arch="amd64" online />)
    const dialog = await openMigration()
    expect(dialog).toHaveTextContent('节点 1 · 存储命名空间 node:1 · UTC 日 2026-10-21')
    expect(posts).toHaveLength(0)
    await userEvent.setup().click(screen.getByRole('button', { name: '取消' }))
    expect(posts).toHaveLength(0)
    await userEvent.setup().click(screen.getByRole('button', { name: '迁移分区' }))
    // 即使后台日期字段被脚本改变，确认仍绑定首击捕获的日期。
    fireEvent.change(screen.getByLabelText('迁移日期（该日分区）'), { target: { value: '2026-10-20' } })
    const confirm = screen.getByRole('button', { name: '确认迁移' })
    fireEvent.click(confirm); fireEvent.click(confirm)
    await waitFor(() => expect(posts).toHaveLength(1))
    expect(posts[0]).toEqual({ storageNamespace: 'node:1', utcDay: '2026-10-21' })
    expect(confirm).toBeDisabled()
    release()
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('节点prop切换不把已打开确认漂移到另一节点', async () => {
    const post = vi.spyOn(api, 'post').mockResolvedValue({ data: { state: 'migrated' } })
    function Harness() {
      const [node, setNode] = useState(1)
      return <><NodeLogRuntimePanel nodeId={node} os="linux" arch="amd64" online /><button data-testid="switch-node" onClick={() => setNode(2)}>切节点</button></>
    }
    renderWithProviders(<Harness />)
    await openMigration()
    fireEvent.click(screen.getByTestId('switch-node'))
    await userEvent.setup().click(screen.getByRole('button', { name: '确认迁移' }))
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post.mock.calls[0][0]).toBe('/nodes/1/log-runtime/migrate')
  })

  it.each([409, 502])('%d错误保留确认目标与错误，不假成功不自动重发', async (status) => {
    let posts = 0
    server.use(http.post(API('/nodes/1/log-runtime/migrate'), () => { posts++; return HttpResponse.json({ message: `迁移失败-${status}` }, { status }) }))
    renderWithProviders(<NodeLogRuntimePanel nodeId={1} os="linux" arch="amd64" online />)
    await openMigration()
    await userEvent.setup().click(screen.getByRole('button', { name: '确认迁移' }))
    await waitFor(() => expect(screen.getByRole('dialog')).toHaveTextContent(`迁移失败-${status}`))
    expect(screen.getByRole('dialog')).toHaveTextContent('2026-10-21')
    expect(posts).toBe(1)
  })

  it('数字role0但auth/me有效平台管理员模板仍可确认整日迁移一次', async () => {
    const payload = btoa(JSON.stringify({ userId: 2, username: 'template-admin', role: 0, exp: Math.floor(Date.now() / 1000) + 900 }))
    server.use(http.get(API('/auth/me'), () => HttpResponse.json({ userId: 2, username: 'template-admin', role: 0, roleKey: 'platform_admin', isPlatformAdmin: true, nodes: ['node.manage'] })))
    loginMockUser(`mock.${payload}.sig`)
    await usePermissionsStore.getState().loadFromAuthMe()
    const post = vi.spyOn(api, 'post').mockResolvedValue({ data: { state: 'migrated' } })
    renderWithProviders(<NodeLogRuntimePanel nodeId={1} os="linux" arch="amd64" online />)
    fireEvent.change(screen.getByLabelText('迁移日期（该日分区）'), { target: { value: '2026-10-21' } })
    expect(screen.getByRole('button', { name: '迁移分区' })).toBeEnabled()
    await userEvent.setup().click(screen.getByRole('button', { name: '迁移分区' }))
    expect(screen.getByRole('button', { name: '确认迁移' })).toBeEnabled()
    expect(screen.getByRole('dialog')).toHaveTextContent('节点 1 · 存储命名空间 node:1 · UTC 日 2026-10-21')
    expect(post).not.toHaveBeenCalled()
    await userEvent.setup().click(screen.getByRole('button', { name: '取消' }))
    expect(post).not.toHaveBeenCalled()
    await userEvent.setup().click(screen.getByRole('button', { name: '迁移分区' }))
    await userEvent.setup().click(screen.getByRole('button', { name: '确认迁移' }))
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post.mock.calls[0]).toEqual(['/nodes/1/log-runtime/migrate', { storageNamespace: 'node:1', utcDay: '2026-10-21' }, { timeout: 180_000 }])
  })

  it('已加载auth/me显式非平台管理员不能被旧JWT10或单独node.manage洗掉', async () => {
    server.use(http.get(API('/auth/me'), () => HttpResponse.json({ userId: 1, username: 'restricted', role: 0, roleKey: 'node_operations', isPlatformAdmin: false, nodes: ['node.manage'] })))
    await usePermissionsStore.getState().loadFromAuthMe()
    const post = vi.spyOn(api, 'post')
    renderWithProviders(<NodeLogRuntimePanel nodeId={1} os="linux" arch="amd64" online />)
    fireEvent.change(screen.getByLabelText('迁移日期（该日分区）'), { target: { value: '2026-10-21' } })
    expect(screen.getByRole('button', { name: '迁移分区' })).toBeDisabled()
    expect(screen.getByText('整日跨命名空间迁移仅平台管理员可用')).toBeInTheDocument()
    expect(post).not.toHaveBeenCalled()
  })

  it('数字role0只有node.manage但不是有效平台管理员仍零POST', async () => {
    const payload = btoa(JSON.stringify({ userId: 2, username: 'node-operator', role: 0, exp: Math.floor(Date.now() / 1000) + 900 }))
    server.use(http.get(API('/auth/me'), () => HttpResponse.json({ userId: 2, username: 'node-operator', role: 0, roleKey: 'node_operations', isPlatformAdmin: false, nodes: ['node.manage'] })))
    loginMockUser(`mock.${payload}.sig`)
    await usePermissionsStore.getState().loadFromAuthMe()
    const post = vi.spyOn(api, 'post')
    renderWithProviders(<NodeLogRuntimePanel nodeId={1} os="linux" arch="amd64" online />)
    fireEvent.change(screen.getByLabelText('迁移日期（该日分区）'), { target: { value: '2026-10-21' } })
    expect(screen.getByRole('button', { name: '迁移分区' })).toBeDisabled()
    expect(post).not.toHaveBeenCalled()
  })

  it('确认期间有效平台权限撤销立即关窗并保持零POST，不借旧JWT10继续确认', async () => {
    const post = vi.spyOn(api, 'post')
    renderWithProviders(<NodeLogRuntimePanel nodeId={1} os="linux" arch="amd64" online />)
    await openMigration()
    act(() => usePermissionsStore.getState().setFromAuthMe({ userId: 1, username: 'restricted', role: 0, roleKey: 'node_operations', isPlatformAdmin: false, nodes: ['node.manage'] }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: '迁移分区' })).toBeDisabled()
    expect(post).not.toHaveBeenCalled()
  })

  it('auth/me加载前保留JWT10回退，不把未加载的false误作服务端显式拒绝', async () => {
    usePermissionsStore.getState().reset()
    renderWithProviders(<NodeLogRuntimePanel nodeId={1} os="linux" arch="amd64" online />)
    fireEvent.change(screen.getByLabelText('迁移日期（该日分区）'), { target: { value: '2026-10-21' } })
    expect(screen.getByRole('button', { name: '迁移分区' })).toBeEnabled()
    await userEvent.setup().click(screen.getByRole('button', { name: '迁移分区' }))
    expect(screen.getByRole('button', { name: '确认迁移' })).toBeEnabled()
  })

  it('非平台管理员只禁用整日迁移，不误禁同面板普通运行时控制', async () => {
    const payload = btoa(JSON.stringify({ userId: 2, username: 'operator', role: 1, exp: Math.floor(Date.now() / 1000) + 900 }))
    loginMockUser(`mock.${payload}.sig`)
    server.use(http.get(API('/nodes/1/log-runtime'), () => HttpResponse.json({ supported: true, instances: [{ namespace: 'hot', state: 'RUNNING', health_ok: true, query_ready: true }] })))
    renderWithProviders(<NodeLogRuntimePanel nodeId={1} os="linux" arch="amd64" online />)
    await screen.findByText('进程健康')
    fireEvent.change(screen.getByLabelText('迁移日期（该日分区）'), { target: { value: '2026-10-21' } })
    expect(screen.getByRole('button', { name: '迁移分区' })).toBeDisabled()
    expect(screen.getByText('整日跨命名空间迁移仅平台管理员可用')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '停止 HOT' })).toBeEnabled()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('超时明确结果未知且不自动重放，目标与确认框仍保留', async () => {
    const post = vi.spyOn(api, 'post').mockRejectedValue(new AxiosError('timeout', 'ECONNABORTED'))
    renderWithProviders(<NodeLogRuntimePanel nodeId={1} os="linux" arch="amd64" online />)
    await openMigration()
    await userEvent.setup().click(screen.getByRole('button', { name: '确认迁移' }))
    await waitFor(() => expect(screen.getByRole('dialog')).toHaveTextContent('迁移结果未知；不会自动重发'))
    expect(post).toHaveBeenCalledTimes(1)
    expect(screen.getByRole('dialog')).toHaveTextContent('2026-10-21')
  })
})
