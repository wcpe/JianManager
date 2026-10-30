import { mkdirSync } from 'node:fs'
import path from 'node:path'
import { test, expect } from '@playwright/test'
import { login } from './helpers'

const artifactsDir = process.cwd().endsWith(`${path.sep}web`)
  ? path.resolve(process.cwd(), '..', '.tmp')
  : path.resolve(process.cwd(), '.tmp')

/** 真浏览器纵切：侧栏资源面板入口、实例内 Bot 分区与批量操作。 */
test.describe('控制台实例内 Bot 管理段（mock 模式真浏览器）', () => {
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

  test('侧栏资源面板搜索 → 实例内 Bot 分区 → 批量设行为', async ({ page }) => {
    // 【为何不再走「选择服务器」弹窗】侧栏重构删除了 `ServerSelector`（服务选择器按钮）与
    // 它的虚拟列表（`server-selector-virtual`），资源定位改为资源面板内的搜索框。
    const sidebar = page.locator('[data-slot="console-sidebar"]')
    await sidebar.getByRole('textbox', { name: '搜索服务器' }).fill('survival-1')
    const hit = sidebar.getByText('survival-1').first()
    await expect(hit).toBeVisible({ timeout: 10_000 })
    await page.screenshot({ path: path.join(artifactsDir, 'fr039-e2e-server-selector-entry.png'), fullPage: true })

    await hit.click()
    await expect(page).toHaveURL(/\/instances\/1$/)
    // 顶栏标题只保留实例名，无「服务器控制台 /」前缀
    await expect(page.getByRole('heading', { name: 'survival-1', exact: true })).toBeVisible()
    await expect(page.locator('[data-page="instance-console"]')).toBeVisible()

    // 实例分区为 role=tab（tablist「实例页签」），不再是 button
    await page.getByRole('tab', { name: 'Bot', exact: true }).click()
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
