import { mkdirSync } from 'node:fs'
import path from 'node:path'
import { test, expect, type Locator, type Page } from '@playwright/test'
import { login } from './helpers'

const artifactsDir = process.cwd().endsWith(`${path.sep}web`)
  ? path.resolve(process.cwd(), '..', '.tmp')
  : path.resolve(process.cwd(), '.tmp')

function botGroupCard(page: Page, name: string): Locator {
  return page.locator('div.bg-card').filter({ has: page.getByRole('button', { name, exact: true }) }).first()
}

/** FR-038 真浏览器纵切：Bot 聚合摘要、分页窥视与批量 API。 */
test.describe('FR-038 Bot 规模化后端 API（mock 模式真浏览器）', () => {
  test.beforeEach(async ({ page }) => {
    mkdirSync(artifactsDir, { recursive: true })
    await login(page)
  })

  test('Bot 规模化 API：摘要聚合 → 分页窥视 → 批量设行为', async ({ page }) => {
    await page.goto('/bots')
    await expect(page.getByRole('heading', { name: 'Bot 管理' })).toBeVisible()
    await expect(page.getByText('2 实例 · 2 节点')).toBeVisible()
    await expect(page.getByText('舰队健康')).toBeVisible()
    await expect(page.getByRole('button', { name: '生存服', exact: true })).toBeVisible()
    await expect(page.getByRole('button', { name: '空岛服', exact: true })).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr038-e2e-bots-overview.png'), fullPage: true })

    const survival = botGroupCard(page, '生存服')
    await survival.getByRole('button', { name: '生存服', exact: true }).click()
    await expect(survival.getByText('GuardBot')).toBeVisible()
    await expect(survival.getByText('FollowBot')).toBeVisible()
    await expect(survival.getByText('共 2 个 Bot')).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr038-e2e-group-peek.png'), fullPage: true })

    await survival.getByRole('combobox').click()
    await page.getByText('设为跟随', { exact: true }).click()
    await expect(page.getByText('成功 2 · 失败 0')).toBeVisible()
    await expect(survival.getByText('GuardBot')).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr038-e2e-batch-result.png'), fullPage: true })
  })
})
