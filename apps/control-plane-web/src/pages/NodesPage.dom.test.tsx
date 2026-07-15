import { describe, it, expect } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@jianmanager/devmock/inject'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import { useAuthStore } from '@/stores/auth'
import NodesPage from './NodesPage'

// 监控分段的 TimeSeriesChart 依赖 ResizeObserver 实测宽度，jsdom 无之 → 补桩使图表不崩（项目既有约定）。
if (!('ResizeObserver' in globalThis)) {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
}

/**
 * NodesPage 强断言（FR-200 / FR-128）：①渲染 seed 节点 ②进入维护写操作→列表联动出维护标记
 * ③详情激活 Tab 可寻址（?tab= 读写 + ?node= 深链）④注入 500→错误态。
 * NodesPage 跨域调 GET /instances（FR-201 域），本域 worktree 未注册——用 server.use 本地桩返回空，
 * 满足 onUnhandledRequest:'error' 覆盖闸，且不污染他域 handler 文件。
 */
function stubInstances() {
  server.use(http.get(API('/instances'), () => HttpResponse.json([])))
}

/** 监控分段跨域调 GET /metrics/series（观测域），本域未注册——本地桩返回空序列即可（图表走空态提示）。 */
function stubMetricSeries() {
  server.use(
    http.get(API('/metrics/series'), () =>
      HttpResponse.json({ resolution: 'raw', from: '', to: '', series: [] }),
    ),
  )
}

/** 平台管理员 JWT：危险操作门禁（DangerConfirm scope=platform）按 role=10 放行（同 GroupsPage 惯例）。 */
function adminJwt(role = 10): string {
  const payload = btoa(JSON.stringify({ userId: 1, username: 'admin', role, exp: Math.floor(Date.now() / 1000) + 900 }))
  return `mock.${payload}.sig`
}

/** 登录为平台管理员：写 sessions（过 requireAuth）+ 同步 auth store role（过危险操作门禁）。 */
function loginPlatformAdmin(): void {
  const token = adminJwt(10)
  loginMockUser(token)
  useAuthStore.getState().login(token, 'test-refresh-token')
}

