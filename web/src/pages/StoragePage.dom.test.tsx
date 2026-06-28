import { describe, it, expect } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { mockInject } from '@/mocks/inject'
import { db } from '@/mocks/db'
import type { Session } from '@/mocks/handlers/domains/auth'
import { useAuthStore } from '@/stores/auth'
import StoragePage from './StoragePage'

/**
 * 平台存储页强断言（FR-204 文件归档域）：验 mock 假后端 storage 集合渲染 + 浏览导航联动 + 错误注入。
 * StoragePage 仅平台管理员（role=10）可见，故登录态需带 role=10 的 JWT 声明（loginMockUser 用的纯 token
 * 解不出 role，会被角色门禁挡住），这里就地构造一个 payload 含 role=10 的伪 JWT。
 */
function loginAsPlatformAdmin(): void {
  const payload = btoa(JSON.stringify({ userId: 1, username: 'admin', role: 10 }))
  const token = `mock.${payload}.sig`
  db<Session>('sessions').insert({ accessToken: token, refreshToken: 'r-admin', userId: 1 })
  localStorage.setItem('accessToken', token)
  localStorage.setItem('refreshToken', 'r-admin')
  // 同步 zustand store，使 StoragePage 的 role 门禁读到 10。
  useAuthStore.getState().loadFromStorage()
}

describe('StoragePage（mock 假后端）', () => {
  it('渲染存储概览种子：数据根路径 + FHS 目录占用行', async () => {
    loginAsPlatformAdmin()
    renderWithProviders(<StoragePage />, { route: '/storage' })

    // 数据根绝对路径与制品库目录行（label/path 来自 storageDirs 种子）。
    expect(await screen.findByText('/data/jianmanager')).toBeInTheDocument()
    expect(await screen.findByText('制品库')).toBeInTheDocument()
    expect(screen.getByText('var/artifacts')).toBeInTheDocument()
    // 缓存目录标可清理（clearable=true 种子）。
    expect(screen.getByText('临时缓存')).toBeInTheDocument()
  })

  it('数据根浏览：点目录下钻反映子项变化', async () => {
    loginAsPlatformAdmin()
    const user = userEvent.setup()
    renderWithProviders(<StoragePage />, { route: '/storage' })

    // 浏览面板（browserTitle）根层把 var 渲染为可下钻目录按钮（DirUsage 表只出现全路径 "var/xxx"，
    // 故裸名 "var" 按钮唯一来自浏览面板）。
    const varDir = await screen.findByRole('button', { name: 'var' })

    // 下钻 var/ → 子项 artifacts、log 出现（导航联动；裸名仅浏览面板有）。
    await user.click(varDir)
    expect(await screen.findByRole('button', { name: 'artifacts' })).toBeInTheDocument()
    expect(screen.getByText('log')).toBeInTheDocument()
  })

  it('注入 500：概览加载失败显示错误态', async () => {
    loginAsPlatformAdmin()
    mockInject('get', '/storage/overview', { kind: 'status', status: 500 })
    renderWithProviders(<StoragePage />, { route: '/storage' })

    expect(await screen.findByText('加载平台存储概览失败')).toBeInTheDocument()
  })
})
