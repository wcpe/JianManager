import { mkdirSync } from 'node:fs'
import path from 'node:path'
import { test, expect, type Page, type Locator } from '@playwright/test'
import { login } from './helpers'

const artifactsDir = process.cwd().endsWith(`${path.sep}web`)
  ? path.resolve(process.cwd(), '..', '.tmp')
  : path.resolve(process.cwd(), '.tmp')

function instanceCard(page: Page, name: string): Locator {
  return page.locator('div.bg-card').filter({ has: page.getByRole('button', { name, exact: true }) })
}

/** FR-036 真浏览器纵切：一键复制 backend 子服、预检、配置修正参数与代理注册选择。 */
test.describe('FR-036 一键复制子服（mock 模式真浏览器）', () => {
  test.beforeEach(async ({ page }) => {
    mkdirSync(artifactsDir, { recursive: true })
    await login(page)
  })

  test('复制子服：打开菜单、预检资源、提交后副本实例出现', async ({ page }) => {
    const name = `fr036-clone-${Date.now()}`

    await page.goto('/instances')
    await expect(page.getByRole('heading', { name: '实例管理' })).toBeVisible()
    const sourceCard = instanceCard(page, 'creative-1')
    await expect(sourceCard).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr036-e2e-instances-before.png'), fullPage: true })

    await sourceCard.getByRole('button', { name: '更多操作' }).click()
    await page.getByRole('menuitem', { name: '复制', exact: true }).click()
    await expect(page.getByRole('heading', { name: '复制子服 — creative-1' })).toBeVisible()
    const form = page.locator('form').filter({ hasText: '复制范围' })

    await form.getByRole('textbox').nth(0).fill(name)
    await form.getByRole('textbox').nth(1).fill('克隆大厅')
    await form.getByRole('textbox').nth(2).fill('world_clone')
    await expect(page.getByText(/仅复制核心 jar/)).toBeVisible()
    await page.getByRole('checkbox', { name: 'lobby-proxy' }).click()

    await form.getByRole('button', { name: '预检', exact: true }).click()
    await expect(page.getByText(/将分配：端口 25566\/25566，目录 \/srv\/instances\//)).toBeVisible()
    await expect(page.getByText(/将排除/)).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr036-e2e-clone-dialog.png'), fullPage: true })

    await form.getByRole('button', { name: '复制', exact: true }).click()
    await expect(page.getByRole('heading', { name: '复制子服 — creative-1' })).toHaveCount(0, { timeout: 10_000 })
    await page.getByTestId('instances-card-virtual').evaluate((el) => { el.scrollTop = el.scrollHeight })
    await expect(page.getByRole('button', { name, exact: true })).toBeVisible({ timeout: 10_000 })
    await expect(page.getByText('后端').last()).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr036-e2e-clone-created.png'), fullPage: true })
  })
})
