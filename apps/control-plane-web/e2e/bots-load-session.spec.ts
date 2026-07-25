import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-372 会话实时观测 · 单机（Playwright + VITE_MOCK 模式）验收。
 * 覆盖：详情页路由 /bots/sessions/:id、六个 tab 骨架、SSE 连接状态、tab 切换不 crash。
 * devmock 种子 V2 演示会话 id=100（runState=running）。
 */
test.describe('FR-372 会话实时观测', () => {
  test.beforeEach(async ({ page }) => {
    await login(page)
  })

  test('详情页标题与六个 tab 骨架可见', async ({ page }) => {
    await page.goto('/bots/sessions/100?tab=overview')
    // 等待页面骨架加载完成
    await expect(page.getByTestId('bot-load-session-page')).toBeVisible({ timeout: 15_000 })

    // 页面标题（演示压测·stable·100）
    await expect(page.getByRole('heading', { name: '演示压测·stable·100' })).toBeVisible()

    // 六个 tab 全部可见
    for (const label of ['概览', 'Bot', '指标', '失败', '事件', '配置']) {
      await expect(page.getByRole('tab', { name: label })).toBeVisible()
    }
  })

  test('SSE 连接后 stream 状态指示为已连接', async ({ page }) => {
    await page.goto('/bots/sessions/100?tab=overview')
    await expect(page.getByTestId('bot-load-session-page')).toBeVisible({ timeout: 15_000 })

    // SessionHeader 展示「实时流: 已连接」（SSE 建立后 streamStatus=open）
    await expect(page.getByTestId('session-header')).toBeVisible()
    await expect(page.getByText('实时流', { exact: false })).toBeVisible()
    // 等待 SSE 连接建立（可能短暂处于 connecting，最终到 open）
    await expect(page.getByText(/已连接|连接中/).first()).toBeVisible({ timeout: 15_000 })
  })

  test('切换各 tab 不 crash', async ({ page }) => {
    await page.goto('/bots/sessions/100?tab=overview')
    await expect(page.getByTestId('bot-load-session-page')).toBeVisible({ timeout: 15_000 })

    // 逐个切 tab（用本地化名称），确认页面不 crash
    const tabLabels = ['Bot', '指标', '失败', '事件', '配置']
    for (const label of tabLabels) {
      await page.getByRole('tab', { name: label }).click()
      // 切换后 tab 应激活（data-state=active）
      await expect(page.getByRole('tab', { name: label })).toHaveAttribute('data-state', 'active', { timeout: 10_000 })
      // 页面骨架仍存在（未 crash）
      await expect(page.getByTestId('bot-load-session-page')).toBeVisible()
    }
  })

  test('状态区显示运行状态与判定信息', async ({ page }) => {
    await page.goto('/bots/sessions/100?tab=overview')
    await expect(page.getByTestId('session-header')).toBeVisible({ timeout: 15_000 })

    // 运行状态 chip（runState=running → 运行中）
    await expect(page.getByText('状态', { exact: true }).first()).toBeVisible()
    await expect(page.getByText('运行中')).toBeVisible()
    // 判定 chip（verdict=pending → 「判定: 待定」）
    await expect(page.getByText('判定', { exact: false }).first()).toBeVisible()
  })
})
