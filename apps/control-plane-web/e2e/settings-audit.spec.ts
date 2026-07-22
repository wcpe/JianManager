import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-158 设置与审计页增强 · 单机（Playwright + mock 模式）验收。
 * 覆盖：设置分类导航 + security 项隔离（安全/系统 分类）；审计 导出 + 行展开变更详情 + 真实命中总数 + 分页(加载更多)。
 * （未保存草稿拦截逻辑由 settings-form.test.ts 单测覆盖）
 * 证据落 .tmp/acceptance/FR-158/。
 */

test('FR-158a 设置分类导航 + 安全项隔离', async ({ page }) => {
  await login(page)
  await page.goto('/settings')
  await expect(page.getByRole('heading', { name: '系统设置' })).toBeVisible()
  for (const cat of ['外观', '日志', '运行时', '网络', '备份', '安全 / 系统']) {
    await expect(page.getByRole('button', { name: cat, exact: true })).toBeVisible()
  }
})

test('FR-158b 审计 导出 + 行展开 + 真实总数 + 加载更多', async ({ page }) => {
  await login(page)
  await page.goto('/audit')
  await expect(page.getByRole('heading', { name: '审计日志' })).toBeVisible()
  await expect(page.getByRole('button', { name: '导出' })).toBeVisible()
  await expect(page.getByText(/共 \d+ 条/)).toBeVisible() // 真实命中总数
  await expect(page.getByRole('button', { name: '加载更多' })).toBeVisible() // 分页

  // 行可展开看变更详情
  const row = page.getByRole('button', { name: /instance\.start/ })
  await expect(row).toHaveAttribute('aria-expanded', 'false')
  await row.click()
  await expect(row).toHaveAttribute('aria-expanded', 'true')

  await page.screenshot({ path: '../.tmp/acceptance/FR-158/single-machine-audit-expanded.png', fullPage: true })
})
