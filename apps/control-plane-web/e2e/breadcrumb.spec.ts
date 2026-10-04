import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-134 统一页头 · 单机（Playwright + mock 模式）验收。
 *
 * FR-496 阶段 6 补丁改写了本用例的契约：顶栏里的 `PageBreadcrumb` 整段移除——
 * 同一页的页名此前在顶栏（面包屑末级）与内容页（`PageHeader` 大标题）各出现一次，
 * 而原型《资源优先工作区》第 1 节明确「全局顶栏只做两件事（切工作区 + 全局工具），
 * 这里没有页面大标题」。现在**页名只由内容页承担**：内容页顶部的大标题随路由变化，
 * 顶栏不再输出任何页名/面包屑（对象详情页要的紧凑面包屑属阶段 7 的对象头）。
 * 证据落 .tmp/acceptance/FR-134/。
 */

test('FR-134 页名由内容页承担，顶栏不再输出面包屑', async ({ page }) => {
  await login(page)

  // 顶栏（banner）里没有任何面包屑导航。
  const banner = page.getByRole('banner')
  await expect(banner.getByRole('navigation', { name: 'breadcrumb' })).toHaveCount(0)

  // 节点页 → 内容页大标题「节点管理」
  await page.goto('/nodes')
  await expect(page.locator('[data-slot="console-main"] h1')).toHaveText('节点管理')
  await page.screenshot({ path: '../.tmp/acceptance/FR-134/single-machine-page-title-nodes.png', fullPage: false })

  // 观测/监控总览 → 「监控」（切页后页名随之变化）
  await page.goto('/monitor')
  await expect(page.locator('[data-slot="console-main"] h1')).toHaveText('监控')

  // 开源许可页 → 「开源许可」（FR-431 六域：licenses 挂平台设置分节）
  await page.goto('/licenses')
  await expect(page.locator('[data-slot="console-main"] h1')).toHaveText('开源许可')
  await page.screenshot({ path: '../.tmp/acceptance/FR-134/single-machine-page-title-licenses.png', fullPage: false })
})
