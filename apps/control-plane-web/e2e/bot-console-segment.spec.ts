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
    /**
     * 等**目标行本身**，不等 `data-total-count`（2026-10-20 消除 flaky）。
     *
     * `data-total-count` 取值是 `aggregate?.total ?? searchResult?.total`，而列表行
     * `rows` 只来自 **searchResult**——两者是 `/instances/aggregate` 与 `/instances`
     * 两个独立请求，完成顺序不确定。原用例先等计数 ≥1，此时可能 aggregate 已回、
     * searchResult 未回 ⇒ `rows` 仍为空、目标行不存在，随后的点击/标题断言就偶发失败。
     * 直接等这一行可见，才是「列表里真有这个实例」的充要信号。
     */
    const survivalRow = selector.getByRole('button', { name: /survival-1.*RUNNING/ })
    await expect(survivalRow).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr039-e2e-server-selector-entry.png'), fullPage: true })

    await survivalRow.click()
    await expect(page).toHaveURL(/\/instances\/1$/)
    // FR-412：顶栏标题只保留实例名，无「服务器控制台 /」前缀
    await expect(page.getByRole('heading', { name: 'survival-1', exact: true })).toBeVisible()
    await expect(page.locator('[data-page="instance-console"]')).toBeVisible()

    // FR-413：实例分区为 role=tab（tablist「实例页签」），不再是 button
    await page.getByRole('tab', { name: 'Bot', exact: true }).click()
    await expect(page.getByText('当前筛选 2 个 Bot')).toBeVisible()
    await expect(page.getByText('GuardBot')).toBeVisible()
    await expect(page.getByText('FollowBot')).toBeVisible()
    await expect(page.getByText('共 2 个')).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr039-e2e-bot-segment.png'), fullPage: true })

    await page.getByRole('checkbox', { name: 'GuardBot' }).click()
    /**
     * 批量栏用稳定 testid 定位（2026-10-20 消除 flaky）。
     *
     * 原写法 `getByText('已选 1 个 Bot').locator('xpath=ancestor::div[contains(@class,"rounded-lg")][1]')`
     * 靠 CSS 类名反推容器：`rounded-lg` 在本页大量出现，且分组块表头也有「批量设行为」
     * 按钮，层级猜错时会命中错的容器 ⇒ 后续 combobox/按钮操作偶发失败。
     */
    const batchBar = page.getByTestId('bot-batch-bar')
    await expect(batchBar.getByTestId('bot-batch-scope')).toHaveText('已选 1 个 Bot')
    await batchBar.getByRole('combobox').click()
    await page.getByRole('option', { name: '巡逻' }).click()
    await batchBar.getByRole('button', { name: '批量设行为' }).click()
    await expect(page.getByText('成功 1 · 失败 0')).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr039-e2e-batch-result.png'), fullPage: true })
  })
})
