import { test, expect } from '@playwright/test'
import { login } from './helpers'

test.describe('登录（mock 模式）', () => {
  test('正确凭据 → 进入控制台仪表盘', async ({ page }) => {
    await login(page)
    await expect(page).toHaveURL('http://localhost:5173/')
    await expect(page.getByRole('heading', { name: '仪表盘' })).toBeVisible()
  })

  test('错误凭据 → 停留登录页（仍在 /login）', async ({ page }) => {
    await page.goto('/login')
    await page.getByLabel('用户名', { exact: true }).fill('admin')
    await page.getByLabel('密码', { exact: true }).fill('wrong-password')
    await page.getByRole('button', { name: '登录', exact: true }).click()
    // 不进入控制台：URL 仍是 /login（错误提示是否持久属登录 bug 范畴，由 sdd-fix-bug 回归覆盖）
    await expect(page).toHaveURL(/\/login$/)
    await expect(page.getByRole('button', { name: '登录' })).toBeVisible()
  })
})
