import { type Page, expect } from '@playwright/test'

/** 经 mock 登录页用种子管理员（admin/admin123）登录，等待进入控制台仪表盘。 */
export async function login(page: Page, username = 'admin', password = 'admin123'): Promise<void> {
  await page.goto('/login')
  await page.getByLabel('用户名', { exact: true }).fill(username)
  await page.getByLabel('密码', { exact: true }).fill(password)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.getByRole('heading', { name: '仪表盘' })).toBeVisible()
}
