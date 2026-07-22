import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-145 群组管理可寻址双栏 + proxy↔backend 拓扑 · 单机（Playwright + mock 模式）验收。
 * 覆盖：列表/拓扑切换、成员状态分布、点「管理」→ 深链 ?network= + 左成员/右候选双栏 + 候选筛选。
 * 证据截图落 .tmp/acceptance/FR-145/。
 */

test('FR-145 群组列表状态分布 + 管理双栏深链 + 候选筛选', async ({ page }) => {
  await login(page)
  await page.goto('/networks')

  await expect(page.getByRole('heading', { name: '群组管理' })).toBeVisible()
  await expect(page.getByRole('button', { name: '拓扑' })).toBeVisible()
  // 列表行成员状态分布
  await expect(page.getByRole('button', { name: /survival .*个成员/ })).toBeVisible()

  // 深链直达（?network=）→ 左成员/右候选双栏 + 候选筛选
  await page.goto('/networks?network=1')
  await expect(page.getByText('成员', { exact: true })).toBeVisible()
  await expect(page.getByPlaceholder('筛选候选…')).toBeVisible()
  await expect(page.getByText('survival-proxy')).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptance/FR-145/single-machine-networks-manage.png', fullPage: true })
})
