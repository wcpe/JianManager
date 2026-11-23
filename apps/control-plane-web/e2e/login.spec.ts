import { test, expect } from '@playwright/test'
import { login } from './helpers'

test.describe('登录（mock 模式）', () => {
  test('正确凭据 → 进入控制台仪表盘', async ({ page }) => {
    await login(page)
    expect(new URL(page.url()).pathname).toBe('/')
    // `/` 的默认首页是 AI 首页（`DEFAULT_HOME_MODE = 'assistant'`），其页名为「AI 助手」；
    // 经典首页（标题「首页」）要靠记忆或 `?home=classic` 才会出现。
    // 本用例验的是「登录成功并落进控制台」，那就断言默认落地的那一面。
    await expect(page.locator('[data-page="assistant-home"]')).toBeVisible()
    await expect(page.getByRole('heading', { name: 'AI 助手', exact: true })).toBeVisible()
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
