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
    // 行内测试连接：行按钮名为「测试」（「测试连接」是创建表单内的按钮，此处表单未开）。
    await page.getByRole('button', { name: '测试', exact: true }).first().click()
    // 成功 → toast 显示后端 message（mock `/backup-storages/:id/test` 返回「连接正常」）。
    // 原断言「连通 32 ms」为陈旧文案（前端 handleTest 用 toast.success(result.message)，从不渲染延迟毫秒）。
    await expect(page.getByText('连接正常').first()).toBeVisible()
  })
})
