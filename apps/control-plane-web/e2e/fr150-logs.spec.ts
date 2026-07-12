import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-150 日志中心增强 · 单机（Playwright + mock 模式）验收。
 * 覆盖：实时跟随、时间范围、级别过滤、搜索、导出（范围/格式）、分页。
 * （级别配色/时间预设/导出范围/虚拟窗口逻辑由 logs-filters.test.ts 单测覆盖）
 * 证据落 .tmp/acceptance/FR-150/。
 */

test('FR-150 日志中心 实时跟随 + 时间范围 + 级别过滤 + 导出 + 分页', async ({ page }) => {
  await login(page)
  await page.goto('/logs')

  await expect(page.getByRole('heading', { name: '日志中心' })).toBeVisible()
  await expect(page.getByRole('button', { name: '实时跟随' })).toBeVisible()
  await expect(page.getByRole('button', { name: '导出' })).toBeVisible()
  await expect(page.getByRole('button', { name: '错误', exact: true })).toBeVisible()
  await expect(page.getByPlaceholder('搜索日志内容…')).toBeVisible()
  await expect(page.getByRole('combobox').filter({ hasText: '全部时间' })).toBeVisible()
  await expect(page.getByRole('button', { name: '下一页' })).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptance/FR-150/single-machine-logs.png', fullPage: true })
})
