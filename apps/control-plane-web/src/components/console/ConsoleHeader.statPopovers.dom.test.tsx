import { describe, expect, it } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import { server } from '@/mocks/server'
import { db } from '@/mocks/db'
import type { MockInstance } from '@/mocks/handlers/domains/instance'
import ConsoleHeader from './ConsoleHeader'

/**
 * FR-294 页眉集群统计浮窗：三徽标（在线节点 / 运行服务器 / 崩溃服务器）点击弹缩略浮窗
 * （复用 FR-216 铃铛 DropdownMenu 范式），行点击 / 底部「查看全部」导航，
 * 数据仅浮窗打开时拉取（query enabled 绑定 open 态），行数上限 8 超出提示剩余数，崩溃空态友好文案。
 */

/** 收集所有实例域请求 URL（pathname + search），用于断言「打开浮窗才发请求」。 */
function collectInstanceRequests() {
  const urls: string[] = []
  const listener = ({ request }: { request: Request }) => {
    const url = new URL(request.url)
    if (url.pathname.startsWith('/api/v1/instances')) urls.push(url.pathname + url.search)
  }
  server.events.on('request:start', listener)
  return {
    urls,
    stop: () => server.events.removeListener('request:start', listener),
  }
}

/** 清空假后端全部 CRASHED 实例（崩溃空态用例）。 */
function removeCrashedInstances() {
  const instances = db<MockInstance>('instances')
  instances.list((i) => i.status === 'CRASHED').forEach((i) => instances.remove(i.id))
}

