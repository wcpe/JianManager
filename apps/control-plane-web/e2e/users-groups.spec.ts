import { test, expect, type Page } from '@playwright/test'
import { login, useClassicHome } from './helpers'

/** 
 *用户与组管理能力补齐 · 单机（Playwright + mock 模式）验收。
 * 覆盖：用户 改密/启停/编辑；组 编辑/删除/改配额/管成员；角色权限差异说明；
 * 并做「真机」端到端：改密后新密码可登录、停用账户被拒登录（mock 登录校验 username+password+!disabled）。
 * 证据落 .tmp/acceptance。
 */

async function logout(page: Page): Promise<void> {
  await page.getByRole('button', { name: '账户' }).click()
  await page.getByRole('menuitem', { name: '退出登录' }).click()
  await expect(page.getByRole('button', { name: '登录', exact: true })).toBeVisible()
}

test('用户/组 管理能力 + 角色权限说明', async ({ page }) => {
  await login(page)
  await page.goto('/users')
  await expect(page.getByRole('heading', { name: '用户管理' })).toBeVisible()
  await expect(page.getByRole('switch', { name: '状态' }).first()).toBeVisible() // 启停
  const opRow = page.getByRole('row').filter({ hasText: 'operator' })
  await opRow.getByRole('button', { name: '编辑' }).click()
  const dialog = page.getByRole('dialog', { name: /编辑用户「operator」/ })
  await expect(dialog.getByText(/平台管理员拥有全部权限/)).toBeVisible() // 角色权限差异说明
  await expect(dialog.getByPlaceholder('留空则不修改')).toBeVisible() // 改密
  await page.keyboard.press('Escape')

  await page.goto('/groups')
  await expect(page.getByRole('heading', { name: '用户组管理' })).toBeVisible()
  await expect(page.getByRole('button', { name: '编辑' }).first()).toBeVisible()
  await expect(page.getByRole('button', { name: '成员' }).first()).toBeVisible()
  await expect(page.getByText('实例配额').first()).toBeVisible() // 配额
  await page.screenshot({ path: '../.tmp/acceptancesingle-machine-groups.png', fullPage: true })
})

test('真机流: 改密后新密码可登录', async ({ page }) => {
  // 本用例的就绪信号是经典首页（`[data-page="overview"]`），而 `/` 默认已改 AI 首页。
  await useClassicHome(page)
  await login(page)
  await page.goto('/users')
  const opRow = page.getByRole('row').filter({ hasText: 'operator' })
  await opRow.getByRole('button', { name: '编辑' }).click()
  const dialog = page.getByRole('dialog', { name: /编辑用户「operator」/ })
  await dialog.getByPlaceholder('留空则不修改').fill('newop123')
  await dialog.getByRole('button', { name: '保存' }).click()
  await expect(dialog).not.toBeVisible()

  await logout(page)
  // 用新密码登录 operator → 成功进入控制台
  await page.getByLabel('用户名', { exact: true }).fill('operator')
  await page.getByLabel('密码', { exact: true }).fill('newop123')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.locator('[data-page="overview"]')).toBeVisible()
})

test('真机流: 停用账户被拒登录', async ({ page }) => {
  await login(page)
  await page.goto('/users')
  const opRow = page.getByRole('row').filter({ hasText: 'operator' })
  await opRow.getByRole('switch', { name: '状态' }).click() // 停用 operator
  await logout(page)
  // 停用后用原密码登录 → 被拒（仍在登录页 + 报错）
  await page.getByLabel('用户名', { exact: true }).fill('operator')
  await page.getByLabel('密码', { exact: true }).fill('op123456')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.getByText(/用户名或密码错误|已停用|禁用/).first()).toBeVisible({ timeout: 10_000 })
  // 被拒 ⇒ 停在登录页（而不是进入了控制台）。
  // 【为何不断言 `[data-page="overview"]` 计数为 0】`/` 的默认首页已是 AI 首页，
  // 那个选择器在未登录时也本就为 0——“计数为 0”不能区分“被拒”与“首页形态变了”。
  await expect(page).toHaveURL(/\/login$/)
  await expect(page.locator('[data-slot="console-shell"]')).toHaveCount(0)
})
