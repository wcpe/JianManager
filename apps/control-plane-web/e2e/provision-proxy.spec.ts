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

/** 真浏览器纵切：搭建 Velocity 代理并返回 forwarding secret。 */
test.describe('搭建代理（mock 模式真浏览器）', () => {
  test.beforeEach(async ({ page }) => {
    mkdirSync(artifactsDir, { recursive: true })
    await login(page)
  })

  test('搭建 Velocity 代理：版本解析、系统分配提示、secret 提示与代理实例出现', async ({ page }) => {
    // CI runner 上单路由就绪就要 10~20s，本用例要 登录 → 搭建向导多步 → 提交 → 回列表查验，
    // 默认 60s 预算在慢 runner 上会超时（是预算不够，不是断言失败）。
    test.setTimeout(120_000)

    const name = `fr035-velocity-${Date.now()}`

    await page.goto('/instances')
    // 页名已随 i18n 统一由「实例管理」改为「服务器」（instances.title）。
    await expect(page.getByRole('heading', { name: '服务器', exact: true })).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr035-e2e-instances-before.png'), fullPage: true })

    // 【改】「搭建代理」在「更多新建方式」下拉菜单里（role=menuitem），不是页头 button。

    await page.getByRole('button', { name: '更多新建方式', exact: true }).click()

    await page.getByRole('menuitem', { name: '搭建代理', exact: true }).click()
    await expect(page.getByRole('heading', { name: '搭建代理（BungeeCord/Waterfall/Velocity）' })).toBeVisible()
    await expect(page.getByText('端口与工作目录由系统自动分配，无需填写')).toBeVisible()

    await page.getByPlaceholder('velocity-main').fill(name)
    await pickCombo(page, '选择节点', 'alpha')
    await pickCombo(page, '选择版本', '3.3.0-SNAPSHOT')
    await expect(page.getByText(/将下载: velocity-3\.3\.0-SNAPSHOT-196\.jar/)).toBeVisible()
    await expect(page.getByText(/temurin 21/i)).toBeVisible()
    await expect(page.getByRole('checkbox', { name: '正版校验 online-mode' })).toBeChecked()
    await page.screenshot({ path: path.join(artifactsDir, 'fr035-e2e-proxy-dialog.png'), fullPage: true })

    await page.getByRole('button', { name: '搭建', exact: true }).click()
    await expect(page.getByText(/mock-(forwarding-secret|fwd-secret)/)).toBeVisible({ timeout: 10_000 })
    await page.getByRole('button', { name: '完成', exact: true }).click()
    await page.getByRole('searchbox', { name: '搜索实例' }).fill(name)
    await expect(page.getByRole('button', { name, exact: true })).toBeVisible({ timeout: 10_000 })
    await expect(page.getByText('代理').last()).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr035-e2e-proxy-created.png'), fullPage: true })
  })
})
