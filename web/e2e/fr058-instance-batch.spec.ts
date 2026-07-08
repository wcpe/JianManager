import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-058 实例批量操作 · 单机（Playwright + mock 模式）验收。
 * 覆盖：列表视图行内多选 → 浮现批量操作栏（已选计数）→ 直接动作（批量重启）落结果反馈；
 * 危险动作（批量强制关服）弹二次确认并要求输入 FORCE 关键字后才放行。
 * （批量下发 payload、部分失败保留重试、状态感知禁用由 InstanceBatchBar.dom.test.tsx 单测覆盖）
 *
 * 说明：实例页默认卡片虚拟视图（FR-235），批量多选复选框在列表视图，故用 `view=list` 深链；
 * mock 生成 1200 实例（server-XXXX），status = STATUS_POOL[id%5]，故 id%5==0 为 RUNNING。
 * 用 `q=` + `status=RUNNING` 深链把可见集合钉到已知运行中实例，行复选框 aria-label = 实例名。
 * 证据落 .tmp/acceptance/FR-058/。
 */

test('FR-058 列表多选 → 批量操作栏 → 批量重启结果反馈 + 强制关服 FORCE 二次确认', async ({ page }) => {
  await login(page)
  // 列表视图 + 仅看运行中 + 搜索钉到 server-0005（id=5，RUNNING）。
  await page.goto('/instances?view=list&status=RUNNING&q=server-0005')

  // 列表视图表格就位，目标运行中实例行可见（行复选框 aria-label = 实例名）。
  const rowCheckbox = page.getByRole('checkbox', { name: 'server-0005' }).first()
  await expect(rowCheckbox).toBeVisible({ timeout: 10_000 })
  await rowCheckbox.check()

  // 选中后浮现批量操作栏，展示已选计数。
  await expect(page.getByText('已选 1 个')).toBeVisible()

  // 批量重启（非危险动作，无需二次确认）→ 直接执行，落结果反馈 toast。
  await page.getByRole('button', { name: '批量重启' }).click()
  await expect(page.getByText(/批量完成：成功 \d+/).first()).toBeVisible({ timeout: 10_000 })

  // 成功后批量栏清空选择（onClear），需重新选中该运行中实例再演示危险动作。
  await expect(rowCheckbox).not.toBeChecked()
  await rowCheckbox.check()
  await expect(page.getByText('已选 1 个')).toBeVisible()

  // 危险动作：批量强制关服需输入 FORCE 才放行。
  await page.getByRole('button', { name: '批量强制关服' }).click()
  const killDialog = page.getByRole('dialog', { name: '确认批量强制关服？' })
  await expect(killDialog).toBeVisible()
  const execute = killDialog.getByRole('button', { name: '执行' })
  await expect(execute).toBeDisabled()
  await killDialog.getByPlaceholder('FORCE').fill('FORCE')
  await expect(execute).toBeEnabled()

  await page.screenshot({ path: '../.tmp/acceptance/FR-058/single-machine-instance-batch.png', fullPage: true })

  await execute.click()
  await expect(page.getByText(/批量完成：成功 \d+/).first()).toBeVisible({ timeout: 10_000 })
})
