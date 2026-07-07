import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-147 Bot 规模化管理增强 · 单机（Playwright + mock 模式）验收。
 * 覆盖：舰队健康条多段（在线/连接中/异常/已停止）、分组维度（实例/节点/状态/行为）、
 * 每组批量操作下拉、创建 Bot。证据截图落 .tmp/acceptance/FR-147/。
 */

test('FR-147 舰队健康多段 + 分组维度 + 批量操作', async ({ page }) => {
  await login(page)
  await page.goto('/bots')

  await expect(page.getByRole('heading', { name: 'Bot 管理' })).toBeVisible()
  await expect(page.getByRole('button', { name: '创建 Bot' })).toBeVisible()

  // 舰队健康条四段
  await expect(page.getByText('舰队健康')).toBeVisible()
  await expect(page.getByText('在线', { exact: true }).first()).toBeVisible()
  await expect(page.getByText('连接中', { exact: true }).first()).toBeVisible()
  await expect(page.getByText('异常', { exact: true }).first()).toBeVisible()
  await expect(page.getByText('已停止', { exact: true }).first()).toBeVisible()

  // 分组维度 + 每组批量下拉
  await expect(page.getByRole('button', { name: '行为', exact: true })).toBeVisible()
  await expect(page.getByRole('combobox').filter({ hasText: '批量' }).first()).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptance/FR-147/single-machine-bots.png', fullPage: true })
})
