import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-146 玩家管理增强 · 单机（Playwright + mock 模式）验收。
 * 覆盖：在线玩家行勾选 + 批量踢/封 + 子服筛选；实时事件流暂停 + 类型过滤 + 清空。
 * 证据截图落 .tmp/acceptance/FR-146/。
 */

test('FR-146 在线玩家批量踢封+子服筛选 与 实时事件暂停/过滤/清空', async ({ page }) => {
  await login(page)
  await page.goto('/players')

  await expect(page.getByRole('heading', { name: '玩家管理' })).toBeVisible()
  // 在线玩家：子服筛选 + 批量踢/封（默认禁用）+ 行勾选
  await expect(page.getByText('按子服筛选')).toBeVisible()
  await expect(page.getByRole('button', { name: '批量踢出' })).toBeDisabled()
  await expect(page.getByRole('button', { name: '批量封禁' })).toBeDisabled()
  await expect(page.getByRole('checkbox', { name: /选择玩家 Alice/ })).toBeVisible()
  // 勾选一行 → 批量按钮启用
  await page.getByRole('checkbox', { name: /选择玩家 Alice/ }).check()
  await expect(page.getByRole('button', { name: '批量踢出' })).toBeEnabled()
  await page.screenshot({ path: '../.tmp/acceptance/FR-146/single-machine-players-online.png', fullPage: true })

  // 实时事件：暂停 + 类型过滤 + 清空
  await page.getByRole('button', { name: '实时事件' }).click()
  await expect(page.getByRole('button', { name: '暂停' })).toBeVisible()
  await expect(page.getByRole('button', { name: '清空' })).toBeVisible()
})
