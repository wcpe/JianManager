import { mkdirSync } from 'node:fs'
import path from 'node:path'
import { test, expect, type Page } from '@playwright/test'
import { login } from './helpers'

const artifactsDir = process.cwd().endsWith(`${path.sep}web`)
  ? path.resolve(process.cwd(), '..', '.tmp')
  : path.resolve(process.cwd(), '.tmp')

async function pickCombo(page: Page, triggerText: string, optionName: string | RegExp): Promise<void> {
  await page.getByText(triggerText, { exact: true }).click()
  await page.getByRole('button', { name: optionName }).click()
}

/** 真浏览器纵切：一键搭建 Bukkit/Paper 后端子服。 */
test.describe('搭建 Bukkit 子服（mock 模式真浏览器）', () => {
  test.beforeEach(async ({ page }) => {
    mkdirSync(artifactsDir, { recursive: true })
    await login(page)
  })

  test('一键搭建后端子服：核心解析、系统分配提示、提交后实例列表出现 backend', async ({ page }) => {
    const name = `fr034-paper-${Date.now()}`

    await page.goto('/instances')
    // 页名已随 i18n 统一由「实例管理」改为「服务器」（instances.title）。
    await expect(page.getByRole('heading', { name: '服务器', exact: true })).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr034-e2e-instances-before.png'), fullPage: true })

    await page.getByRole('button', { name: '一键搭建', exact: true }).click()
    await expect(page.getByRole('heading', { name: '一键搭建后端子服' })).toBeVisible()
    await expect(page.getByText('端口与工作目录由系统自动分配，无需填写')).toBeVisible()

    await page.getByPlaceholder('lobby').fill(name)
    await pickCombo(page, '选择节点', 'alpha')
    await pickCombo(page, '选择版本', '1.21.1')
    await expect(page.getByText(/将下载: paper-1\.21\.1-196\.jar/)).toBeVisible()
    await expect(page.getByText(/temurin 21/i)).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr034-e2e-provision-dialog.png'), fullPage: true })

    await page.getByRole('button', { name: '搭建', exact: true }).click()
    await expect(page.getByRole('heading', { name: '服务器', exact: true })).toBeVisible({ timeout: 10_000 })
    await page.getByRole('searchbox', { name: '搜索实例' }).fill(name)
    await expect(page.getByRole('button', { name, exact: true })).toBeVisible({ timeout: 10_000 })
    await expect(page.getByText('后端').last()).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr034-e2e-provision-created.png'), fullPage: true })
  })
})
