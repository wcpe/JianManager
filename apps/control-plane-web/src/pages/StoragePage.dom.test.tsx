import { describe, it, expect } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { mockInject } from '@jianmanager/devmock/inject'
import { db } from '@jianmanager/devmock/db'
import type { Session } from '@jianmanager/devmock/handlers/domains/auth'
import { useAuthStore } from '@/stores/auth'
import StoragePage from './StoragePage'

/**
 * 平台存储页强断言（FR-204 文件归档域）：验 mock 假后端 storage 集合渲染 + 浏览导航联动 + 错误注入。
 * StoragePage 仅平台管理员（role=10）可见，故登录态需带 role=10 的 JWT 声明（loginMockUser 用的纯 token
 * 解不出 role，会被角色门禁挡住），这里就地构造一个 payload 含 role=10 的伪 JWT。
 */
function loginAsPlatformAdmin(): void {
  loginWithRolePayload(1, 'admin', 10, 'r-admin')
}

function loginAsForgedAdminPayloadForMember(): void {
  loginWithRolePayload(2, 'operator', 10, 'r-member')
}

function loginWithRolePayload(userId: number, username: string, role: number, refreshToken: string): void {
  const payload = btoa(JSON.stringify({ userId, username, role }))
  const token = `mock.${payload}.sig`
  db<Session>('sessions').insert({ accessToken: token, refreshToken, userId })
  localStorage.setItem('accessToken', token)
  localStorage.setItem('refreshToken', refreshToken)
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
    // 固定 FHS 子目录缺失也必须列出，避免 mock 漏掉 bin/etc 布局约束。
    expect(screen.getAllByText('bin').length).toBeGreaterThan(0)
    expect(screen.getAllByText('etc').length).toBeGreaterThan(0)
    expect(screen.getByText('opt/jdks')).toBeInTheDocument()
    expect(screen.getByText('var/servers')).toBeInTheDocument()
    expect(screen.getByText('var/log')).toBeInTheDocument()
    // 缓存目录标可清理（clearable=true 种子）。
    expect(screen.getByText('临时缓存')).toBeInTheDocument()
  })

  it('清理 cache 前二次确认，确认后概览与目录浏览联动归零', async () => {
    loginAsPlatformAdmin()
    const user = userEvent.setup()
    renderWithProviders(<StoragePage />, { route: '/storage' })

    expect(await screen.findByText('/data/jianmanager')).toBeInTheDocument()
    await user.click(await screen.findByRole('button', { name: 'cache' }))
    expect(await screen.findByText('tmp-build.log')).toBeInTheDocument()

    await user.click(screen.getAllByRole('button', { name: '清理缓存' })[0])
    expect(await screen.findByText('清空临时缓存？')).toBeInTheDocument()
    const buttons = screen.getAllByRole('button', { name: '清理缓存' })
    await user.click(buttons[buttons.length - 1])

    await waitFor(() => {
      const cacheRow = screen.getByText('临时缓存').closest('tr')
      expect(cacheRow).not.toBeNull()
      expect(within(cacheRow as HTMLElement).getByText('0 B')).toBeInTheDocument()
      expect(within(cacheRow as HTMLElement).getByText('0')).toBeInTheDocument()
    })
    expect(await screen.findByText('该目录为空')).toBeInTheDocument()
    expect(screen.queryByText('tmp-build.log')).not.toBeInTheDocument()
  })

  it('伪造管理员 payload 但 session 绑定普通成员时，mock 接口拒绝且不泄露数据根', async () => {
    loginAsForgedAdminPayloadForMember()
    renderWithProviders(<StoragePage />, { route: '/storage' })

    expect(await screen.findByText('加载平台存储概览失败')).toBeInTheDocument()
    expect(screen.queryByText('/data/jianmanager')).not.toBeInTheDocument()
    expect(screen.queryByText('制品库')).not.toBeInTheDocument()
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
