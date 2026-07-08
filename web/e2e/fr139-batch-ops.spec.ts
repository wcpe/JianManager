import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-139 批量操作增强 · 单机（Playwright + mock）验收。
 * 覆盖 console-ux spec §FR-139 验收：
 *   - 多选实例 → 批量操作栏出现（含状态感知禁用）；
 *   - 触发一个批量动作并得到反馈（批量重启直接下发 → 成功 toast）；
 *   - 停止/命令下发/强杀走二次确认弹窗（此处以停止确认弹窗验二次确认可发现）。
 * 证据落 .tmp/acceptance/FR-139/。
 */

test('FR-139 多选实例 → 批量栏出现并可触发批量重启（有反馈）', async ({ page }) => {
  await login(page)

  // 收敛到 survival* 三个 RUNNING 种子（survival-1 / survival-proxy / survival-lobby），列表视图取行内复选框。
  await page.goto('/instances?view=list&q=survival')
  await expect(page.getByTestId('instances-table-virtual')).toBeVisible()
  await expect(page.getByRole('button', { name: 'survival-1', exact: true })).toBeVisible()

  // 勾选两个实例（行内复选框 aria-label = 实例名，避开表头「全选」）。
  await page.getByRole('checkbox', { name: 'survival-1' }).check()
  await page.getByRole('checkbox', { name: 'survival-lobby' }).check()

  // 批量操作栏出现，显示「已选 2 个」。
  await expect(page.getByText('已选 2 个')).toBeVisible()

  // 批量重启（两者均 RUNNING → 动作可用，非禁用）；直接下发不需确认，得成功 toast。
  const restartBtn = page.getByRole('button', { name: '批量重启', exact: true })
  await expect(restartBtn).toBeEnabled()

  await page.screenshot({ path: '../.tmp/acceptance/FR-139/single-machine-batch-bar.png', fullPage: false })

  await restartBtn.click()
  // 反馈：批量完成 toast（instanceBatch.result）。
  await expect(page.getByText(/批量完成：成功/).first()).toBeVisible({ timeout: 10_000 })
})

test('FR-139 批量停止走二次确认弹窗（防回车直提）', async ({ page }) => {
  await login(page)

  await page.goto('/instances?view=list&q=survival')
  await expect(page.getByRole('button', { name: 'survival-1', exact: true })).toBeVisible()

  await page.getByRole('checkbox', { name: 'survival-1' }).check()
  await expect(page.getByText(/已选 1 个/)).toBeVisible()

  // 批量停止 → 弹出二次确认对话框（复述数量），而非立即执行。
  await page.getByRole('button', { name: '批量停止', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: '确认批量停止？' })
  await expect(dialog).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptance/FR-139/single-machine-batch-confirm.png', fullPage: false })

  // 确认执行 → 成功反馈。
  await dialog.getByRole('button', { name: '执行' }).click()
  await expect(page.getByText(/批量完成：成功/).first()).toBeVisible({ timeout: 10_000 })
})
