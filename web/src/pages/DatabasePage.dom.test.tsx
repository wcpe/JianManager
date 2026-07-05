import { describe, it, expect, beforeEach } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { mockInject } from '@/mocks/inject'
import { db } from '@/mocks/db'
import type { Session } from '@/mocks/handlers/domains/auth'
import { useAuthStore } from '@/stores/auth'
import DatabasePage from './DatabasePage'

/**
 * DatabasePage 强断言：验 db 域 mock 联动 + 平台管理员权限边界。
 * fakeJWT payload 仅驱动前端 store；mock 端必须以 session 绑定用户为准，避免伪造 payload 越权。
 */
const makeToken = (userId: number, username: string, role: number) =>
  `mock.${btoa(JSON.stringify({ userId, username, role, exp: Math.floor(Date.now() / 1000) + 900 }))}.sig`

function loginWithSessionUser(tokenRole: number, sessionUserId: number, username = 'admin') {
  const token = makeToken(sessionUserId, username, tokenRole)
  db<Session>('sessions').insert({ accessToken: token, refreshToken: `r-${sessionUserId}`, userId: sessionUserId })
  useAuthStore.getState().login(token, `r-${sessionUserId}`)
}

function loginPlatformAdmin() {
  loginWithSessionUser(10, 1, 'admin')
}

function loginAsForgedAdminPayloadForMember() {
  loginWithSessionUser(10, 2, 'operator')
}

describe('DatabasePage（mock 假后端）', () => {
  beforeEach(() => {
    if (!Element.prototype.scrollIntoView) Element.prototype.scrollIntoView = () => {}
    if (!Element.prototype.hasPointerCapture) Element.prototype.hasPointerCapture = () => false
    if (!Element.prototype.setPointerCapture) Element.prototype.setPointerCapture = () => {}
    loginPlatformAdmin()
  })

  it('渲染种子：表清单含 users / instances，默认表显示 seed 行（admin/operator）', async () => {
    renderWithProviders(<DatabasePage />)
    // 表清单（左树）渲染出种子表。
    expect(await screen.findByRole('button', { name: /users/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /instances/ })).toBeInTheDocument()
    // 默认选中首表 users → 行内出现种子数据 username。
    expect(await screen.findByText('admin')).toBeInTheDocument()
    expect(screen.getByText('operator')).toBeInTheDocument()
    // 敏感列 password_hash 已脱敏，原值不出现。
    expect(screen.queryByText(/\$2a\$10\$/)).not.toBeInTheDocument()
  })

  it('交互：切换到 instances 表 → 行反映该表 seed（survival / creative）', async () => {
    const user = userEvent.setup()
    renderWithProviders(<DatabasePage />)
    await screen.findByText('admin')
    await user.click(await screen.findByRole('button', { name: /instances/ }))
    expect(await screen.findByText('survival')).toBeInTheDocument()
    expect(screen.getByText('creative')).toBeInTheDocument()
    // 切表后旧表的行不再渲染。
    expect(screen.queryByText('operator')).not.toBeInTheDocument()
  })

  it('过滤 username=operator 后仅保留匹配行，并可按 id 倒序排序', async () => {
    const user = userEvent.setup()
    renderWithProviders(<DatabasePage />)
    expect(await screen.findByText('admin')).toBeInTheDocument()

    await user.click(screen.getAllByRole('combobox')[0]!)
    await user.click(await screen.findByRole('option', { name: 'username' }))
    await user.type(screen.getByPlaceholderText('过滤关键字…'), 'operator')
    await user.click(screen.getByRole('button', { name: '过滤' }))

    expect(await screen.findByText('operator')).toBeInTheDocument()
    expect(screen.queryByText('admin')).not.toBeInTheDocument()

    await user.click(screen.getByText('清除'))
    expect(await screen.findByText('admin')).toBeInTheDocument()
    await user.click(screen.getByText('id'))
    await user.click(screen.getByText('id'))
    const dataRows = screen
      .getAllByRole('row')
      .map((row) => row.textContent ?? '')
      .filter((text) => text.includes('admin') || text.includes('operator'))
    expect(dataRows[0]).toContain('operator')
    expect(dataRows[1]).toContain('admin')
  })

  it('伪造管理员 payload 但 session 绑定普通成员时，mock 接口拒绝且不泄露表清单', async () => {
    useAuthStore.getState().logout()
    loginAsForgedAdminPayloadForMember()
    renderWithProviders(<DatabasePage />)

    expect(await screen.findByText('加载表清单失败')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /users/ })).not.toBeInTheDocument()
    expect(screen.queryByText('admin')).not.toBeInTheDocument()
  })

  it('注入 500（GET /db/tables）→ 显示加载表清单失败错误态', async () => {
    mockInject('get', '/db/tables', { kind: 'status', status: 500 })
    renderWithProviders(<DatabasePage />)
    expect(await screen.findByText('加载表清单失败')).toBeInTheDocument()
  })

  it('非平台管理员 → 显示无权限占位', async () => {
    useAuthStore.getState().logout() // 清掉 beforeEach 的管理员态
    renderWithProviders(<DatabasePage />)
    expect(await screen.findByText('仅平台管理员可访问')).toBeInTheDocument()
  })
})
