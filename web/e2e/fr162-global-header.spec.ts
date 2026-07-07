import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-162 全局页眉/顶栏 · 单机（Playwright + mock 模式）验收。
 * 覆盖：面包屑 + 搜索入口 + 集群概览徽标（在线节点/运行服务器/崩溃）可点跳筛选 + 通知铃铛 + 账户菜单。
 * （搜索本期为命令面板入口 FR-241；通知合并 FR-216）
 * 证据落 .tmp/acceptance/FR-162/。
 */

test('FR-162 顶栏 集群徽标+搜索+通知+账户 与徽标可点跳筛选', async ({ page }) => {
  await login(page)

  const banner = page.getByRole('banner')
  await expect(banner.getByRole('navigation', { name: 'breadcrumb' })).toBeVisible() // 面包屑
  await expect(banner.getByRole('button', { name: /搜索服务器/ })).toBeVisible() // 搜索入口
  await expect(banner.getByRole('button', { name: /在线节点/ })).toBeVisible() // 集群徽标
  await expect(banner.getByRole('button', { name: /运行服务器/ })).toBeVisible()
  await expect(banner.getByRole('button', { name: /崩溃服务器/ })).toBeVisible()
  await expect(banner.getByRole('button', { name: '通知' })).toBeVisible() // 告警/站内信铃铛
  await expect(banner.getByRole('button', { name: '账户' })).toBeVisible() // 账户菜单
  await page.screenshot({ path: '../.tmp/acceptance/FR-162/single-machine-header.png', fullPage: true })

  // 集群徽标可点 → 跳对应筛选（运行服务器 → 实例列表）
  await banner.getByRole('button', { name: /运行服务器/ }).click()
  await expect(page).toHaveURL(/\/instances/)
})
