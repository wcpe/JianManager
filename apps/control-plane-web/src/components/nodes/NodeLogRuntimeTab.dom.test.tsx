import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import api from '@/api/client'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { usePermissionsStore } from '@/stores/permissions'
import NodeLogRuntimeTab from './NodeLogRuntimeTab'

/**
 * 整日分区迁移的**权限门禁**（外壳层）。
 *
 * 存在理由：分区迁移跨命名空间把 HOT 搬到 COLD，**不可逆**——搬错日期或越权发起
 * 都没有回退路径。门禁放在外壳（NodeLogRuntimeTab）而不是受控视图，因为判定要读
 * auth/me 与 JWT 两个 store，属于应用侧知识；视图只接一个已算好的 `canMigrate`。
 *
 * 本文件钉住三件事：
 * ① auth/me 未加载时回退 JWT role=10（不能把“未加载”当成“服务端拒绝”）；
 * ② auth/me 已加载且明确非管理员时，旧 JWT 的 role=10 不得洗回来（零 POST）；
 * ③ 非管理员只禁整日迁移，不误禁同面板的普通运行时控制。
 *
 * 权限一律经 mock 的 `/auth/me` 设定，而不是手改 store：登入本身会触发
 * `loadFromAuthMe()` 发一个真的 `/auth/me`，回来后把手工设的值覆盖掉。
 */
beforeEach(() => {
  usePermissionsStore.getState().reset()
  server.use(
    http.get(API('/log-runtime/assets'), () => HttpResponse.json({ assets: [] })),
    http.get(API('/nodes/:id/log-runtime'), () => HttpResponse.json({ supported: true, instances: [{ namespace: 'hot', state: 'RUNNING', health_ok: true, query_ready: true }] })),
  )
})
afterEach(() => vi.restoreAllMocks())

/** 以指定 JWT role 登入（role=10 即平台管理员）。 */
function loginAs(role: number) {
  const payload = btoa(JSON.stringify({ userId: 1, username: 'u', role, exp: Math.floor(Date.now() / 1000) + 900 }))
  loginMockUser(`mock.${payload}.sig`)
}

/** 用 mock 的 /auth/me 设定权限（与生产同一条路径）。 */
function stubAuthMe(body: Record<string, unknown>) {
  server.use(http.get(API('/auth/me'), () => HttpResponse.json(body)))
}

async function fillMigrationDay() {
  fireEvent.change(await screen.findByLabelText('迁移日期（该日分区）'), { target: { value: '2026-10-21' } })
}

describe('整日分区迁移的权限门禁（外壳）', () => {
  it('auth/me 未加载时回退 JWT role=10，管理员可打开危险确认', async () => {
    loginAs(10)
    server.use(http.get(API('/auth/me'), () => HttpResponse.json({}, { status: 500 })))
    renderWithProviders(<NodeLogRuntimeTab nodeId={1} os="linux" arch="amd64" online />)
    await fillMigrationDay()
    const trigger = screen.getByRole('button', { name: '迁移分区' })
    expect(trigger).toBeEnabled()
    await userEvent.setup().click(trigger)
    expect(await screen.findByRole('dialog', { name: '确认迁移日志分区' })).toBeInTheDocument()
  })

  it('auth/me 明确非平台管理员时，旧 JWT role=10 不得洗回来（零 POST）', async () => {
    loginAs(10)
    // auth/me 显式否定：即使 JWT 里还是 10、即使有 node.manage，也不算平台管理员。
    stubAuthMe({ userId: 1, username: 'restricted', role: 0, roleKey: 'node_operations', isPlatformAdmin: false, nodes: ['node.manage'] })
    const post = vi.spyOn(api, 'post')
    renderWithProviders(<NodeLogRuntimeTab nodeId={1} os="linux" arch="amd64" online />)
    await fillMigrationDay()
    await waitFor(() => expect(screen.getByRole('button', { name: '迁移分区' })).toBeDisabled())
    // 禁用之外还要说明缘由：只置灰不说原因，用户会以为是没选日期。
    expect(screen.getByText('整日跨命名空间迁移仅平台管理员可用')).toBeInTheDocument()
    expect(post).not.toHaveBeenCalled()
  })

  it('auth/me 模板管理员（role=0 但 isPlatformAdmin=true）仍可迁移', async () => {
    loginAs(0)
    stubAuthMe({ userId: 2, username: 'template-admin', role: 0, roleKey: 'platform_admin', isPlatformAdmin: true, nodes: ['node.manage'] })
    renderWithProviders(<NodeLogRuntimeTab nodeId={1} os="linux" arch="amd64" online />)
    await fillMigrationDay()
    await waitFor(() => expect(screen.getByRole('button', { name: '迁移分区' })).toBeEnabled())
  })

  it('非管理员只禁整日迁移，不误禁同面板的普通运行时控制', async () => {
    loginAs(1)
    stubAuthMe({ userId: 2, username: 'operator', role: 1, roleKey: 'node_operations', isPlatformAdmin: false, nodes: ['node.manage'] })
    renderWithProviders(<NodeLogRuntimeTab nodeId={1} os="linux" arch="amd64" online />)
    await fillMigrationDay()
    await waitFor(() => expect(screen.getByRole('button', { name: '迁移分区' })).toBeDisabled())
    // 起停是普通运维动作，不应被迁移门禁连坐。
    expect(screen.getByRole('button', { name: '停止 HOT' })).toBeEnabled()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })
})
