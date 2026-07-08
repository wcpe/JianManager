import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-060 时序监控与历史曲线 · 单机（Playwright + mock 模式）验收。
 * 覆盖：监控页平台级历史曲线网格（CPU/负载/内存/在线玩家 4 图，node_load/node_cpu_pct/... 时序）
 * 经 /metrics/overview 落图；时间范围选择器（RangePicker 1h~90d）供历史回看；下钻到节点后
 * 经 /metrics/series 出节点级 6 图。断言曲线容器（recharts SVG）真渲染，而非空态。
 * 证据落 .tmp/acceptance/FR-060/。关联 ADR-013（分级降采样）、FR-061（RangePicker/TimeSeriesChart 依赖）。
 */

test('FR-060 平台历史曲线网格 + 时间范围 + 下钻节点级序列', async ({ page }) => {
  await login(page)
  await page.goto('/monitor')

  await expect(page.getByRole('heading', { name: '监控' })).toBeVisible()

  // 平台主图网格 4 图标题（PLATFORM_CHART_DEFS = cpu/load/memory/players）均已挂载渲染。
  // 网格图卡在响应式栅格内可能超出首屏视口，故断言「已渲染进 DOM」（count>0）而非首屏可见；
  // 「真渲染出曲线」由下方 recharts SVG 可见断言证明。
  await expect(page.getByText('负载', { exact: true }).first()).toBeAttached()
  await expect(page.getByText('CPU').first()).toBeVisible()
  await expect(page.getByText('内存').first()).toBeVisible()
  await expect(page.getByText('在线玩家').first()).toBeVisible()

  // 时间范围选择器（历史回看窗口，供每图 + 页级复用）。
  await expect(page.getByRole('tab', { name: '24h' }).first()).toBeVisible()
  await expect(page.getByRole('tab', { name: '7d' }).first()).toBeVisible()
  await expect(page.getByRole('tab', { name: '30d' }).first()).toBeVisible()

  // 历史曲线真渲染：recharts 折线 SVG（path.recharts-line-curve）出现，证明 /metrics/overview
  // 时序数据落图，非空态。等待数据到达后曲线出现。
  await expect(page.locator('svg.recharts-surface').first()).toBeVisible()
  await expect
    .poll(async () => page.locator('path.recharts-line-curve, path.recharts-area-curve').count(), {
      timeout: 15_000,
    })
    .toBeGreaterThan(0)

  // 下钻到节点 → 触发 /metrics/series（节点级 6 图：资源/负载/CPU/内存/磁盘/网络）。
  const drill = page.getByRole('combobox', { name: '下钻到实例' }).first()
  await expect(drill).toBeVisible()
  const seriesResp = page.waitForResponse(
    (r) => r.url().includes('/metrics/series') && r.request().method() === 'GET',
    { timeout: 15_000 },
  )
  // 选中第一个真实节点选项（跳过占位「平台/节点…」项）。
  const nodeOption = drill.locator('option').filter({ hasNotText: /平台|节点…|全部/ }).first()
  await drill.selectOption(await nodeOption.getAttribute('value') ?? { index: 1 })
  await seriesResp
  // 节点视图独有的磁盘/网络图标题渲染进网格，证明切到节点级 defs（6 图，末图在折叠下方，
  // 断言已挂载而非首屏可见）。
  await expect(page.getByText('磁盘').first()).toBeVisible()
  await expect(page.getByText('网络 IO').first()).toBeAttached()

  await page.screenshot({ path: '../.tmp/acceptance/FR-060/single-machine-timeseries-curves.png', fullPage: true })
})
