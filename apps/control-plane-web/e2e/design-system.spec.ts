import { test, expect } from '@playwright/test'
import { login } from './helpers'

/** 
 *视觉底座与设计系统 · 单机（Playwright + mock 模式）验收。
 * 覆盖：统一 Panel（data-slot=panel）/ StatCard（data-slot=stat-card）共享组件渲染，弃 shadcn Card 松散用法。
 * 证据落 .tmp/acceptance。
 */

test('共享 Panel / StatCard 设计底座渲染', async ({ page }) => {
  test.setTimeout(90_000)
  await login(page)

  // Panel 底座（实例监控段 ChartPanel → Panel data-slot）
  await page.goto('/instances/1?tab=metrics')
  await expect(page.locator('[data-page="instance-console"]')).toBeVisible({ timeout: 15_000 })
  await expect(page.locator('[data-slot="panel"]').first()).toBeVisible({ timeout: 15_000 })
  expect(await page.locator('[data-slot="panel"]').count()).toBeGreaterThanOrEqual(1)

  // StatCard 底座。
  // 【为何不再指 /nodes 详情】节点详情的水位已改为对象页头里的一组环形仪表
  // （`data-testid="node-detail-header-gauges"`，与首页同一套 `ResourceGauge`），
  // 左栏不再有 StatCard。改为指向仍在用 StatCard 的客户端分发概览。
  await page.goto('/client-dist-ops')
  await expect(page.locator('[data-slot="stat-card"]').first()).toBeVisible({ timeout: 20_000 })

  await page.screenshot({ path: '../.tmp/acceptancesingle-machine-design-system.png', fullPage: true })
})
