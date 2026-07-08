import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-062 节点负载（load average）采集与仪表盘 · 单机（Playwright + mock 模式）验收。
 * 覆盖：仪表盘（/）总负载 ResourceGauge（load÷核 倍数，单位「×」，FR-108 不破环）+ 负载趋势曲线
 * （node_load 时序）；监控页节点级「负载」图（node_load 单序列）。
 * 断言负载专属仪表盘/曲线在真 UI 渲染。证据落 .tmp/acceptance/FR-062/。
 * 负载采集链路（心跳 LoadAvg1 → node_load 时序、overview LoadAvg=load1/核×100）由 Go 单测覆盖。
 */

test('FR-062 仪表盘总负载环 + 负载趋势曲线', async ({ page }) => {
  await login(page)
  await expect(page.locator('[data-page="overview"]')).toBeVisible()

  // 总负载环形仪表盘：标签「总负载」+ 倍数单位「×」（区别于 CPU/内存的 %）。
  const loadGauge = page.getByText('总负载').first()
  await expect(loadGauge).toBeVisible()
  // 环内值以「×」倍数呈现（load÷核，FR-108 封顶 1.0=满核，不破环）。
  await expect(page.getByText(/×/).first()).toBeVisible()

  // 负载趋势曲线面板（node_load 时序落图）。
  await expect(page.getByText('负载趋势').first()).toBeVisible()
  // 曲线真渲染（recharts 折线）。
  await expect
    .poll(async () => page.locator('path.recharts-line-curve').count(), { timeout: 15_000 })
    .toBeGreaterThan(0)

  await page.screenshot({ path: '../.tmp/acceptance/FR-062/single-machine-load-dashboard.png', fullPage: true })
})

test('FR-062 监控页节点级负载图（node_load 单序列）', async ({ page }) => {
  await login(page)
  await page.goto('/monitor')
  await expect(page.getByRole('heading', { name: '监控' })).toBeVisible()

  // 平台层已有「负载」图（PLATFORM_CHART_DEFS 含 load，node_load 均值）——已挂载渲染。
  await expect(page.getByText('负载', { exact: true }).first()).toBeAttached()

  // 下钻到节点 → 节点级 defs 含独立「负载」图（monitor.chart.load，node_load 单序列）。
  const drill = page.getByRole('combobox', { name: '下钻到实例' }).first()
  await expect(drill).toBeVisible()
  const nodeOption = drill.locator('option').filter({ hasNotText: /平台|节点…|全部/ }).first()
  await drill.selectOption((await nodeOption.getAttribute('value')) ?? { index: 1 })

  // 节点视图仍有「负载」图（monitor.chart.load，node_load 单序列）+ 曲线真渲染。
  const nodeLoadTitle = page.getByText('负载', { exact: true }).first()
  await expect(nodeLoadTitle).toBeAttached()
  await nodeLoadTitle.scrollIntoViewIfNeeded()
  await expect
    .poll(async () => page.locator('path.recharts-line-curve').count(), { timeout: 15_000 })
    .toBeGreaterThan(0)

  await page.screenshot({ path: '../.tmp/acceptance/FR-062/single-machine-node-load-chart.png', fullPage: true })
})
