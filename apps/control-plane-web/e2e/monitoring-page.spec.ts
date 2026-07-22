import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-169 监控页升级 · 单机（Playwright + mock 模式）验收。
 * 覆盖：平台/节点/实例 scope 下钻 + 每图时间筛选 + 实时刷新档 + 关键指标概览 + 多指标叠加对比。
 * （brush 拖拽轴 / hover 浮窗由 chart-hover.test.ts 单测 + TimeSeriesChart 覆盖）
 * 证据落 .tmp/acceptance/FR-169/。
 */

test('FR-169 监控 scope下钻 + 时间筛选 + 实时 + 关键指标 + 多指标对比', async ({ page }) => {
  await login(page)
  await page.goto('/monitor')

  await expect(page.getByRole('heading', { name: '监控' })).toBeVisible()
  // 实时刷新档
  await expect(page.getByRole('tab', { name: '自动' }).first()).toBeVisible()
  await expect(page.getByRole('tab', { name: '30 秒' }).first()).toBeVisible()
  // 时间范围筛选（每图各有一组 → 多个 24h tab）
  await expect(page.getByRole('tab', { name: '24h' }).first()).toBeVisible()
  // scope 下钻到实例（平台→节点→实例）
  await expect(page.getByRole('combobox', { name: '下钻到实例' })).toBeVisible()
  // 关键指标概览 + 多指标对比叠加
  await expect(page.getByText('关键指标概览')).toBeVisible()
  await expect(page.getByText('多指标对比')).toBeVisible()
  await expect(page.getByText(/勾选下方指标在同一图内叠加对比/)).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptance/FR-169/single-machine-monitor.png', fullPage: true })
})
