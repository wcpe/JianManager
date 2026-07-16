import { type Page, expect } from '@playwright/test'

/** 经 mock 登录页用种子管理员（admin/admin123）登录，等待进入控制台仪表盘。 */
export async function login(page: Page, username = 'admin', password = 'admin123'): Promise<void> {
  await page.goto('/login')
  await page.getByLabel('用户名', { exact: true }).fill(username)
  await page.getByLabel('密码', { exact: true }).fill(password)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.locator('[data-page="overview"]')).toBeVisible()
}

/**
 * 在 /backups 页选中实例。实例选择器是 @jianmanager/ui Combobox（Radix Popover + button 触发器，
 * 非原生 `<select>`），空值时触发器可读名为占位符「选择实例」；点触发器展开、再点弹层里的选项按钮。
 * 选项来自服务端搜索（默认空 query 返回全部），故种子实例（如 survival-1）直接可见。
 */
export async function selectBackupInstance(page: Page, name: string): Promise<void> {
  await page.getByRole('button', { name: '选择实例', exact: true }).click()
  // 弹层内搜索框键入实例名：既触发服务端搜索、又客户端过滤，避开可见项上限（visibleOptions 截断）导致
  // 目标实例不在首屏可见项里而点不到。
  await page.locator('[data-slot="combobox-content"] input').fill(name)
  await page.getByRole('button', { name, exact: true }).click()
  // 提交后触发器显示所选实例名，确认选中生效（弹层已关闭，此时仅剩触发器）。
  await expect(page.getByRole('button', { name, exact: true })).toBeVisible()
}
