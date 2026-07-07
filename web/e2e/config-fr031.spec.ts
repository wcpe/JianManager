import { test, expect, type Page } from '@playwright/test'
import { login } from './helpers'

async function expectInputValue(page: Page, value: string): Promise<void> {
  await expect
    .poll(async () => page.locator('input').evaluateAll((inputs, expected) => inputs.some((input) => (input as HTMLInputElement).value === expected), value))
    .toBe(true)
}

/** FR-031：配置文件管理引擎在真实浏览器 + MSW mock 模式下的关键链路。 */
test.describe('配置文件管理引擎（FR-031，mock 模式）', () => {
  test.beforeEach(async ({ page }) => {
    await login(page)
  })

  test('文件配置页可打开 schema 文件、切表单、校验并查看版本 diff/回滚入口', async ({ page }) => {
    await page.goto('/instances/1')
    await expect(page.getByText(/服务器控制台 \/ survival-1/)).toBeVisible()

    await page.getByRole('button', { name: '文件配置', exact: true }).click()
    await expect(page.getByRole('button', { name: /已发现配置/ })).toBeVisible()
    await expect(page.getByText('server.properties').first()).toBeVisible()

    const serverPropertiesRow = page.locator('[title="server.properties"]').filter({ hasText: 'server.properties' }).first()
    await serverPropertiesRow.getByRole('button').first().click()

    await expect(page.getByRole('button', { name: '表单', exact: true })).toBeEnabled()
    await page.getByRole('button', { name: '表单', exact: true }).click()
    await expectInputValue(page, 'A Mock Minecraft Server')
    await expectInputValue(page, '20')
    await expect(page.getByText('valid')).toBeVisible()

    await page.getByRole('button', { name: '校验', exact: true }).click()
    await expect(page.getByText('跨实例一致性校验通过')).toBeVisible()

    await page.getByRole('button', { name: '历史版本', exact: true }).click()
    await expect(page.getByText('改 motd 与玩家上限')).toBeVisible()
    await expect(page.getByText('初始化配置')).toBeVisible()

    const version1 = page.locator('li').filter({ hasText: '#1' })
    const version2 = page.locator('li').filter({ hasText: '#2' })
    await version1.getByRole('button', { name: '从', exact: true }).click()
    await version2.getByRole('button', { name: '到', exact: true }).click()
    await expect(page.getByText('差异 #1 → #2')).toBeVisible()
    await expect(page.getByText(/A Mock Minecraft Server/)).toBeVisible()

    await version1.getByRole('button', { name: '回滚', exact: true }).click()
    await expect(page.getByText('回滚配置版本')).toBeVisible()
    await expect(page.getByText(/确定将 server\.properties 回滚到版本 #1/)).toBeVisible()
  })
})
