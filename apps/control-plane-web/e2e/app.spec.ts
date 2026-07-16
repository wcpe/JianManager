import { test, expect } from '@playwright/test'
import { login } from './helpers'

/** 登录后逛关键页：验 mock 模式整站可路由、各页在真浏览器 + MSW Service Worker 下渲染不崩。 */
test.describe('整站导航（mock 模式）', () => {
  test.beforeEach(async ({ page }) => {
    await login(page)
  })

  test('实例管理页渲染', async ({ page }) => {
    await page.goto('/instances')
    await expect(page.locator('[data-page="instances"]')).toBeVisible({ timeout: 15_000 })
  })

  test('节点管理页渲染', async ({ page }) => {
    await page.goto('/nodes')
    await expect(page.locator('[data-page="nodes"]')).toBeVisible({ timeout: 15_000 })
  })

  test('玩家管理页渲染', async ({ page }) => {
    await page.goto('/players')
    await expect(page.getByRole('heading', { name: '玩家管理' })).toBeVisible({ timeout: 15_000 })
  })

  test('备份存储页展示容量并可测试连接', async ({ page }) => {
    await page.goto('/backup-storages')
    await expect(page.getByRole('heading', { name: '备份存储后端' })).toBeVisible()
    await expect(page.getByText('256 MB')).toBeVisible()
    // 先监听行内测试请求，避免列表已有的「连接正常」文本被误当作点击完成信号。
    const testResponsePromise = page.waitForResponse((response) => {
      const request = response.request()
      const pathname = new URL(response.url()).pathname
      return request.method() === 'POST' && /^\/api\/v1\/backup-storages\/\d+\/test$/.test(pathname)
    })
    await page.getByRole('button', { name: '测试', exact: true }).first().click()
    const testResponse = await testResponsePromise
    expect(testResponse.ok()).toBe(true)

    // 成功 toast 必须来自 sonner 门户，且展示后端返回的「连接正常」。
    await expect(page.locator('[data-sonner-toast]').filter({ hasText: '连接正常' }).first()).toBeVisible()
  })
})
