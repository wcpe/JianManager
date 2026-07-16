import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-148 趋势图增强 · 单机（Playwright + mock 模式）验收。
 * TimeSeriesChart：多序列常驻图例、窄幅指标 Y 轴 auto domain、空态 i18n。
 * 在指标最丰富的实例监控页断言（该图组件全站复用）。证据落 .tmp/acceptance/FR-148/。
 */

test('FR-148 多序列图例 + 空态 i18n（TimeSeriesChart）', async ({ page }) => {
  await login(page)
  // 趋势图由独立时序请求驱动，导航前监听响应，避免整套慢速运行时在数据回填前断言。
  const seriesResponsePromise = page.waitForResponse((response) => {
    const request = response.request()
    return request.method() === 'GET' && new URL(response.url()).pathname === '/api/v1/metrics/series'
  })
  await page.goto('/instances/1?tab=metrics')
  const seriesResponse = await seriesResponsePromise
  expect(seriesResponse.ok()).toBe(true)

  // 多序列常驻图例（堆内存：上限 + 已用）
  await expect(page.getByText('堆内存')).toBeVisible()
  await expect(page.getByText('上限', { exact: true }).first()).toBeVisible()
  await expect(page.getByText('已用', { exact: true }).first()).toBeVisible()

  // 空态 i18n（分世界实体无数据 → common.noData=「暂无数据」）
  await expect(page.getByText('分世界实体')).toBeVisible()
  await expect(page.getByText('暂无数据').first()).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptance/FR-148/single-machine-charts.png', fullPage: true })
})
