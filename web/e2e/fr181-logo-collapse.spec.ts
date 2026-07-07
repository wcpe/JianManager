import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-181 Logo 点击折叠/展开导航栏 · 单机（Playwright + mock 模式）验收。
 * logo（Boxes + JianManager）作为按钮复用 toggleSidebar：展开态点击=收起为图标轨，再点=展开。
 * 证据落 .tmp/acceptance/FR-181/。
 */

test('FR-181 点 logo 折叠/展开侧栏', async ({ page }) => {
  await login(page)
  const aside = page.locator('aside').first()
  const wExpanded = (await aside.boundingBox())!.width
  expect(wExpanded).toBeGreaterThan(180) // 展开态 w-60

  // 点 logo → 收起为图标轨
  await page.getByRole('button', { name: /收起导航|收起侧栏|JianManager/ }).first()
    .click()
    .catch(async () => { await page.locator('aside a, aside button').filter({ hasText: 'JianManager' }).first().click() })
  await expect.poll(async () => (await aside.boundingBox())!.width).toBeLessThan(100) // 图标轨

  await page.screenshot({ path: '../.tmp/acceptance/FR-181/single-machine-sidebar-collapsed.png', fullPage: false })
})