describe('NodesPage（mock 假后端）', () => {
  it('渲染 seed 节点列表（alpha / beta）', async () => {
    loginMockUser()
    stubInstances()
    const { container } = renderWithProviders(<NodesPage />)
    expect(container.firstElementChild).toHaveAttribute('data-page', 'nodes')
    expect(container.firstElementChild).toHaveClass('jm-page-stack')
    expect(container.firstElementChild).toHaveClass('flex-col')
    expect(container.firstElementChild).toHaveClass('lg:flex-row')

    // beta 仅列表出现（唯一，作 await 锚点）；alpha 为首个、进页默认选中（FR-232）→ 列表 + 详情各一处，故 getAllByText。
    expect(await screen.findByText('beta')).toBeInTheDocument()
    expect(screen.getAllByText('alpha').length).toBeGreaterThan(0)
    expect(screen.getAllByText('10.0.0.11').length).toBeGreaterThan(0) // alpha host：列表 + 详情
  })

  it('对节点进入维护后，列表联动出现「维护」标记', async () => {
    loginMockUser()
    stubInstances()
    const user = userEvent.setup()
    renderWithProviders(<NodesPage />)

    // alpha 为首个、进页即默认选中（FR-232）→ 右栏详情直接出操作菜单（kebab，aria-label=操作），无需先点选。
    await screen.findByText('beta') // 等节点列表渲染完成
    const actionsBtn = await screen.findByRole('button', { name: '操作' })
    await user.click(actionsBtn)
    await user.click(await screen.findByRole('menuitem', { name: '进入维护' }))

    // setNodeMaintenance 成功后失效 ['nodes'] → 重新拉取，handler 现回 maintenance:true，列表行渲染「维护中」徽标。
    await waitFor(() => expect(screen.getAllByText('维护中').length).toBeGreaterThan(0))
  })

  it('添加节点向导生成 CP 托管脚本命令，且手动 token 不落配置', async () => {
    loginMockUser()
    stubInstances()
    const user = userEvent.setup()
    renderWithProviders(<NodesPage />)

    await user.click(await screen.findByRole('button', { name: '添加节点' }))
    await user.click(await screen.findByRole('button', { name: '生成一键命令' }))

    await waitFor(() => {
      const text = document.body.textContent ?? ''
      expect(text).toContain('/install-worker.sh')
      expect(text).toContain('--control-plane')
      expect(text).toContain('--token')
    })

    await user.click(await screen.findByRole('tab', { name: '手动连接' }))
    expect(await screen.findByText(/不写入 worker\.yml/)).toBeInTheDocument()
  })

  it('URL 带 ?node=2&tab=monitor 时深链选中 beta 并激活监控分段（FR-128）', async () => {
    loginMockUser()
    stubInstances()
    stubMetricSeries()
    renderWithProviders(<NodesPage />, { route: '/nodes?node=2&tab=monitor' })

    // 详情身份块出 beta（深链选中 id=2，非默认首个 alpha）。
    const heading = await screen.findByRole('heading', { name: 'beta' })
    expect(heading).toBeInTheDocument()
    // 监控分段渲染：CPU 趋势面板可见（概览分段无此面板）。
    expect(await screen.findByText('CPU 趋势')).toBeInTheDocument()
  })

  it('切换详情分段时 URL 同步写入 ?tab=，切回概览时省略参数（FR-128）', async () => {
    loginMockUser()
    stubInstances()
    stubMetricSeries()
    const user = userEvent.setup()
    renderWithProviders(<NodesPage />, { route: '/nodes' })

    await screen.findByText('beta') // 列表就绪；alpha 默认选中
    // 默认 overview 不写 tab 参数。
    expect(new URLSearchParams(window.location.search).get('tab')).toBeNull()

    // 点「监控」分段 → URL 写入 tab=monitor。
    await user.click(await screen.findByRole('button', { name: '监控' }))
    await waitFor(() => expect(new URLSearchParams(window.location.search).get('tab')).toBe('monitor'))
    expect(await screen.findByText('CPU 趋势')).toBeInTheDocument()

    // 切回「概览」→ tab 参数被省略（保持链接简洁）。
    await user.click(await screen.findByRole('button', { name: '概览' }))
    await waitFor(() => expect(new URLSearchParams(window.location.search).get('tab')).toBeNull())
  })

  it('离线节点名下有实例：下线被 409 拒并列实例清单，强制下线级联删除（FR-309）', async () => {
    loginPlatformAdmin()
    stubInstances()
    const user = userEvent.setup()
    renderWithProviders(<NodesPage />)

    // 选中离线节点 beta（seed id=2，其名下有 seed 实例，如 creative-1）。
    await user.click(await screen.findByText('beta'))
    await screen.findByRole('heading', { name: 'beta' })
    await user.click(screen.getByRole('button', { name: '操作' }))
    await user.click(await screen.findByRole('menuitem', { name: '下线' }))

    // 第一道确认（既有 DangerConfirm）：输入节点名后确认。
    await user.type(await screen.findByPlaceholderText('beta'), 'beta')
    await user.click(screen.getByRole('button', { name: '下线' }))

    // 守卫 409 → 实例清单模态：列出名下实例 + 离线节点的强制下线入口。
    expect(await screen.findByText('无法下线：节点名下仍有实例')).toBeInTheDocument()
    expect(await screen.findByText('creative-1')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '强制下线' }))

    // 第二道确认（强制级联，文案明示不清理远端文件）：再次输入节点名。
    expect(await screen.findByText('强制下线节点')).toBeInTheDocument()
    expect(screen.getByText(/不会被清理/)).toBeInTheDocument()
    await user.type(await screen.findByPlaceholderText('beta'), 'beta')
    await user.click(screen.getByRole('button', { name: '强制下线' }))

    // 级联删除成功：节点从列表消失。
    await waitFor(() => expect(screen.queryByText('beta')).not.toBeInTheDocument())
  })

  it('实例对比分段：只发 1 条批量指标请求（消 N+1），批量目标 ≤12（FR-340）', async () => {
    loginMockUser()
    // 计数批量指标请求 + 捕获请求体（断言单请求 + 目标上限）；search 请求断言 pageSize=12。
    let batchCount = 0
    let batchTargetCount = 0
    let searchPageSize: string | null = null
    server.use(
      http.post(API('/metrics/series/batch'), async ({ request }) => {
        batchCount += 1
        const body = (await request.json()) as { targetIds?: string[] }
        batchTargetCount = body.targetIds?.length ?? 0
        return HttpResponse.json({ resolution: 'raw', from: '', to: '', series: {}, skipped: [] })
      }),
      http.get(API('/instances/search'), ({ request }) => {
        const url = new URL(request.url)
        // 只在按节点分页（对比清单）时记录；其它 search 调用不干扰。
        if (url.searchParams.get('nodeId')) searchPageSize = url.searchParams.get('pageSize')
        const items = Array.from({ length: 12 }, (_, i) => ({
          id: 100 + i,
          uuid: `cmp-${i}`,
          nodeId: 2,
          name: `srv-${String(i).padStart(2, '0')}`,
          type: 'minecraft_java',
          role: 'backend',
          processType: 'daemon',
          status: 'RUNNING',
          startCommand: 'java -jar s.jar',
          workDir: '/x',
          image: '',
          cpuLimit: 0,
          memLimitMb: 0,
          diskLimitMb: 0,
          serverPort: 25600 + i,
          autoStart: false,
          autoRestart: true,
          tags: '',
          createdAt: '2026-01-01T00:00:00Z',
        }))
        return HttpResponse.json({ items, total: 600, page: 1, pageSize: 12 })
      }),
    )
    const user = userEvent.setup()
    renderWithProviders(<NodesPage />, { route: '/nodes?node=2&tab=instances' })

    // 进入实例分段：对比面板标题可见（等异步查询发起）。
    expect(await screen.findByText('实例指标对比')).toBeInTheDocument()
    await waitFor(() => expect(batchCount).toBe(1))
    expect(batchTargetCount).toBeLessThanOrEqual(12)
    expect(searchPageSize).toBe('12')

    // 切换对比指标（TPS→MSPT）仍是单条批量查询，不回退 N 条逐实例请求。
    await user.click(await screen.findByRole('button', { name: 'MSPT' }))
    await waitFor(() => expect(batchCount).toBe(2))
  })

  it('注入 500：节点列表请求失败，列表区不崩溃（无节点行）', async () => {
    loginMockUser()
    stubInstances()
    mockInject('get', '/nodes', { kind: 'status', status: 500 })
    renderWithProviders(<NodesPage />)

    // 错误态：列表空（不出现 seed 节点名），页面整体仍渲染（搜索框可见）。
    await screen.findByRole('textbox')
    await waitFor(() => {
      expect(screen.queryByText('alpha')).not.toBeInTheDocument()
    })
    expect(within(document.body).queryByText('beta')).not.toBeInTheDocument()
  })
})
