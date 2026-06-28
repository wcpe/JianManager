import { test, expect } from '@playwright/test'
import { login } from './helpers'

test.describe('登录（mock 模式）', () => {
  test('正确凭据 → 进入控制台仪表盘', async ({ page }) => {
    await login(page)
    await expect(page).toHaveURL('http://localhost:5173/')
    await expect(page.getByRole('heading', { name: '仪表盘' })).toBeVisible()
  })

  test('错误凭据 → 显示错误提示且停留登录页（不整页刷新）', async ({ page }) => {
    await page.goto('/login')
    await page.getByLabel('用户名', { exact: true }).fill('admin')
    await page.getByLabel('密码', { exact: true }).fill('wrong-password')
    await page.getByRole('button', { name: '登录', exact: true }).click()
    // 登录刷页 bug 的真浏览器回归：修复前 401 触发整页跳转 /login 把错误提示冲掉；修复后提示持久可见
    await expect(page.getByText('用户名或密码错误')).toBeVisible()
    await expect(page).toHaveURL(/\/login$/)
  })
})
