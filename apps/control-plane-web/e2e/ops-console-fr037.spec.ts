import { mkdirSync } from 'node:fs'
import path from 'node:path'
import { test, expect } from '@playwright/test'
import { login } from './helpers'

const artifactsDir = process.cwd().endsWith(`${path.sep}web`)
  ? path.resolve(process.cwd(), '..', '.tmp')
  : path.resolve(process.cwd(), '.tmp')

/** FR-037 真浏览器纵切：控制台 Shell（方案 C 品牌顶栏）、服务器选择器与工作区深链。 */
test.describe('FR-037 运维控制台布局（mock 模式真浏览器）', () => {
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

  test('控制台布局：服务器选择器 → 实例工作区深链', async ({ page }) => {
    await expect(page.locator('[data-slot="console-shell"]')).toBeVisible()
    await expect(page.locator('[data-slot="console-sidebar"]')).toBeVisible()
    await expect(page.locator('[data-slot="console-header"]')).toBeVisible()
    await expect(page.locator('[data-slot="console-main"]')).toBeVisible()
    const sidebar = page.locator('[data-slot="console-sidebar"]')
    await expect(sidebar.getByRole('link', { name: '平台首页', exact: true })).toBeVisible()
    await expect(sidebar.getByRole('button', { name: '服务器', exact: true })).toBeVisible()
    await expect(sidebar.getByRole('button', { name: '平台管理', exact: true })).toBeVisible()
    // 方案 C：品牌 Logo 位于顶栏品牌区，节点作用域下拉已下线。
    await expect(page.locator('[data-slot="console-header"]').getByText('JianManager')).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr037-e2e-console-shell.png'), fullPage: true })

    await page.getByRole('button', { name: '选择服务器' }).click()
    await expect(page.getByRole('dialog', { name: '服务器选择器' })).toBeVisible()
    await page.getByRole('searchbox', { name: '搜索服务器' }).fill('creative-1')
    const selector = page.getByTestId('server-selector-virtual')
    await expect(selector).toBeVisible()
    await expect.poll(async () => Number(await selector.getAttribute('data-total-count'))).toBeGreaterThanOrEqual(1)
    await expect(page.getByRole('button', { name: /creative-1.*CRASHED/ })).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr037-e2e-server-selector.png'), fullPage: true })

    await page.getByRole('button', { name: /creative-1.*CRASHED/ }).click()
    await expect(page).toHaveURL(/\/instances\/3$/)
    await expect(page.getByText('creative-1').first()).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr037-e2e-instance-workspace.png'), fullPage: true })
  })
})
