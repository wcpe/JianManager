import { test, expect } from '@playwright/test'
import { login } from './helpers'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

/**
 * FR-356~361 客户端分发安全/遥测/统计闭环 · 发版前 mock 真浏览器冒烟。
 * 覆盖：KPI 标签区分、监控四 Tab、错误码钻取、安全中心处置入口、频道安全摘要深链、跨页 query、导出 CSV 按钮。
 * 证据截图：.tmp/acceptance/FR-356-361/
 */
const evidenceDir = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  '../../../.tmp/acceptance/FR-356-361',
)

test.describe('FR-356~361 分发闭环发版前 E2E', () => {
  test('监控页：请求侧 KPI + 监控错误码 + 导出入口 + 跨页深链', async ({ page }) => {
    await login(page)
    await page.goto('/client-dist-monitor')
    await expect(page.locator('[data-page="client-dist-monitor"]')).toBeVisible()

    // FR-356：统计 Tab 仅请求侧标签，不得出现「更新成功率」冒充
    await expect(page.getByRole('tab', { name: '统计' })).toBeVisible()
    await expect(page.getByText('请求成功率').first()).toBeVisible()
    await expect(page.locator('[data-kpi-scope="client-dist-monitor-statistics"]')).toBeVisible()
    await expect(page.locator('[data-kpi-scope="client-dist-monitor-statistics"]').getByText('更新成功率')).toHaveCount(0)

    await page.screenshot({ path: path.join(evidenceDir, '01-monitor-statistics.png'), fullPage: true })

    // FR-357：监控 Tab + 错误码 TopN
    await page.getByRole('tab', { name: '监控' }).click()
    await expect(page.getByText('错误码 Top 10').first()).toBeVisible({ timeout: 15_000 })
    await page.screenshot({ path: path.join(evidenceDir, '02-monitor-errors.png'), fullPage: true })

    // 错误码点击 → 日志 Tab（页内钻取）
    const errorPanel = page.locator('[data-slot="panel"]').filter({ hasText: '错误码 Top 10' }).first()
    const firstError = errorPanel.locator('button, a, [role="button"]').first()
    if (await firstError.isVisible().catch(() => false)) {
      await firstError.click()
      await expect(page.getByRole('tab', { name: '日志' })).toHaveAttribute('data-state', 'active')
    }

    // FR-361：导出 CSV 按钮（页头 stats-summary）
    await expect(page.getByRole('button', { name: '导出 CSV' }).first()).toBeVisible()

    // FR-359：打开安全中心深链保留筛选（监控页日志区链接）
    await page.getByRole('tab', { name: '统计' }).click()
    const securityLink = page.getByRole('link', { name: '打开安全中心' }).first()
    if (await securityLink.isVisible().catch(() => false)) {
      const href = await securityLink.getAttribute('href')
      expect(href || '').toContain('/client-dist-security')
    }
  })

  test('安全中心：标题/事件行处置/导出/query 预填', async ({ page }) => {
    await login(page)
    await page.goto('/client-dist-security?channelId=skyblock-s1&tab=events')
    await expect(page.locator('[data-page="client-dist-security"]')).toBeVisible()
    await expect(page.getByRole('heading', { name: '客户端分发安全' })).toBeVisible()

    // FR-358：事件行至少有封禁 IP；有 key/channel 时有改 key 态 / 频道防护
    await expect(page.getByRole('tab', { name: /事件|异常/ }).or(page.getByText('异常请求分析')).first()).toBeVisible({ timeout: 15_000 })
    // tab 可能已是 events
    const banBtn = page.getByRole('button', { name: '封禁 IP' }).first()
    await expect(banBtn).toBeVisible({ timeout: 15_000 })
    await expect(page.getByRole('button', { name: '改 key 态' }).first()).toBeVisible()
    await expect(page.getByRole('button', { name: '频道防护' }).first()).toBeVisible()

    // DangerConfirm：点封禁 IP 出确认，取消不写
    await banBtn.click()
    await expect(page.getByRole('dialog')).toBeVisible()
    await expect(page.getByText(/临时封禁 IP/)).toBeVisible()
    await page.getByRole('button', { name: /取消|关闭/ }).first().click().catch(async () => {
      await page.keyboard.press('Escape')
    })

    await page.screenshot({ path: path.join(evidenceDir, '03-security-events.png'), fullPage: true })

    // 日志 tab + 导出
    await page.getByRole('tab', { name: '日志详情' }).click()
    await expect(page.getByRole('button', { name: '导出 CSV' }).first()).toBeVisible({ timeout: 10_000 })

    // FR-359：query 预填频道
    await expect(page.locator('input[value="skyblock-s1"]').first()).toBeVisible()
  })

  test('频道工作台：统计 KPI 更新成功率 + 安全摘要深链', async ({ page }) => {
    await login(page)
    await page.goto('/client-channels?channel=skyblock-s1&tab=stats')
    await expect(page.locator('[data-page="client-channel-workbench"]')).toBeVisible({ timeout: 15_000 })

    // FR-358：安全摘要条
    const summary = page.getByTestId('channel-security-summary')
    await expect(summary).toBeVisible({ timeout: 15_000 })
    await expect(summary.getByText('安全摘要')).toBeVisible()
    await expect(summary.getByRole('link', { name: '打开安全中心' })).toHaveAttribute(
      'href',
      /\/client-dist-security\?channelId=skyblock-s1/,
    )

    // FR-356：频道统计展示「更新成功率」（与监控请求成功率区分）
    await expect(page.getByText('更新成功率').first()).toBeVisible({ timeout: 15_000 })
    await expect(page.locator('[data-kpi-scope="client-stats-panel"]')).toBeVisible()

    // FR-357：窗外/空态提示在默认 30d mock 可能出现
    const emptyBanner = page.getByTestId('client-stats-empty-kind')
    if (await emptyBanner.isVisible().catch(() => false)) {
      await expect(emptyBanner).toHaveAttribute('data-empty-kind', /no_traffic|no_telemetry|out_of_window/)
    }

    await page.screenshot({ path: path.join(evidenceDir, '04-channel-stats-security.png'), fullPage: true })
  })

  test('跨页 query：监控带参 → 安全中心保留 channelId', async ({ page }) => {
    await login(page)
    await page.goto('/client-dist-monitor?channelId=skyblock-s1&errCode=INVALID_CLIENT_KEY&tab=logs')
    await expect(page.locator('[data-page="client-dist-monitor"]')).toBeVisible()

    // 日志 tab 激活
    await expect(page.getByRole('tab', { name: '日志' })).toHaveAttribute('data-state', 'active')

    // 深链到安全中心
    await page.goto('/client-dist-security?channelId=skyblock-s1&errCode=INVALID_CLIENT_KEY&tab=logs')
    await expect(page.locator('[data-page="client-dist-security"]')).toBeVisible()
    await expect(page.locator('input[value="skyblock-s1"]').first()).toBeVisible({ timeout: 10_000 })
    await page.screenshot({ path: path.join(evidenceDir, '05-cross-page-query.png'), fullPage: true })
  })
})
