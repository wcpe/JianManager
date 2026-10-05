import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-131 侧边栏可折叠图标轨 + 隐藏滚动条 + 布局持久化 · 单机（Playwright + mock 模式）验收。
 * 覆盖：① 折叠开关把侧栏从展开（w-60）收缩为仅图标轨；② 导航区滚动条隐藏（scrollbar-none）；
 * ③ 折叠态持久化 localStorage（sidebar.collapsed），reload 后仍折叠。
 * 证据落 .tmp/acceptance/FR-131/。
 */

test('FR-131 折叠为图标轨 + 隐藏滚动条 + 折叠态刷新持久', async ({ page }) => {
  await login(page)
  const aside = page.locator('aside').first()

  // 展开态：宽度 > 180（w-60）
  const wExpanded = (await aside.boundingBox())!.width
  expect(wExpanded).toBeGreaterThan(180)

  // 导航区滚动条隐藏但保留滚动（FR-131）：由 .scrollbar-none 工具类承载（见 index.css，
  // ui-details.test.ts 亦有守护）。
  //
  // 不要写成 `aside.locator('nav').first()`——侧栏内有多个 nav（快捷项 SideNavShortcuts、
  // 资源树等），谁排第一取决于当前视图是「按节点 / 按群组 / 收藏」中的哪一种，
  // 于是断言会随视图漂移而失败，失败信息却是「类名不匹配」，看不出是定位问题。
  // 改为断言「侧栏内存在带该工具类的滚动区」，既守住 FR-131 的需求本身，
  // 又不绑定某一种具体的导航结构。
  // 加 :visible —— 侧栏（及其抽屉）内此类滚动区不止一处，其中有的处于隐藏态，
  // 不加就会匹配到隐藏的那个，失败信息仍是「不可见」，同样指不到真正的定位问题。
  const scrollArea = aside.locator('.scrollbar-none:visible').first()
  await expect(scrollArea).toBeVisible()

  // 点「收起侧栏」→ 收缩为仅图标轨（宽度 < 100）
  await page.getByRole('button', { name: '收起侧栏' }).first().click()
  await expect.poll(async () => (await aside.boundingBox())!.width).toBeLessThan(100)

  // 折叠态已写 localStorage（持久键 FR-131）
  const persisted = await page.evaluate(() => localStorage.getItem('sidebar.collapsed'))
  expect(persisted).toBe('1')

  await page.screenshot({ path: '../.tmp/acceptance/FR-131/single-machine-collapsed-rail.png', fullPage: false })

  // 刷新后仍折叠（布局持久化，刷新不重置）
  await page.reload()
  await expect(page.locator('[data-page="overview"]')).toBeVisible()
  await expect.poll(async () => (await page.locator('aside').first().boundingBox())!.width).toBeLessThan(100)
  await page.screenshot({ path: '../.tmp/acceptance/FR-131/single-machine-persisted-after-reload.png', fullPage: false })
})
