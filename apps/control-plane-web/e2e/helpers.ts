import { type Page, expect } from '@playwright/test'

/** 经 mock 登录页用种子管理员（admin/admin123）登录，等待进入控制台仪表盘。 */
export async function login(page: Page, username = 'admin', password = 'admin123'): Promise<void> {
  await page.goto('/login')
  await page.getByLabel('用户名', { exact: true }).fill(username)
  await page.getByLabel('密码', { exact: true }).fill(password)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.locator('[data-page="overview"]')).toBeVisible()
}

/** 在服务端搜索型 Combobox 中输入并选择精确选项。 */
export async function selectComboboxOption(page: Page, triggerText: string, optionText: string): Promise<void> {
  const trigger = page.locator('[data-slot="combobox-trigger"]').filter({ hasText: triggerText }).first()
  await expect(trigger).toBeVisible()
  await trigger.click()

  const content = page.locator('[data-slot="combobox-content"]')
  await expect(content).toBeVisible()
  await content.locator('input').fill(optionText)
  const option = content.getByRole('button', { name: optionText, exact: true })
  await expect(option).toBeVisible()
  await option.click()
  // 提交后触发器显示所选项，确认选中生效且弹层已关闭。
  await expect(page.getByRole('button', { name: optionText, exact: true })).toBeVisible()
}
