import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-235 实例列表页重设计（1000+ 实例）· 单机（Playwright + mock 模式）验收。
 * 覆盖 spec §5：1200 mock 实例下 `/instances` 首屏只请求分页搜索 `GET /instances/search`
 * 与聚合 `GET /instances/aggregate`，不再请求裸数组 `GET /instances`；虚拟渲染只挂可视窗口。
 * 证据落 .tmp/acceptance/FR-235/。
 */

test('FR-235 实例列表首屏走服务端分页搜索，不拉裸数组', async ({ page }) => {
  await login(page)

  // 在导航到 /instances 之前挂请求监听，捕获首屏所有 /api/v1/instances* 请求。
  const instancePaths: string[] = []
  page.on('request', (req) => {
    const { pathname } = new URL(req.url())
    if (pathname.startsWith('/api/v1/instances')) instancePaths.push(pathname)
  })

  await page.goto('/instances')

  // 首屏虚拟渲染壳就位（默认卡片视图），data-total-count 反映 1200 规模数据集。
  const surface = page.getByTestId('instances-card-virtual')
  await expect(surface).toBeVisible()
  await expect
    .poll(async () => Number((await surface.getAttribute('data-total-count')) ?? '0'))
    .toBeGreaterThanOrEqual(1000)

  // 首屏必须请求分页搜索 + 聚合端点。
  await expect.poll(() => instancePaths).toContain('/api/v1/instances/search')
  await expect.poll(() => instancePaths).toContain('/api/v1/instances/aggregate')

  // 首屏绝不请求裸数组列表端点 GET /instances（1200 全量拉取）。
  expect(instancePaths).not.toContain('/api/v1/instances')

  // 虚拟渲染只挂可视窗口 + overscan，远少于 1200 全量卡片。
  const cardCount = await page.getByTestId('instances-card-virtual-item').count()
  expect(cardCount).toBeLessThan(80)

  await page.screenshot({ path: '../.tmp/acceptance/FR-235/single-machine-instances.png', fullPage: false })
})

test('FR-235 URL 深链刷新后恢复筛选/排序/视图', async ({ page }) => {
  await login(page)

  const searchQueries: URLSearchParams[] = []
  page.on('request', (req) => {
    const url = new URL(req.url())
    if (url.pathname === '/api/v1/instances/search') searchQueries.push(url.searchParams)
  })

  // 深链带全套 URL 状态：搜索词 / 状态 / 列表视图 / 排序字段+方向 / 每页数量。
  await page.goto('/instances?q=server&status=RUNNING&view=list&sort=name&order=desc&pageSize=50')

  // 视图与控件从 URL 恢复。
  await expect(page.getByRole('searchbox', { name: '搜索实例' })).toHaveValue('server')
  await expect(page.getByRole('button', { name: '列表视图' })).toHaveAttribute('aria-pressed', 'true')

  // URL 状态确实下发到服务端分页搜索请求（而非前端假分页）。
  await expect
    .poll(() =>
      searchQueries.some(
        (p) =>
          p.get('q') === 'server' &&
          p.get('status') === 'RUNNING' &&
          p.get('sort') === 'name' &&
          p.get('order') === 'desc' &&
          p.get('pageSize') === '50',
      ),
    )
    .toBe(true)
})
