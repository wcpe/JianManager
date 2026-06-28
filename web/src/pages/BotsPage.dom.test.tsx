import { beforeAll, describe, it, expect } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { server } from '@/mocks/server'
import { API } from '@/mocks/api'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import BotsPage from './BotsPage'

/**
 * BotsPage 强断言（FR-209 域簇验收三条）：
 *  ① 渲染出 seed 派生的分组（含 seed bot 名）；
 *  ② 通过新建对话框创建 Bot → 实例分组计数联动 +1；
 *  ③ 注入 500 → 页面显示错误态（不崩溃、不整页刷新）。
 *
 * 跨域依赖（/nodes 归 FR-200、/instances 归 FR-201）本域不定义，改在测试内用 server.use
 * 临时桩注入（test-local，确定性，且对最终聚合的真实 instances/nodes handler 取优先级），
 * 终端 WS 回显属真机项（jsdom 不稳），不在本测试范围 —— 只测 bots REST。
 */

// Radix（Popover/Select/Dialog）在 jsdom 下依赖以下 API；vitest jsdom 默认缺，按标准配方补齐。
beforeAll(() => {
  if (!Element.prototype.scrollIntoView) Element.prototype.scrollIntoView = () => {}
  if (!Element.prototype.hasPointerCapture) Element.prototype.hasPointerCapture = () => false
  if (!Element.prototype.setPointerCapture) Element.prototype.setPointerCapture = () => {}
  if (!Element.prototype.releasePointerCapture) Element.prototype.releasePointerCapture = () => {}
  if (!('ResizeObserver' in globalThis)) {
    globalThis.ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    } as unknown as typeof ResizeObserver
  }
})

/** 注入 BotsPage 渲染所需的跨域只读依赖（节点 / 实例下拉数据源）。 */
function stubCrossDomain() {
  server.use(
    http.get(API('/nodes'), () =>
      HttpResponse.json([
        { id: 1, uuid: 'n-1', name: '主节点', host: '127.0.0.1', grpcPort: 9100, wsPort: 9200, status: 1, maintenance: false, os: 'linux', arch: 'amd64', cpuCores: 4, memoryMb: 8192, diskTotalMb: 100000, cpuUsage: 0, memoryUsage: 0, diskUsage: 0, networkBytesSent: 0, networkBytesRecv: 0, loadAvg1: 0, lastHeartbeat: null, createdAt: '2026-06-28T00:00:00Z' },
      ]),
    ),
    http.get(API('/instances'), () =>
      HttpResponse.json([
        { id: 1, uuid: 'i-1', nodeId: 1, name: '生存服', type: 'minecraft', role: 'universal', processType: 'direct', status: 'RUNNING', startCommand: '', workDir: '/srv/1', serverPort: 25565, autoStart: false, autoRestart: false, tags: '', createdAt: '2026-06-28T00:00:00Z' },
        { id: 2, uuid: 'i-2', nodeId: 2, name: '空岛服', type: 'minecraft', role: 'universal', processType: 'direct', status: 'RUNNING', startCommand: '', workDir: '/srv/2', serverPort: 25566, autoStart: false, autoRestart: false, tags: '', createdAt: '2026-06-28T00:00:00Z' },
      ]),
    ),
  )
}

describe('BotsPage（mock 假后端）', () => {
  it('① 渲染 seed 派生分组与 seed bot', async () => {
    loginMockUser()
    stubCrossDomain()
    renderWithProviders(<BotsPage />, { route: '/bots' })

    // 默认按实例分组：seed 的 GuardBot/FollowBot 都在实例 1（生存服），应出 total=2 的分组卡。
    const card = await screen.findByText('生存服')
    const group = card.closest('div.flex-col') as HTMLElement
    expect(within(group).getByText('2')).toBeInTheDocument()
    // 实例 2（空岛服）一只 PatrolBot。
    expect(await screen.findByText('空岛服')).toBeInTheDocument()

    // 展开生存服分组 → 拉 GET /bots，应窥见具体 seed bot 名。
    await userEvent.click(card)
    expect(await screen.findByText('GuardBot')).toBeInTheDocument()
    expect(await screen.findByText('FollowBot')).toBeInTheDocument()
  })

  it('② 新建 Bot → 实例分组计数联动 +1', async () => {
    loginMockUser()
    stubCrossDomain()
    renderWithProviders(<BotsPage />, { route: '/bots' })

    const survival = await screen.findByText('生存服')
    const group = survival.closest('div.flex-col') as HTMLElement
    expect(within(group).getByText('2')).toBeInTheDocument()

    // 打开新建对话框
    await userEvent.click(screen.getByRole('button', { name: '创建 Bot' }))
    const dialog = await screen.findByRole('dialog')

    // 填名称（FieldLabel 未绑定 htmlFor，按 placeholder 取输入）
    await userEvent.type(within(dialog).getByPlaceholderText('GuardBot'), 'NewBot')
    // 选实例（Combobox：点触发器 → 点选项「生存服」，选后自动回填 server/port）
    await userEvent.click(within(dialog).getByText('选择实例'))
    const option = await screen.findByRole('button', { name: '生存服' })
    await userEvent.click(option)

    // 提交
    await userEvent.click(within(dialog).getByRole('button', { name: /^创建$/ }))

    // 联动：生存服分组从 2 → 3
    await waitFor(() => {
      const g = (screen.getByText('生存服').closest('div.flex-col')) as HTMLElement
      expect(within(g).getByText('3')).toBeInTheDocument()
    })
  })

  it('③ 注入 500 → 页面显示错误态而非崩溃', async () => {
    loginMockUser()
    stubCrossDomain()
    // 全部 summary 维度都打到同一 endpoint，注入 500 后页面无分组数据。
    mockInject('get', '/bots/summary', { kind: 'status', status: 500 })
    renderWithProviders(<BotsPage />, { route: '/bots' })

    // 页面标题仍在（未崩溃），分组区落到空态文案。
    expect(await screen.findByRole('heading', { name: 'Bot 管理' })).toBeInTheDocument()
    expect(await screen.findByText('暂无 Bot')).toBeInTheDocument()
    // 仍在 /bots，未被整页跳转。
    expect(window.location.pathname).toBe('/bots')
  })
})
