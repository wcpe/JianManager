import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-144 节点页直观化 · 单机（Playwright + mock 模式）验收。
 *
 * FR-177 先重做主从双栏；FR-496 阶段 6 又照原型 `nodesPage()` 拆成**列表态与详情态**：
 *   /nodes            → 列表态：集群概览（状态计数）+ 本地搜索 + 添加节点 + 节点卡片墙
 *   /nodes?node=<id>  → 详情态：对象头 + 分段工具
 *
 * 故本文件按这两种形态分别断言——旧的单用例在 /nodes 同时断言列表与详情，已不成立。
 * 证据截图落 .tmp/acceptance/FR-144/。
 */

test('FR-144 节点列表态：集群概览 + 本地搜索 + 添加节点 + 卡片墙', async ({ page }) => {
  await login(page)
  await page.goto('/nodes')

  await expect(page.getByRole('heading', { name: '节点管理' })).toBeVisible()

  // 集群概览：在线/离线/维护 计数（作用域条内，可点按状态筛）。
  // 用 ^…$ 锚定：卡片按钮的可访问名里也含「在线」，不加锚点会撞 strict mode。
  await expect(page.getByRole('button', { name: /^在线 \d+$/ })).toBeVisible()
  await expect(page.getByRole('button', { name: /^离线 \d+$/ })).toBeVisible()
  await expect(page.getByRole('button', { name: /^维护中 \d+$/ })).toBeVisible()

  // 本地搜索 + 添加节点。
  await expect(page.getByPlaceholder('搜索名称 / host')).toBeVisible()
  await expect(page.getByRole('button', { name: '添加节点' })).toBeVisible()

  // 卡片墙：卡片本身是可点进详情的按钮（可访问名收敛为「节点名 + 状态」）。
  await expect(page.getByRole('button', { name: /^alpha / }).first()).toBeVisible()
  await expect(page.getByRole('button', { name: /^beta / }).first()).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptance/FR-144/single-machine-nodes-list.png', fullPage: true })
})

test('FR-144 节点详情态：操作 kebab + 分段工具', async ({ page }) => {
  await login(page)
  await page.goto('/nodes?node=1')

  // 注：详情态目前仍是旧的双栏右栏（阶段 6 第二步尚未重做），其标题在左栏内，
  // 待第二步换成对象头后，这里应改为断言对象名 heading。
  await expect(page.getByRole('heading', { name: '节点管理' })).toBeVisible()

  await expect(page.getByRole('button', { name: '操作' }).first()).toBeVisible()
  await expect(page.getByRole('button', { name: '概览', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '运行时', exact: true })).toBeVisible() // FR-311：JDK 分段更名运行时
  await expect(page.getByRole('button', { name: '制品缓存', exact: true })).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptance/FR-144/single-machine-node-detail.png', fullPage: true })
})
