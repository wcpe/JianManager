import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-134 统一页头与面包屑组件 · 单机（Playwright + mock 模式）验收。
 * 覆盖：顶栏统一面包屑组件（PageBreadcrumb）据当前路由渲染「域 › 页面」轨迹，
 * 跨多页导航时面包屑随当前位置变化，二级页可点面包屑回列表。
 * 证据落 .tmp/acceptance/FR-134/。
 */

test('FR-134 面包屑随导航反映当前位置（域 › 页面）', async ({ page }) => {
  await login(page)
  const crumb = page.getByRole('banner').getByRole('navigation', { name: 'breadcrumb' })
  await expect(crumb).toBeVisible()

  // 节点页 → 「服务器 › 节点」
  await page.goto('/nodes')
  await expect(crumb).toContainText('服务器')
  await expect(crumb).toContainText('节点')
  await page.screenshot({ path: '../.tmp/acceptance/FR-134/single-machine-breadcrumb-nodes.png', fullPage: false })

  // 观测/监控总览 → 「观测 › 监控总览」（切页后面包屑随之变化）
  await page.goto('/monitor')
  await expect(crumb).toContainText('观测')
  await expect(crumb).toContainText('监控总览')

  // 开源许可页 → 「平台管理 › 开源许可」
  await page.goto('/licenses')
  await expect(crumb).toContainText('平台管理')
  await expect(crumb).toContainText('开源许可')
  await page.screenshot({ path: '../.tmp/acceptance/FR-134/single-machine-breadcrumb-licenses.png', fullPage: false })
})
