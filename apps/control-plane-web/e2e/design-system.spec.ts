import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-163 视觉底座与设计系统 · 单机（Playwright + mock 模式）验收。
 * 覆盖：统一 Panel（data-slot=panel）/ StatCard（data-slot=stat-card）共享组件渲染，弃 shadcn Card 松散用法。
 * 证据落 .tmp/acceptance/FR-163/。
 */

test('FR-163 共享 Panel / StatCard 设计底座渲染', async ({ page }) => {
  await login(page)

  // Panel 底座（实例监控段 ChartPanel → Panel data-slot）
  await page.goto('/instances/1?tab=metrics')
  await expect(page.locator('[data-page="instance-console"]')).toBeVisible({ timeout: 15_000 })
  await expect(page.locator('[data-slot="panel"]').first()).toBeVisible({ timeout: 15_000 })
  expect(await page.locator('[data-slot="panel"]').count()).toBeGreaterThanOrEqual(1)

  // StatCard 底座（节点页）
  await page.goto('/nodes')
  await expect(page.locator('[data-slot="stat-card"]').first()).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptance/FR-163/single-machine-design-system.png', fullPage: true })
})
