import { mkdirSync } from 'node:fs'
import path from 'node:path'
import { test, expect } from '@playwright/test'
import { login } from './helpers'

const artifactsDir = process.cwd().endsWith(`${path.sep}web`)
  ? path.resolve(process.cwd(), '..', '.tmp')
  : path.resolve(process.cwd(), '.tmp')

/** FR-039 真浏览器纵切：服务器选择器入口、实例内 Bot 分区与批量操作。 */
test.describe('FR-039 控制台实例内 Bot 管理段（mock 模式真浏览器）', () => {
  test.beforeEach(async ({ page }) => {
    mkdirSync(artifactsDir, { recursive: true })
    await page.addInitScript(() => {
      localStorage.setItem('sidebar.collapsed', '0')
      localStorage.removeItem('sidebar.collapsedGroups')
      localStorage.removeItem('sidebar.selectedNodeId')
      localStorage.removeItem('server-selector.favorites')
      localStorage.removeItem('server-selector.recent')
    })
    await login(page)
  })

  test('服务器选择器入口 → Bot 分区 → 批量设行为', async ({ page }) => {
    await page.getByRole('button', { name: '选择服务器' }).click()
    await expect(page.getByRole('dialog', { name: '服务器选择器' })).toBeVisible()
    await page.getByRole('searchbox', { name: '搜索服务器' }).fill('survival-1')
    const selector = page.getByTestId('server-selector-virtual')
    await expect(selector).toBeVisible()
    await expect.poll(async () => Number(await selector.getAttribute('data-total-count'))).toBeGreaterThanOrEqual(1)
    await expect(page.getByRole('button', { name: /survival-1.*RUNNING/ })).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr039-e2e-server-selector-entry.png'), fullPage: true })

    await page.getByRole('button', { name: /survival-1.*RUNNING/ }).click()
    await expect(page).toHaveURL(/\/instances\/1$/)
    await expect(page.getByRole('heading', { name: /服务器控制台 \/ survival-1/ })).toBeVisible()

    await page.getByRole('button', { name: 'Bot' }).click()
    await expect(page.getByText('当前筛选 2 个 Bot')).toBeVisible()
    await expect(page.getByText('GuardBot')).toBeVisible()
    await expect(page.getByText('FollowBot')).toBeVisible()
    await expect(page.getByText('共 2 个')).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr039-e2e-bot-segment.png'), fullPage: true })

    await page.getByRole('checkbox', { name: 'GuardBot' }).click()
    await expect(page.getByText('已选 1 个 Bot')).toBeVisible()
    const batchBar = page.getByText('已选 1 个 Bot').locator('xpath=ancestor::div[contains(@class,"rounded-lg")][1]')
    await batchBar.getByRole('combobox').click()
    await page.getByRole('option', { name: '巡逻' }).click()
    await batchBar.getByRole('button', { name: '批量设行为' }).click()
    await expect(page.getByText('成功 1 · 失败 0')).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr039-e2e-batch-result.png'), fullPage: true })
  })
})
