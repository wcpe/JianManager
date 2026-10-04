import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-213~221 观测体系重构 · 单机（Playwright + mock 模式）验收（前端消费面）。
 * FR-215 观测 IA / FR-216 通知中心 / FR-220 统计页 / FR-218 分发监控页。
 * 证据落 .tmp/acceptance/FR-21x/。
 */

test('FR-216 通知中心 统一站内信+告警流', async ({ page }) => {
  await login(page)
  await page.goto('/notifications')
  await expect(page.getByRole('heading', { name: '通知中心' })).toBeVisible()
  await expect(page.getByRole('button', { name: '全部已读' })).toBeVisible()
  await expect(page.getByText('CPU 过载告警').first()).toBeVisible() // 告警并入通知流
  await expect(page.getByRole('button', { name: '标记已读' }).first()).toBeVisible()
})

test('FR-220 统计页 平台级聚合', async ({ page }) => {
  await login(page)
  await page.goto('/statistics')
  await expect(page.getByRole('heading', { name: '统计' })).toBeVisible()
  await expect(page.getByText('实例·按状态')).toBeVisible()
  await expect(page.getByText('实例·按角色')).toBeVisible()
  await page.screenshot({ path: '../.tmp/acceptance/FR-220/single-machine-statistics.png', fullPage: true })
})

test('FR-215 观测 IA（监控/日志/统计 子类 + 客户端分发域）', async ({ page }) => {
  await login(page)
  await page.goto('/')
  // 导航重构后侧栏**跟随工作区切换**：观测类入口只在「观测与自动化」下出现。
  // 直接断言服务器运维的侧栏会得到「元素找不到」——很容易误判成入口被删。
  const workspaces = page.locator('[data-slot="top-nav-workspaces"]')
  await workspaces.getByRole('button', { name: '观测与自动化' }).click()
  const nav = page.locator('aside')
  await expect(nav.getByRole('link', { name: '监控总览' })).toBeVisible()
  await expect(nav.getByRole('link', { name: '日志中心' })).toBeVisible()
  await expect(nav.getByRole('link', { name: '统计分析' })).toBeVisible()

  // FR-430/431：旧「客户端分发监控」并入「客户端分发运维」，且该域整体归属**运营与分发**
  // 工作区（实测「观测与自动化」侧栏只有 监控总览/日志中心/统计分析/告警/任务中心/
  // 定时任务/备份/配置基线/通知中心，并无分发入口）。
  // 两个入口此时都是**链接**（「客户端分发」原为可展开按钮），且前者是后者的前缀，
  // 故必须 exact，否则 strict mode 会同时命中两个。
  await workspaces.getByRole('button', { name: '运营与分发' }).click()
  const opsNav = page.locator('aside')
  await expect(opsNav.getByRole('link', { name: '客户端分发', exact: true })).toBeVisible()
  await expect(opsNav.getByRole('link', { name: '客户端分发运维', exact: true })).toBeVisible()
})

test('FR-218/FR-430 旧监控路由重定向到客户端分发运维', async ({ page }) => {
  await login(page)
  await page.goto('/client-dist-monitor')
  await expect(page).toHaveURL(/\/client-dist-ops/)
  await expect(page.locator('[data-page="client-dist-ops"]')).toBeVisible()
  await expect(page.getByRole('heading', { name: '客户端分发运维' })).toBeVisible()
})
