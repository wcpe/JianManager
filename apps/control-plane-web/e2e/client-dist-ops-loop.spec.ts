import { test, expect } from '@playwright/test'
import { login } from './helpers'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

/**
 * 客户端分发运营闭环 · mock 真浏览器冒烟（FR-430 两页化后）。
 * 旧 `/client-dist-monitor`、`/client-dist-security` 重定向到 `/client-dist-ops`；
 * 深链与「打开安全中心」均指向运维页 query，不再有独立 security/monitor data-page。
 * 证据截图：.tmp/acceptance/client-dist-ops-loop/
 */
const evidenceDir = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  '../../../.tmp/acceptance/client-dist-ops-loop',
)

test.describe('客户端分发运营闭环', () => {
  test('运维统计 Tab：请求侧 KPI + 监控错误码 + 导出入口', async ({ page }) => {
    await login(page)
    await page.goto('/client-dist-ops?tab=statistics')
    await expect(page.locator('[data-page="client-dist-ops"]')).toBeVisible()
    await expect(page.getByRole('heading', { name: '客户端分发运维' })).toBeVisible()

    await expect(page.getByRole('tab', { name: '统计' })).toBeVisible()
    await expect(page.getByText('请求成功率').first()).toBeVisible({ timeout: 15_000 })
    await expect(page.locator('[data-kpi-scope="client-dist-monitor-statistics"]')).toBeVisible()
    await expect(page.locator('[data-kpi-scope="client-dist-monitor-statistics"]').getByText('更新成功率')).toHaveCount(0)

    await page.screenshot({ path: path.join(evidenceDir, '01-ops-statistics.png'), fullPage: true })

    await page.getByRole('tab', { name: /实时监控/ }).click()
    await expect(page.getByText('错误码 Top 10').first()).toBeVisible({ timeout: 15_000 })
    await page.screenshot({ path: path.join(evidenceDir, '02-ops-monitor-errors.png'), fullPage: true })

    await expect(page.getByRole('button', { name: '导出 CSV' }).first()).toBeVisible({ timeout: 15_000 })
  })

  test('运维日志/处置：标题与处置入口（旧 security 重定向）', async ({ page }) => {
    await login(page)
    await page.goto('/client-dist-security?channelId=skyblock-s1&tab=events')
    await expect(page).toHaveURL(/\/client-dist-ops/)
    await expect(page.locator('[data-page="client-dist-ops"]')).toBeVisible()
    await expect(page.getByRole('heading', { name: '客户端分发运维' })).toBeVisible()

    // 旧 events → 运维「实时监控/异常」分档；处置动作在「处置」Tab
    await page.getByRole('tab', { name: '处置' }).click()
    await expect(page.getByRole('button', { name: '封禁 IP' }).first()).toBeVisible({ timeout: 15_000 })
    await expect(page.getByRole('button', { name: '改 key 态' }).first()).toBeVisible()
    await expect(page.getByRole('button', { name: '频道防护' }).first()).toBeVisible()

    const banBtn = page.getByRole('button', { name: '封禁 IP' }).first()
    await banBtn.click()
    await expect(page.getByRole('dialog')).toBeVisible()
    await expect(page.getByText(/临时封禁 IP/)).toBeVisible()
    await page.keyboard.press('Escape')

    await page.screenshot({ path: path.join(evidenceDir, '03-ops-actions.png'), fullPage: true })
  })

  test('频道工作台：统计 KPI 更新成功率 + 安全摘要深链到运维页', async ({ page }) => {
    await login(page)
    await page.goto('/client-channels?channel=skyblock-s1&tab=stats')
    await expect(page.locator('[data-page="client-channel-workbench"]')).toBeVisible({ timeout: 15_000 })

    const summary = page.getByTestId('channel-security-summary')
    await expect(summary).toBeVisible({ timeout: 15_000 })
    await expect(summary.getByText('安全摘要')).toBeVisible()
    await expect(summary.getByRole('link', { name: '打开安全中心' })).toHaveAttribute(
      'href',
      /\/client-dist-ops\?channelId=skyblock-s1/,
    )

    await expect(page.getByText('更新成功率').first()).toBeVisible({ timeout: 15_000 })
    await expect(page.locator('[data-kpi-scope="client-stats-panel"]')).toBeVisible()

    await page.screenshot({ path: path.join(evidenceDir, '04-channel-stats-security.png'), fullPage: true })
  })

  test('跨页 query：旧监控/安全深链均落到运维页并保留 channelId', async ({ page }) => {
    await login(page)
    // 现网页头频道选择器是 Select（显示频道名），logs?type=request 无 channelId 文本框；
    // 断言 URL 透传 + 选择器选中 mock 频道 skyblock-s1 →「空岛一区」。
    const assertOpsLanding = async () => {
      await expect(page).toHaveURL(/\/client-dist-ops/)
      await expect(page).toHaveURL(/channelId=skyblock-s1/)
      await expect(page).toHaveURL(/errCode=INVALID_CLIENT_KEY/)
      await expect(page.locator('[data-page="client-dist-ops"]')).toBeVisible()
      await expect(page.getByRole('heading', { name: '客户端分发运维' })).toBeVisible()
      await expect(page.getByRole('combobox').filter({ hasText: '空岛一区' }).first()).toBeVisible({ timeout: 10_000 })
    }

    await page.goto('/client-dist-monitor?channelId=skyblock-s1&errCode=INVALID_CLIENT_KEY&tab=logs')
    await assertOpsLanding()
    await expect(page).toHaveURL(/type=request/)

    await page.goto('/client-dist-security?channelId=skyblock-s1&errCode=INVALID_CLIENT_KEY&tab=logs')
    await assertOpsLanding()
    await page.screenshot({ path: path.join(evidenceDir, '05-cross-page-query.png'), fullPage: true })
  })
})
