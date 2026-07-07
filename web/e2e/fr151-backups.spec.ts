import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-151 备份页增强 · 单机（Playwright + mock 模式）验收。
 * 覆盖：顶部汇总（总占用/份数/最近成功）、校验和展示、增量行 + 删父依赖警告。
 * （进行中条件轮询/汇总/增量子计数逻辑由 backups-view.test.ts 单测覆盖）
 * 证据落 .tmp/acceptance/FR-151/。
 */

test('FR-151 备份汇总 + 校验和 + 增量链警告', async ({ page }) => {
  await login(page)
  await page.goto('/backups')
  await expect(page.getByRole('heading', { name: '备份管理' })).toBeVisible()

  // 选择实例 → 展示备份列表
  await page.getByRole('combobox').first().selectOption({ label: 'survival-1' })

  // 顶部汇总
  await expect(page.getByRole('button', { name: /总占用/ })).toBeVisible()
  await expect(page.getByRole('button', { name: /份数 \d+/ })).toBeVisible()
  await expect(page.getByRole('button', { name: /最近成功/ })).toBeVisible()
  // 校验和 + 增量
  await expect(page.getByText('校验和').first()).toBeVisible()
  await expect(page.getByText('增量', { exact: true }).first()).toBeVisible()
  // 删父备份依赖警告（挂在删除按钮 aria-description）
  await expect(page.getByRole('button', { name: '删除', description: /增量备份依赖/ }).first()).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptance/FR-151/single-machine-backups.png', fullPage: true })
})
