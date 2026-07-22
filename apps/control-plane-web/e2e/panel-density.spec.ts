import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-061 面板信息密度与视觉改造 · 单机（Playwright + mock 模式）验收。
 * 覆盖 baota 式高密度运维面板契约：仪表盘（/）顶部环形仪表盘（ResourceGauge CPU/负载/内存）+
 * 分区面板 Panel + 聚合历史曲线 + 底部密集实例表（DataTable dense + 状态徽章）。
 * 断言这些高密度组件在真 UI 渲染。证据落 .tmp/acceptance/FR-061/。关联 ADR-009（控制台 Shell）。
 */

test('FR-061 高密度面板：环形仪表盘 + 分区面板 + 密集实例表', async ({ page }) => {
  await login(page)
  // 登录后即落仪表盘（data-page=overview）。
  await expect(page.locator('[data-page="overview"]')).toBeVisible()

  // 环形仪表盘（ResourceGauge）：CPU/负载/内存三只，按阈值变色。
  // 用 .first()：同名文案亦出现在 recharts tooltip item（悬浮项），避免 strict 命中两处。
  await expect(page.getByText('总 CPU').first()).toBeVisible()
  await expect(page.getByText('总负载').first()).toBeVisible()
  await expect(page.getByText('总内存').first()).toBeVisible()
  // 仪表盘为 SVG 环（recharts / 自绘）——至少一个 svg 在仪表区渲染。
  await expect(page.locator('[data-page="overview"] svg').first()).toBeVisible()

  // 分区面板 Panel（标题栏 + 内容区）：聚合历史曲线区四块标题。
  await expect(page.getByText('CPU 趋势')).toBeVisible()
  await expect(page.getByText('负载趋势').first()).toBeVisible()
  await expect(page.getByText('内存趋势')).toBeVisible()

  // 底部密集实例表（虚拟滚动容器 + dense 行）。
  const denseTable = page.locator('[data-testid="overview-instances-virtual"]')
  await expect(denseTable).toBeVisible()
  await expect(page.getByText('实例列表')).toBeVisible()

  // 时间区间选择器（RangePicker，FR-061 契约组件，供仪表盘/监控复用）。
  await expect(page.getByRole('tab', { name: '24h' }).first()).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptance/FR-061/single-machine-panel-density.png', fullPage: true })
})
