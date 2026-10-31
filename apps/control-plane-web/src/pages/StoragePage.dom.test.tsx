import { describe, it, expect } from 'vitest'
import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { mockInject } from '@jianmanager/devmock/inject'
import { db } from '@jianmanager/devmock/db'
import { server } from '@jianmanager/devmock/server'
import type { Session } from '@jianmanager/devmock/handlers/domains/auth'
import { useAuthStore } from '@/stores/auth'
import { useThemeStore } from '@/stores/theme'
import StoragePage from './StoragePage'

/** 
 * 平台存储页强断言（文件归档域）：验 mock 假后端 storage 集合渲染 + 浏览导航联动 + 错误注入。
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
    // refreshKey 重拉后目录折叠，再展开 cache 应为空且无旧文件
    await user.click(screen.getByRole('button', { name: 'cache' }))
    expect(await screen.findByText(/该目录为空|空目录/)).toBeInTheDocument()
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

  /** 
   * 数据源对象必须跨渲染稳定。
   *
   * `FileBrowserTree` 的取数 effect 把 `source` 列为依赖（`[source, dirPath, refreshKey, t]`）；
   * 在渲染体里每次新建对象 → source 身份每次父组件重渲染都变 → 目录树被 `setEntries(null)`
   * 打回加载态并重拉 `/storage/files`。
   *
   * 用「与目录树无关的重渲染」（切主题 → StoragePageConnected 重渲染）来验：
   * 稳定的 source 不该因此产生任何新请求。
   */
  it('数据源稳定：无关重渲染不会重拉 /storage/files', async () => {
    loginAsPlatformAdmin()
    const calls: string[] = []
    const listener = ({ request }: { request: Request }) => {
      const url = new URL(request.url)
      if (url.pathname === '/api/v1/storage/files') calls.push(url.search)
    }
    server.events.on('request:start', listener)
    try {
      renderWithProviders(<StoragePage />, { route: '/storage' })
      // 等目录树真正出数据（var 是根层可下钻目录），此时首帧取数已全部发生。
      expect(await screen.findByRole('button', { name: 'var' })).toBeInTheDocument()
      const afterMount = calls.length
      expect(afterMount).toBeGreaterThan(0)

      // 与文件浏览无关的重渲染：切主题（store 变更 → 连接层重渲染 → 新 source 对象）。
      act(() => {
        useThemeStore.getState().setTheme('dark')
      })

      await waitFor(() => expect(screen.getByRole('button', { name: 'var' })).toBeInTheDocument())
      expect(calls.length).toBe(afterMount)
    } finally {
      server.events.removeListener('request:start', listener)
    }
  })

  /** 
   * 非管理员 / 加载中 / 失败三态也必须落在同一套外壳里。
   *
   * 原先三态直接 return 裸 `<p>`：页面在加载与失败时整体塌成一行无标题文字，
   * 页头消失、与就绪态之间布局跳变。
   */
  it('错误态与权限态仍渲染页壳与页头（不再是裸文本）', async () => {
    loginAsPlatformAdmin()
    mockInject('get', '/storage/overview', { kind: 'status', status: 500 })
    const { container, unmount } = renderWithProviders(<StoragePage />, { route: '/storage' })

    expect(await screen.findByText('加载平台存储概览失败')).toBeInTheDocument()
    const shell = container.querySelector('[data-page="storage"]')
    expect(shell).not.toBeNull()
    expect(within(shell as HTMLElement).getByRole('heading', { name: /平台存储/ })).toBeInTheDocument()
    unmount()

    // 非管理员：同样保留页壳 + 页头。
    useAuthStore.getState().logout()
    const member = renderWithProviders(<StoragePage />, { route: '/storage' })
    expect(await screen.findByText(/仅平台管理员/)).toBeInTheDocument()
    const memberShell = member.container.querySelector('[data-page="storage"]')
    expect(memberShell).not.toBeNull()
    expect(within(memberShell as HTMLElement).getByRole('heading', { name: /平台存储/ })).toBeInTheDocument()
  })
})
