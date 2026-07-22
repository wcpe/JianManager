import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-144 节点页直观化 · 单机（Playwright + mock 模式）验收。
 * 现由 FR-177 节点管理页重做交付：主从双栏 + 集群概览（在线/离线/维护 + 集群 CPU/内存/磁盘）
 * + 主列表搜索 + 详情分段 + 操作 kebab。证据截图落 .tmp/acceptance/FR-144/。
 */

test('FR-144 节点页集群概览 + 主从双栏 + 详情分段 + 操作 kebab', async ({ page }) => {
  await login(page)
  await page.goto('/nodes')

  await expect(page.getByRole('heading', { name: '节点管理' })).toBeVisible()

  // 集群概览：在线/离线/维护 计数 + 集群 CPU/内存/磁盘 聚合水位
  await expect(page.getByRole('button', { name: /在线 \d+/ })).toBeVisible()
  await expect(page.getByRole('button', { name: /离线 \d+/ })).toBeVisible()
  await expect(page.getByRole('button', { name: /维护中 \d+/ })).toBeVisible()

  // 主列表搜索 + 添加节点
  await expect(page.getByPlaceholder('搜索名称 / host')).toBeVisible()
  await expect(page.getByRole('button', { name: '添加节点' })).toBeVisible()

  // 详情：操作 kebab + 分段 tab
  await expect(page.getByRole('button', { name: '操作' }).first()).toBeVisible()
  await expect(page.getByRole('button', { name: '概览', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '运行时', exact: true })).toBeVisible() // FR-311：JDK 分段更名运行时
  await expect(page.getByRole('button', { name: '制品缓存', exact: true })).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptance/FR-144/single-machine-nodes.png', fullPage: true })
})
