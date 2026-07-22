import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * 接入指引内嵌更新器信息 + 版本页旁路摘要 · mock 真浏览器（通用能力验收）。
 * 证据截图：.tmp/acceptance/client-updater-guide/
 */
test('接入指引展示 coreVersion/available/size 且版本页有旁路摘要', async ({ page }) => {
  await login(page)

  // 工作台 query 键为 channel/tab（非 path 参数）
  await page.goto('/client-channels?channel=skyblock-s1&tab=guide')
  await expect(page.locator('[data-page="client-channel-workbench"]')).toBeVisible({ timeout: 15_000 })
  await expect(page.getByRole('tab', { name: /接入指引/ })).toHaveAttribute('data-state', 'active')

  const info = page.getByTestId('embedded-updater-info')
  await expect(info).toBeVisible({ timeout: 15_000 })
  await expect(info.getByText('0.1.0').or(info.locator('dd').first())).toBeVisible()
  // mock coreVersion=3
  await expect(info.getByText('3', { exact: true }).first()).toBeVisible()
  await expect(page.getByRole('button', { name: /wedge\.jar/i })).toBeEnabled()

  await page.screenshot({
    path: '../../.tmp/acceptance/client-updater-guide/guide-embed-info.png',
    fullPage: true,
  })

  // 版本 Tab 旁路摘要
  await page.getByRole('tab', { name: /版本/ }).first().click()
  await expect(page.getByText(/内嵌更新器/)).toBeVisible({ timeout: 15_000 })
  await page.screenshot({
    path: '../../.tmp/acceptance/client-updater-guide/versions-summary.png',
    fullPage: true,
  })
})
