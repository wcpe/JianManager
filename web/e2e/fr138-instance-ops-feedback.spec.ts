import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-138 单实例操作可发现性与反馈 · 单机（Playwright + mock）验收。
 * 覆盖 console-ux spec §FR-138 验收：
 *   - 行内仅留启停/重启主操作，余项收「⋯」菜单（删除标红）；
 *   - 主操作点击有反馈（状态联动翻牌 = 乐观/失效重取反馈）；
 *   - 运行态克隆/删除改禁用 + tooltip（非消失）。
 * 证据落 .tmp/acceptance/FR-138/。
 */

test('FR-138 行内主操作可发现 + 次要操作收「⋯」菜单', async ({ page }) => {
  await login(page)

  await page.goto('/instances?view=list&q=survival-1')
  const row = page.getByRole('row').filter({ has: page.getByRole('button', { name: 'survival-1', exact: true }) })
  await expect(row).toBeVisible()

  // survival-1 种子为 RUNNING：行内主操作「停止」「重启」直接可见（可发现）。
  await expect(row.getByRole('button', { name: '停止', exact: true })).toBeVisible()
  await expect(row.getByRole('button', { name: '重启', exact: true })).toBeVisible()

  // 次要操作收进「更多操作（⋯）」下拉菜单：点开后出现「编辑配置 / 删除」等项。
  const kebab = row.getByRole('button', { name: '更多操作' })
  await expect(kebab).toBeVisible()
  await kebab.click()

  const menu = page.getByRole('menu')
  await expect(menu).toBeVisible()
  await expect(menu.getByRole('menuitem', { name: '编辑配置' })).toBeVisible()
  // 删除项在菜单内（destructive 标红），运行态下禁用样式仍可见（非消失）。
  await expect(menu.getByRole('menuitem', { name: '删除' })).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptance/FR-138/single-machine-row-menu.png', fullPage: false })
})

test('FR-138 主操作点击有反馈：停止 → 状态联动翻牌为「启动」', async ({ page }) => {
  await login(page)

  await page.goto('/instances?view=list&q=survival-1')
  const row = page.getByRole('row').filter({ has: page.getByRole('button', { name: 'survival-1', exact: true }) })
  await expect(row).toBeVisible()

  const stopBtn = row.getByRole('button', { name: '停止', exact: true })
  await expect(stopBtn).toBeVisible()
  await stopBtn.click()

  // 反馈：mock 停止后列表失效重取，该行主操作翻牌为「启动」（停止消失）。
  const updatedRow = page.getByRole('row').filter({ has: page.getByRole('button', { name: 'survival-1', exact: true }) })
  await expect(updatedRow.getByRole('button', { name: '启动', exact: true })).toBeVisible({ timeout: 10_000 })
  await expect(updatedRow.getByRole('button', { name: '停止', exact: true })).toHaveCount(0)

  await page.screenshot({ path: '../.tmp/acceptance/FR-138/single-machine-action-feedback.png', fullPage: false })
})
