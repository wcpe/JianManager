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

test('FR-215 观测 IA（监控/日志/统计 子类 + 分发监控）', async ({ page }) => {
  await login(page)
  await page.goto('/')
  const nav = page.locator('aside')
  await expect(nav.getByRole('link', { name: '监控总览' })).toBeVisible()
  await expect(nav.getByRole('link', { name: '日志中心' })).toBeVisible()
  await expect(nav.getByRole('link', { name: '统计分析' })).toBeVisible()
  await expect(nav.getByRole('link', { name: '客户端分发监控' })).toBeVisible()
})

test('FR-218 客户端分发监控页', async ({ page }) => {
  await login(page)
  await page.goto('/client-dist-monitor')
  await expect(page.locator('[data-page="client-dist-monitor"]')).toBeVisible()
  await expect(page.getByRole('heading').first()).toBeVisible()
})
