import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-059 危险操作保护体系化 · 单机（Playwright + mock 模式）验收。
 *
 * 覆盖 FR-059 核心验收「统一危险操作确认组件 + 高危操作要求输入名称二次校验」：
 * 在用户管理页对 seed 用户「operator」触发删除（scope=platform 的高危破坏性操作），
 * 断言弹出统一 DangerConfirm，且执行按钮在逐字输入资源名前一直禁用、输错仍禁用、输对才放行。
 * 以真实 UI 点击驱动（登录种子管理员 admin/admin123，role=10 为平台管理员，门禁放行→走 type-to-confirm 分支）。
 *
 * 证据落 .tmp/acceptance/FR-059/。
 */

test('FR-059 删除用户走统一确认弹窗且要求逐字输入名称二次校验', async ({ page }) => {
  await login(page)
  await page.goto('/users')

  // 用户列表渲染出 seed 用户 operator（种子第二个用户）。
  const operatorRow = page.getByRole('row', { name: /operator/ }).first()
  await expect(operatorRow).toBeVisible()

  // 触发该行删除 → 弹出统一 DangerConfirm（高危：平台范围删用户，要求输入用户名二次校验）。
  await operatorRow.getByRole('button', { name: '删除' }).click()

  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible()

  // 逐字校验输入框存在（placeholder 为待删资源名 operator），执行按钮初始禁用。
  const typeBox = dialog.getByPlaceholder('operator')
  await expect(typeBox).toBeVisible()
  const confirmBtn = dialog.getByRole('button', { name: '删除' })
  await expect(confirmBtn).toBeDisabled()

  // 输错名称 → 仍禁用（二次校验拦住误操作）。
  await typeBox.fill('wrong-name')
  await expect(confirmBtn).toBeDisabled()

  // 输对名称 → 放行（可执行）。此处只验门禁放行，不真正提交删除以免影响其它用例。
  await typeBox.fill('operator')
  await expect(confirmBtn).toBeEnabled()

  await page.screenshot({ path: '../.tmp/acceptance/FR-059/single-machine-danger-confirm.png', fullPage: true })
})
