import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-142 详情页监控与探针增强 · 单机（Playwright + mock 模式）验收。
 * 覆盖两条验收：
 *  1) 监控面板 TPS/MSPT/CPU 当前阈值色条 + 历史曲线阈值标记（带 RangePicker）
 *  2) 探针状态卡（未装可操作指引）+ 停机时当前指标折叠不占空白
 * 证据截图落 .tmp/acceptance/FR-142/。
 */

const SHOT = (name: string) => ({ path: `../.tmp/acceptance/FR-142/${name}` })

test('FR-142 运行态：当前健康阈值色条 + 探针卡 + 历史曲线阈值标记', async ({ page }) => {
  await login(page)
  await page.goto('/instances/1?tab=metrics')

  // 探针状态卡（FR-068/114 复用，FR-142 指引）
  await expect(page.getByText('ServerProbe 探针更新')).toBeVisible()

  // 当前健康：TPS/MSPT/CPU 三枚阈值色条 pill
  await expect(page.getByText('当前健康', { exact: true })).toBeVisible()
  await expect(page.locator('[data-health-level]')).toHaveCount(3)

  // 历史曲线阈值标记（RangePicker 默认 24h）——阈值线在 SVG tspan 与图例 span 各出现一次，取首个即可
  await expect(page.getByText('TPS 警戒 18').first()).toBeVisible()
  await expect(page.getByText('MSPT 危险 75ms').first()).toBeVisible()
  await expect(page.getByText('CPU 警戒 75%').first()).toBeVisible()

  await page.screenshot({ ...SHOT('single-machine-running.png'), fullPage: true })
})

test('FR-142 停机态：当前指标折叠不占大块空白', async ({ page }) => {
  await login(page)
  await page.goto('/instances/2?tab=metrics')

  await expect(page.getByText('当前健康', { exact: true })).toBeVisible()
  await expect(
    page.getByText('实例未运行，当前指标已折叠；可继续查看下方历史曲线。'),
  ).toBeVisible()

  await page.screenshot({ ...SHOT('single-machine-stopped.png'), fullPage: true })
})
