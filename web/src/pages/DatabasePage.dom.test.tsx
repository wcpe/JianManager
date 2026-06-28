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
 * DatabasePage 强断言（FR-205 配置数据库域）：验 db 域 mock 联动 + 错误注入。
 * 本页仅平台管理员（role=10）可达，故登录时用内嵌 role=10 的 fakeJWT，
 * 既让 auth store 解出 role=10，又让 requireAuth 凭同一 token 放行受保护的 /db/* 端点。
 */
const ADMIN_TOKEN = `mock.${btoa(
  JSON.stringify({ userId: 1, username: 'admin', role: 10, exp: Math.floor(Date.now() / 1000) + 900 }),
)}.sig`

function loginPlatformAdmin() {
  db<Session>('sessions').insert({ accessToken: ADMIN_TOKEN, refreshToken: 'r-admin', userId: 1 })
  useAuthStore.getState().login(ADMIN_TOKEN, 'r-admin')
}

describe('DatabasePage（mock 假后端）', () => {
  beforeEach(() => {
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
