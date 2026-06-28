import { describe, it, expect } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { db } from '@/mocks/db'
import { useAuthStore } from '@/stores/auth'
import { mockInject } from '@/mocks/inject'
import type { Session } from '@/mocks/handlers/domains/auth'
import SystemUpdatePage from './SystemUpdatePage'

/**
 * SystemUpdatePage 强断言（FR-200）：①渲染 seed 版本对比（CP + 各节点）②升级节点→check 联动版本变化
 * ③注入 500→错误态。本页仅平台管理员可见，故需用解出 role=10 的 token 登录（store 从 JWT 解 role）。
 * 仅调本域 GET /self-update/check + POST /self-update/check/refresh + GET /self-update/rollout
 * + POST /self-update/nodes/:id/upgrade。
 */

/** 构造解码出 role=10 的假 JWT（payload 同 auth.ts fakeJwt 结构），并登记 session 供 requireAuth 放行。 */
function loginPlatformAdmin() {
  const payload = btoa(
    JSON.stringify({ userId: 1, username: 'admin', role: 10, exp: Math.floor(Date.now() / 1000) + 900 }),
  )
  const token = `mock.${payload}.sig`
  db<Session>('sessions').insert({ accessToken: token, refreshToken: 'r-admin', userId: 1 })
  useAuthStore.getState().login(token, 'r-admin')
}

describe('SystemUpdatePage（mock 假后端）', () => {
  it('渲染 seed 版本对比（CP 控制台 + 各节点）', async () => {
    loginPlatformAdmin()
    renderWithProviders(<SystemUpdatePage />)

    expect(await screen.findByText('系统更新')).toBeInTheDocument()
    expect(screen.getByText('Control Plane（控制台）')).toBeInTheDocument()
    // 节点行（seed nodes alpha/beta）。
    expect(await screen.findByText('alpha')).toBeInTheDocument()
    expect(screen.getByText('beta')).toBeInTheDocument()
  })

  it('升级节点后，check 联动反映新版本', async () => {
    loginPlatformAdmin()
    const user = userEvent.setup()
    renderWithProviders(<SystemUpdatePage />)

    // alpha 当前 0.9.0（可升级）。定位 alpha 行。
    const alphaRow = (await screen.findByText('alpha')).closest('tr') as HTMLElement
    await waitFor(() => expect(within(alphaRow).getByText('0.9.0')).toBeInTheDocument())

    // 点该行「升级」→ 危险确认（平台管理员放行）→ 确认。
    await user.click(within(alphaRow).getByRole('button', { name: '升级' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: '升级' }))

    // upgradeNode 写 workerVersion=0.10.0 + onUpgraded 触发 refresh → check 重取，alpha 行显示 0.10.0。
    await waitFor(() => {
      const row = (screen.getByText('alpha')).closest('tr') as HTMLElement
      expect(within(row).getByText('0.10.0')).toBeInTheDocument()
    })
  })

  it('注入 500：检查失败显示错误态', async () => {
    loginPlatformAdmin()
    // check 与 refresh 同时注入 500，使页面无任何结果可用 → 渲染错误横幅。
    mockInject('get', '/self-update/check', { kind: 'status', status: 500, body: { message: '检查更新失败' } })
    mockInject('post', '/self-update/check/refresh', { kind: 'status', status: 500, body: { message: '检查更新失败' } })
    renderWithProviders(<SystemUpdatePage />)

    expect(await screen.findByText('检查更新失败')).toBeInTheDocument()
  })
})
