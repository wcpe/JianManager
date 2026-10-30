import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * 侧栏折叠/展开 · 单机（Playwright + mock 模式）验收。
 *
 * 【已重写】原用例验的是 ADR-071 的「点顶栏 logo 折叠/展开」。该入口已在
 * 「折叠后无法展开」的修复中删除：顶栏品牌区在折叠态被条件卸载，缩回去就没有出口。
 * 现在顶栏品牌区是**首页入口**（aria-label = nav.home「返回平台首页」），
 * 折叠入口只留侧栏底部常驻开关（data-testid=sidebar-collapse-toggle，
 * 由 DashboardPage.layout.dom.test.tsx 断言唯一）。
 * 因此本用例改写为验这两个真实契约：品牌区能回首页、底部开关能折叠/展开（可回）。
 * 证据落 .tmp/acceptance。
 */

test('顶栏品牌区回首页 + 侧栏底部开关可折叠且可再展开', async ({ page }) => {
  await login(page)
  const aside = page.locator('aside').first()
  const header = page.locator('[data-slot="console-header"]')
  const toggle = page.getByTestId('sidebar-collapse-toggle')

  const wExpanded = (await aside.boundingBox())!.width
  expect(wExpanded).toBeGreaterThan(180) // 展开态 w-60

  // ① 顶栏品牌区 = 首页入口（不再是折叠开关）
  await expect(header.getByRole('button', { name: '返回平台首页' })).toBeVisible()

  // ② 底部开关收起 → 图标轨
  await expect(toggle).toHaveAttribute('aria-label', '收起侧栏')
  await toggle.click()
  await expect.poll(async () => (await aside.boundingBox())!.width).toBeLessThan(100) // 图标轨
  await page.screenshot({ path: '../.tmp/acceptancesingle-machine-sidebar-collapsed.png', fullPage: false })

  // ③ 同一开关现在表示「展开」，再点可恢复 —— 这正是旧顶栏入口缺失的能力
  await expect(toggle).toHaveAttribute('aria-label', '展开侧栏')
  await toggle.click()
  await expect.poll(async () => (await aside.boundingBox())!.width).toBeGreaterThan(180)
})
