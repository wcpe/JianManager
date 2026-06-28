import { describe, it, expect } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import { useAuthStore } from '@/stores/auth'
import SettingsPage from './SettingsPage'

/**
 * SettingsPage 强断言（FR-210）：渲染 seed 平台配置 → 改设置保存联动回读新值 → 注入 500 显错误态。
 * 平台配置分类仅平台管理员（role=10）可见，故以 role=10 的假 JWT 登录。
 */

/** 构造 role=10 的假 JWT（payload base64url，不验签），使 auth store 解出平台管理员角色。 */
function platformAdminJwt(): string {
  const payload = btoa(
    JSON.stringify({ userId: 1, username: 'admin', role: 10, exp: Math.floor(Date.now() / 1000) + 900 }),
  )
  return `mock.${payload}.sig`
}

/** 登录为平台管理员：注册 mock 会话（requireAuth 放行）+ 让 auth store role=10。 */
function loginAsPlatformAdmin() {
  const jwt = platformAdminJwt()
  loginMockUser(jwt)
  useAuthStore.getState().login(jwt, 'test-refresh-token')
}

describe('SettingsPage（mock 假后端）', () => {
  it('渲染出 seed 平台配置项（日志分类含 log.level）', async () => {
    loginAsPlatformAdmin()
    const user = userEvent.setup()
    renderWithProviders(<SettingsPage />)

    // 平台管理员可见「日志」分类导航；切过去看到 log.level 配置项（键名 mono 展示）。
    await user.click(await screen.findByRole('button', { name: /日志/ }))
    expect(await screen.findByText('log.level')).toBeInTheDocument()
    // 切到「运行时」分类可见 graceful_stop.timeout（不同分类 → 印证分类分组生效）。
    await user.click(screen.getByRole('button', { name: /运行时/ }))
    expect(await screen.findByText('graceful_stop.timeout')).toBeInTheDocument()
  })

  it('改设置保存 → 联动回读新值', async () => {
    loginAsPlatformAdmin()
    const user = userEvent.setup()
    renderWithProviders(<SettingsPage />)

    // 切到「运行时」分类，改 graceful_stop.timeout（其输入框按当前值 30 唯一定位，避开重名键文本）。
    await user.click(await screen.findByRole('button', { name: /运行时/ }))
    const input = (await screen.findByDisplayValue('30')) as HTMLInputElement

    await user.clear(input)
    await user.type(input, '60')
    await user.click(screen.getByRole('button', { name: '保存' }))

    // 联动回读：PUT 写入后 onSuccess 失效缓存重取，输入框回填新值（草稿已清，展示后端生效值）。
    // 成功 toast 走 sonner 门户、harness 未挂 Toaster，故以「值持久化为新值、旧值消失」断言写读联动。
    await waitFor(() => expect(screen.getByDisplayValue('60')).toBeInTheDocument())
    expect(screen.queryByDisplayValue('30')).not.toBeInTheDocument()
  })

  it('注入 500 → 平台配置显示加载失败错误态', async () => {
    loginAsPlatformAdmin()
    mockInject('get', '/settings', { kind: 'status', status: 500 })
    const user = userEvent.setup()
    renderWithProviders(<SettingsPage />)

    await user.click(await screen.findByRole('button', { name: /日志/ }))
    expect(await screen.findByText('加载平台配置失败')).toBeInTheDocument()
  })
})
