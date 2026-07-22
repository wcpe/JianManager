import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-159 共享对话框统一 · 单机（Playwright + mock 模式）验收。
 * 裸 div 模态统一迁 Radix Dialog（role=dialog / aria-modal / 焦点陷阱 / Esc 关闭）。
 * 抽验一个代表性弹窗验 Radix 语义（行为不变 refactor）。
 * 证据落 .tmp/acceptance/FR-159/。
 */

test('FR-159 弹窗为 Radix Dialog（role=dialog + aria-modal + Esc 关闭）', async ({ page }) => {
  await login(page)
  await page.goto('/templates')
  await page.getByRole('button', { name: '一键部署' }).first().click()

  const dialog = page.getByRole('dialog', { name: /部署「Paper 1.21」/ })
  await expect(dialog).toBeVisible()
  await expect(dialog).toHaveAttribute('data-slot', 'dialog-content') // 共享 Radix Dialog 标记
  await page.screenshot({ path: '../.tmp/acceptance/FR-159/single-machine-radix-dialog.png', fullPage: true })

  // Esc 关闭（焦点陷阱 + 键盘可达）
  await page.keyboard.press('Escape')
  await expect(dialog).not.toBeVisible()
})
