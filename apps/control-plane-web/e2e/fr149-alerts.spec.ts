import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-149 告警增强 · 单机（Playwright + mock 模式）验收。
 * 覆盖：规则表内联启停 Switch；事件加时间范围/规则/关键字筛选。
 * （静默跨夜 formatSilenceWindow 与通道测试由单测/组件测覆盖）
 * 证据落 .tmp/acceptance/FR-149/。
 */

test('FR-149 规则内联 Switch + 事件多维筛选', async ({ page }) => {
  await login(page)
  await page.goto('/alerts')

  await expect(page.getByRole('heading', { name: '告警管理' })).toBeVisible()
  // 规则表内联启停 Switch
  await expect(page.getByRole('switch', { name: '启用' }).first()).toBeVisible()

  // 事件 tab：关键字搜索 + 规则筛选 + 时间范围
  await page.getByRole('button', { name: /^事件/ }).click()
  await expect(page.getByPlaceholder('搜索消息')).toBeVisible()
  await expect(page.getByRole('combobox').filter({ hasText: '全部规则' })).toBeVisible()
  // 时间范围：datetime-local 输入（原生选择器）
  await expect(page.locator('input[type="datetime-local"]').first()).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptance/FR-149/single-machine-alerts-events.png', fullPage: true })
})
