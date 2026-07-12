import { describe, it, expect } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import { server } from '@/mocks/server'
import { API } from '@/mocks/api'
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
