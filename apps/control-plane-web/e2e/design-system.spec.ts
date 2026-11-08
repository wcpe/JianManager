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
  await expect(page.locator('[data-page="instance-console"]')).toBeVisible()
  /**
   * 只保留一个断言（2026-10-20 消除 flaky）。
   *
   * 原本这里有两行：先 `expect(...first()).toBeVisible()`，再
   * `expect(await page.locator(...).count()).toBeGreaterThanOrEqual(1)`。
   * 两者验证的是同一个事实（「至少有一个 Panel」），但**语义强度不同**：前者会自动
   * 重试直到出现，后者是**无等待的一次性快照**。`?tab=metrics` 的 tab 切换发生在
   * 导航之后，panel 挂载与这次取快照之间存在时间窗，慢机上就取到 0 ⇒ 偶发失败，
   * 而失败信息只显示「Expected >= 1, Received 0」，与真实原因（时序）完全脱节。
   *
   * 保留 `toBeVisible`（有重试、失败信息指向具体的定位器），删掉冗余的 count 断言——
   * 不要用无等待断言去复核一个正在异步渲染的事实。
   */
  await expect(page.locator('[data-slot="panel"]').first()).toBeVisible()

  // StatCard 底座（节点页）
  await page.goto('/nodes')
  await expect(page.locator('[data-slot="stat-card"]').first()).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptance/FR-163/single-machine-design-system.png', fullPage: true })
})
