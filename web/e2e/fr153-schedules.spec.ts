import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-153 计划任务增强 · 单机（Playwright + mock 模式）验收。
 * 覆盖：列表 cron 可读描述；创建对话框 常用预设 + 可读描述 + 接下来 3 次执行 + 时区标注。
 * （编辑 command 回填由 schedule-form.test.ts 单测覆盖）
 * 证据落 .tmp/acceptance/FR-153/。
 */

test('FR-153 cron 可读描述 + 预设 + 下次执行预览 + 时区标注', async ({ page }) => {
  await login(page)
  await page.goto('/schedules')

  await expect(page.getByRole('heading', { name: '定时任务' })).toBeVisible()
  await expect(page.getByText('每天 04:00 执行').first()).toBeVisible() // 列表可读描述

  await page.getByRole('button', { name: '+ 创建任务' }).click()
  const dialog = page.getByRole('dialog', { name: '创建任务' })
  // 常用预设
  await expect(dialog.getByRole('button', { name: '每天 4:00' })).toBeVisible()
  await expect(dialog.getByRole('button', { name: '每周一 4:00' })).toBeVisible()
  // 点预设「每天 4:00」→ cron=0 4 * * * → 可读描述 + 下次执行 + 时区标注
  await dialog.getByRole('button', { name: '每天 4:00' }).click()
  await expect(dialog.getByText('每天 04:00 执行')).toBeVisible()
  await expect(dialog.getByText('下次执行')).toBeVisible()
  await expect(dialog.getByText(/预览按浏览器本地时区/)).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptance/FR-153/single-machine-schedule-dialog.png', fullPage: true })
})
