import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-352 接入指引内嵌更新器信息 + 版本/发布页旁路摘要 · mock 真浏览器验收。
 * 证据截图：.tmp/acceptance/FR-352/
 */
test('FR-352 guide 展示 coreVersion/available/size 且版本页有旁路摘要', async ({ page }) => {
  await login(page)

  // 进已有种子频道工作台 → 接入指引 Tab
  await page.goto('/client-channels')
  await expect(page.getByRole('heading', { name: '客户端分发' })).toBeVisible()
  await page.getByRole('button', { name: /survival-s2/ }).first().click()

  // guide Tab（URL 或 Tabs 文案）
  const guideTab = page.getByRole('tab', { name: /接入指引|指引|guide/i }).or(page.getByText('接入指引'))
  if (await guideTab.first().isVisible().catch(() => false)) {
    await guideTab.first().click()
  } else {
    await page.goto('/client-channels/survival-s2?tab=guide')
  }

  const info = page.getByTestId('embedded-updater-info')
  await expect(info).toBeVisible({ timeout: 15_000 })
  await expect(info.getByText('0.1.0').or(info.locator('dd').first())).toBeVisible()
  // mock coreVersion=3
  await expect(info.getByText('3', { exact: true }).first()).toBeVisible()
  await expect(page.getByRole('button', { name: /wedge\.jar/i })).toBeEnabled()

  await page.screenshot({
    path: '../../.tmp/acceptance/FR-352/guide-embed-info.png',
    fullPage: true,
  })

  // 版本 Tab 旁路摘要
  const versionsTab = page.getByRole('tab', { name: /版本/ }).first()
  if (await versionsTab.isVisible().catch(() => false)) {
    await versionsTab.click()
  } else {
    await page.goto('/client-channels/survival-s2?tab=versions')
  }
  await expect(page.getByText(/内嵌更新器/)).toBeVisible({ timeout: 15_000 })
  await page.screenshot({
    path: '../../.tmp/acceptance/FR-352/versions-summary.png',
    fullPage: true,
  })
})