describe('ConsoleHeader 集群统计浮窗（FR-294）', () => {
  it('在线节点浮窗：行=节点名+运行实例数，底部查看全部→/nodes，行点击定位节点', async () => {
    loginMockUser()
    const user = userEvent.setup()
    renderWithProviders(<ConsoleHeader />, { route: '/instances' })

    await user.click(await screen.findByRole('button', { name: /在线节点/ }))
    const menu = await screen.findByRole('menu')

    // 行 = 节点名 + 该节点运行实例数（RUNNING 聚合就绪后 alpha 有非零运行数）。
    await within(menu).findByRole('menuitem', { name: /alpha/ })
    await waitFor(() =>
      expect(within(menu).getByRole('menuitem', { name: /alpha/ })).toHaveTextContent(/[1-9]\d* 个运行中/),
    )
    expect(within(menu).getByRole('menuitem', { name: /beta/ })).toBeInTheDocument()

    // 底部「查看全部节点」→ /nodes。
    await user.click(within(menu).getByRole('menuitem', { name: '查看全部节点' }))
    expect(window.location.pathname).toBe('/nodes')
    expect(window.location.search).toBe('')

    // 重新打开，行点击 → /nodes?node=<id> 定位该节点（FR-128 深链）。
    await user.click(screen.getByRole('button', { name: /在线节点/ }))
    const reopened = await screen.findByRole('menu')
    await user.click(within(reopened).getByRole('menuitem', { name: /alpha/ }))
    expect(window.location.pathname).toBe('/nodes')
    expect(window.location.search).toBe('?node=1')
  })

  it('运行中浮窗：行=实例名+节点名+在线人数，行点击进该服控制台', async () => {
    loginMockUser()
    const user = userEvent.setup()
    renderWithProviders(<ConsoleHeader />, { route: '/instances' })

    await user.click(await screen.findByRole('button', { name: /运行服务器/ }))
    const menu = await screen.findByRole('menu')

    // creative-proxy（id=20，nodeId=1→alpha）按名称排序在首屏 8 行内。
    const row = await within(menu).findByRole('menuitem', { name: /creative-proxy/ })
    expect(row).toHaveTextContent('alpha')
    // 在线人数：mock /instances/:id/metrics 返回 onlinePlayers=12，加载后行内出现。
    await waitFor(() => expect(within(menu).getAllByText(/12 人在线/).length).toBeGreaterThan(0))

    await user.click(row)
    expect(window.location.pathname).toBe('/instances/20')
  })

  it('运行中浮窗：行数上限 8，超出显示「还有 N 个，查看全部」并导航到 RUNNING 筛选页', async () => {
    loginMockUser()
    const user = userEvent.setup()
    renderWithProviders(<ConsoleHeader />, { route: '/instances' })

    await user.click(await screen.findByRole('button', { name: /运行服务器/ }))
    const menu = await screen.findByRole('menu')

    // 种子有数百运行实例：行数不超过 8，底部提示剩余数。
    const footer = await within(menu).findByRole('menuitem', { name: /还有 \d+ 个，查看全部/ })
    const rows = within(menu).getAllByRole('menuitem')
    expect(rows.length).toBeLessThanOrEqual(9) // 8 行 + footer

    await user.click(footer)
    expect(window.location.pathname).toBe('/instances')
    expect(window.location.search).toBe('?status=RUNNING')
  })

  it('崩溃浮窗：行=实例名+节点名+崩溃原因（statusReason），行点击进该服控制台', async () => {
    loginMockUser()
    const user = userEvent.setup()
    renderWithProviders(<ConsoleHeader />, { route: '/instances' })

    await user.click(await screen.findByRole('button', { name: /崩溃服务器/ }))
    const menu = await screen.findByRole('menu')

    // creative-1（id=3，nodeId=2→beta）带 statusReason。
    const row = await within(menu).findByRole('menuitem', { name: /creative-1/ })
    expect(row).toHaveTextContent('beta')
    expect(row).toHaveTextContent('实例未绑定 JDK，启动委托失败')

    await user.click(row)
    expect(window.location.pathname).toBe('/instances/3')
  })

  it('崩溃浮窗空态：显示友好文案，底部查看全部→CRASHED 筛选页', async () => {
    loginMockUser()
    removeCrashedInstances()
    const user = userEvent.setup()
    renderWithProviders(<ConsoleHeader />, { route: '/instances' })

    await user.click(await screen.findByRole('button', { name: /崩溃服务器/ }))
    const menu = await screen.findByRole('menu')

    await within(menu).findByText('没有崩溃的服务器')

    await user.click(within(menu).getByRole('menuitem', { name: '查看全部' }))
    expect(window.location.pathname).toBe('/instances')
    expect(window.location.search).toBe('?status=CRASHED')
  })

  it('数据仅浮窗打开时拉取：打开前无 search/metrics/RUNNING 聚合请求，打开后才发', async () => {
    loginMockUser()
    const requests = collectInstanceRequests()
    const user = userEvent.setup()
    try {
      renderWithProviders(<ConsoleHeader />, { route: '/instances' })

      // 等徽标计数数据源（既有聚合）就绪——这是徽标本身的数据，不属于浮窗。
      await screen.findByRole('button', { name: /运行服务器/ })
      await waitFor(() => expect(requests.urls.some((u) => u.startsWith('/api/v1/instances/aggregate'))).toBe(true))

      // 浮窗未打开：不发实例搜索、不发单实例 metrics、不发 RUNNING 聚合。
      expect(requests.urls.some((u) => u.includes('/instances/search'))).toBe(false)
      expect(requests.urls.some((u) => /\/instances\/\d+\/metrics/.test(u))).toBe(false)
      expect(requests.urls.some((u) => u.includes('/aggregate') && u.includes('status=RUNNING'))).toBe(false)

      // 打开运行中浮窗 → 才发 status=RUNNING 搜索与行内 metrics。
      await user.click(screen.getByRole('button', { name: /运行服务器/ }))
      await screen.findByRole('menu')
      await waitFor(() =>
        expect(requests.urls.some((u) => u.includes('/instances/search') && u.includes('status=RUNNING'))).toBe(true),
      )
      await waitFor(() => expect(requests.urls.some((u) => /\/instances\/\d+\/metrics/.test(u))).toBe(true))
    } finally {
      requests.stop()
    }
  })
})
