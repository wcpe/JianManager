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

/** FR-040 真浏览器纵切：全局 Bot 聚合总览、分组切换、窥视、批量与控制台联动。 */
test.describe('FR-040 全局 Bot 管理页重构（mock 模式真浏览器）', () => {
  test.beforeEach(async ({ page }) => {
    mkdirSync(artifactsDir, { recursive: true })
    await page.addInitScript(() => {
      localStorage.setItem('sidebar.collapsed', '0')
      localStorage.removeItem('sidebar.collapsedGroups')
      localStorage.removeItem('sidebar.selectedNodeId')
    })
    await login(page)
  })

  test('聚合总览 → 控制台联动 → 分页窥视 → 分组批量', async ({ page }) => {
    await page.goto('/bots')
    await expect(page.getByRole('heading', { name: 'Bot 管理' })).toBeVisible()
    await expect(page.getByText('2 实例 · 2 节点')).toBeVisible()
    await expect(page.getByText('舰队健康')).toBeVisible()
    await expect(page.getByRole('button', { name: '压测' })).toBeVisible()
    await expect(page.getByRole('button', { name: '生存服', exact: true })).toBeVisible()
    await expect(page.getByRole('button', { name: '空岛服', exact: true })).toBeVisible()
    await expect(page.getByText('GuardBot')).toHaveCount(0)
    await page.screenshot({ path: path.join(artifactsDir, 'fr040-e2e-overview.png'), fullPage: true })

    await page.getByRole('button', { name: '行为' }).click()
    await expect(page.getByRole('button', { name: 'guard', exact: true })).toBeVisible()
    await expect(page.getByRole('button', { name: 'follow', exact: true })).toBeVisible()
    await expect(page.getByRole('button', { name: 'patrol', exact: true })).toBeVisible()
    await expect(page.getByText('GuardBot')).toHaveCount(0)
    await page.screenshot({ path: path.join(artifactsDir, 'fr040-e2e-group-dim-behavior.png'), fullPage: true })

    await page.getByRole('button', { name: '实例' }).click()
    const survival = botGroupCard(page, '生存服')
    await survival.getByRole('button', { name: '在控制台打开' }).click()
    await expect(page).toHaveURL(/\/instances\/1$/)
    await expect(page.getByRole('heading', { name: /服务器控制台 \/ survival-1/ })).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr040-e2e-open-console.png'), fullPage: true })

    await page.goto('/bots')
    const survivalAgain = botGroupCard(page, '生存服')
    await survivalAgain.getByRole('button', { name: '生存服', exact: true }).click()
    await expect(survivalAgain.getByText('GuardBot')).toBeVisible()
    await expect(survivalAgain.getByText('FollowBot')).toBeVisible()
    await expect(survivalAgain.getByText('共 2 个 Bot')).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr040-e2e-group-peek.png'), fullPage: true })

    await survivalAgain.getByRole('combobox').click()
    await page.getByRole('option', { name: '设为巡逻' }).click()
    await expect(page.getByText('成功 2 · 失败 0')).toBeVisible()
    await page.getByRole('button', { name: '行为' }).click()
    const patrol = botGroupCard(page, 'patrol')
    await expect(patrol.getByText('在线 1 / 共 3')).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr040-e2e-batch-result.png'), fullPage: true })
  })
})
