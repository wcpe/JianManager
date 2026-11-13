import { describe, expect, it } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import { server } from '@jianmanager/devmock/server'
import { db } from '@jianmanager/devmock/db'
import type { MockInstance } from '@jianmanager/devmock/handlers/domains/instance'
import ConsoleHeader from './ConsoleHeader'

/** 
 *页眉集群统计浮窗：三徽标（在线节点 / 运行服务器 / 崩溃服务器）点击弹缩略浮窗
 * （复用铃铛 DropdownMenu 范式），行点击 / 底部「查看全部」导航，
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

/** 
 * 把假后端节点裁到只剩 [keep] 个（用于覆盖浮窗底部 `viewAllNodes` 分支）。
 *
 * 为什么要裁：`StatPopoverFooter` 的文案取决于 remaining = 总数 - 浮窗上限(8)。
 * 默认 mock 有 37 个节点，remaining 恒为正，只会走 `header.moreCount`；
 * 只有把总数压到 ≤ 8，才会走另一分支 `header.viewAllNodes`（「查看全部节点」）。
 * 两个分支都是真实用户可见的文案，各自都该有用例守护。
 */
function keepOnlyNodes(keep: number) {
  const nodes = db<{ id: number }>('nodes')
  nodes.list().forEach((n, index) => {
    if (index >= keep) nodes.remove(n.id)
  })
}

describe('ConsoleHeader 集群统计浮窗', () => {
  it('在线节点浮窗：行=节点名+运行实例数，底部查看全部→/nodes，行点击定位节点', async () => {
    // 本用例断言的是**底部 `viewAllNodes` 分支**（「查看全部节点」），
    // 而该分支只在 `remaining == 0`（总数 ≤ 浮窗上限 8）时渲染；
    // 默认档位带 34 台压力种子，总数 37 → remaining 恒为正，走的是 `moreCount` 分支。
    // 因此这里必须先把节点裁到上限以内，否则断言的是另一个分支的文案。
    keepOnlyNodes(2)
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

    // 底部入口 → /nodes。
    //
    // 文案有两个分支（见 ClusterBadges 的 StatPopoverFooter）：
    //   remaining > 0 → `header.moreCount`（「还有 N 个，查看全部」）
    //   remaining = 0 → `header.viewAllNodes`（「查看全部节点」）
    // mock 有 37 个节点、浮窗上限 8，故这里必然走 moreCount 分支。
    // 早先本行断言的是精确名「查看全部节点」，在 37 节点下永远匹配不到；
    // 这里放宽为「含『查看全部』」，并把 label 分支单独交给下一个用例覆盖。
    await user.click(within(menu).getByRole('menuitem', { name: /查看全部/ }))
    expect(window.location.pathname).toBe('/nodes')
    expect(window.location.search).toBe('')

    // 重新打开，行点击 → /nodes?node=<id> 定位该节点（深链）。
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

      /* 
        【契约变更·本轮】崩溃实例（`status=CRASHED`）改为**常驻取数**：
        它同时供集群徽标的崩溃浮窗与页眉「需要处理」使用，若仍绑定浮窗开合，
        铃铛里永远看不到崩溃实例——而那是首屏最该报的一类。窗口仍是很小的 8 条。

        其余按需取数的契约不变，仍是本用例真正守护的东西：
        运行中搜索、单实例 metrics、RUNNING 聚合都必须等到浮窗打开才发。
      */
      expect(
        requests.urls.some((u) => u.includes('/instances/search') && !u.includes('status=CRASHED')),
      ).toBe(false)
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
  /** 
   * 底部 `viewAllNodes` 分支（节点数 ≤ 浮窗上限时 remaining = 0）。
   *
   * 与上一个用例互补：那边 37 个节点走 `moreCount`（「还有 N 个，查看全部」），
   * 这边把节点裁到 3 个，remaining 归零，应显示不带计数的「查看全部节点」。
   */
  it('节点数不超过浮窗上限时，底部显示不带计数的「查看全部节点」并跳转', async () => {
    keepOnlyNodes(3)
    loginMockUser()
    const user = userEvent.setup()
    renderWithProviders(<ConsoleHeader />, { route: '/instances' })

    await user.click(await screen.findByRole('button', { name: /在线节点/ }))
    const menu = await screen.findByRole('menu')

    // remaining = 0 → 走 label 分支
    const footer = await within(menu).findByRole('menuitem', { name: '查看全部节点' })
    // 明确断言「不带计数」：多一个「还有 N 个」就说明分支选错了
    expect(footer).not.toHaveTextContent(/还有 \d+ 个/)

    await user.click(footer)
    expect(window.location.pathname).toBe('/nodes')
    expect(window.location.search).toBe('')
  })
})
