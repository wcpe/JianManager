import { test, expect } from '@playwright/test'
import { login } from './helpers'

/** 
 *共享对话框统一 · 单机（Playwright + mock 模式）验收。
 * 裸 div 模态统一迁 Radix Dialog（role=dialog / aria-modal / 焦点陷阱 / Esc 关闭）。
 * 抽验一个代表性弹窗验 Radix 语义（行为不变 refactor）。
 * 证据落 .tmp/acceptance。
 */

test('弹窗为 Radix Dialog（role=dialog + aria-modal + Esc 关闭）', async ({ page }) => {
  await login(page)
  await page.goto('/templates')
  // 【为何不直接 getByRole('button',{name:'一键部署'}).first()】该可及名会匹配到 **4 个**按钮：
  // 页顶「一键开服」统计卡里的 <Zap> 图标（16×16、文本为空）也是 button，且排在最前。
  // `.first()` 点到的是那个图标——点了没反应，报错却是「对话框不存在」，极易误判成产品 bug。
  // 真正能开弹窗的是卡片底部那个 `w-full` 按钮。
  await page.locator('button.w-full').filter({ hasText: '一键部署' }).first().click()

  const dialog = page.getByRole('dialog', { name: /部署「Paper 1.21」/ })
  await expect(dialog).toBeVisible()
  await expect(dialog).toHaveAttribute('data-slot', 'dialog-content') // 共享 Radix Dialog 标记
  await page.screenshot({ path: '../.tmp/acceptancesingle-machine-radix-dialog.png', fullPage: true })

  // Esc 关闭（焦点陷阱 + 键盘可达）
  await page.keyboard.press('Escape')
  await expect(dialog).not.toBeVisible()
})
