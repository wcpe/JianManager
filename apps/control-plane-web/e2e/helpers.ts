import { type Page, expect } from '@playwright/test'

/**
 * 经 mock 登录页用种子管理员（admin/admin123）登录，等待**控制台外壳**就绪。
 *
 * 【为什么不等 `[data-page="overview"]`】那是首页的**内容**节点，其渲染依赖
 * `/metrics/overview` 等一组查询；导航重构引入路由分块后首屏要多下载几个 chunk，
 * 实测从登录到内容就绪可超过 5 秒——而 Playwright 默认断言超时正是 5 秒，
 * 于是每一个调用 login() 的用例都会卡在这一行上失败（曾一次波及 39 个用例）。
 *
 * 外壳 `[data-slot="console-shell"]` 在鉴权通过后立即挂载，语义上它才是「已进入控制台」
 * 的标志；各用例真正关心的页面元素由它们自己断言，不该由公共 helper 代劳。
 */
export async function login(page: Page, username = 'admin', password = 'admin123'): Promise<void> {
  await page.goto('/login')
  await page.getByLabel('用户名', { exact: true }).fill(username)
  await page.getByLabel('密码', { exact: true }).fill(password)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.locator('[data-slot="console-shell"]')).toBeVisible({ timeout: 20_000 })
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
