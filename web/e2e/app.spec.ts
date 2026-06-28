import { test, expect } from '@playwright/test'
import { login } from './helpers'

/** 登录后逛关键页：验 mock 模式整站可路由、各页在真浏览器 + MSW Service Worker 下渲染不崩。 */
test.describe('整站导航（mock 模式）', () => {
  test.beforeEach(async ({ page }) => {
    await login(page)
  })

  test('实例管理页渲染', async ({ page }) => {
    await page.goto('/instances')
    await expect(page.getByRole('heading', { name: '实例管理' })).toBeVisible()
  })

  test('节点管理页渲染', async ({ page }) => {
    await page.goto('/nodes')
    await expect(page.getByRole('heading', { name: '节点管理' })).toBeVisible()
  })

  test('玩家管理页渲染', async ({ page }) => {
    await page.goto('/players')
    await expect(page.getByRole('heading', { name: '玩家管理' })).toBeVisible()
  })
})
