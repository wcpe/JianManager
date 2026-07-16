import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-063 平台设置（全量平台配置可视化 + 运行时调整）· 单机（Playwright + mock 模式）验收。
 *
 * 覆盖两条核心验收：
 *  1. 全量平台配置可视化：内部分类侧栏含 外观/日志/运行时/网络/备份/安全 六类；切分类见到对应配置项
 *     （log.level 只读/可编辑项 + graceful_stop.timeout + debug.mode 运行时开关）。
 *  2. 运行时调整免重启：改可编辑项（graceful_stop.timeout）→ 保存 → 前端经缓存失效重取（不刷新页面）
 *     即回填新值、旧值消失，印证「运行时调整、无需重启」的热应用路径（与 FR-225 调试模式共用同一机制）。
 *
 * 以真实 UI 点击驱动（登录种子管理员 admin/admin123，平台配置分类仅平台管理员可见）。
 * 证据落 .tmp/acceptance/FR-063/。
 */

test('FR-063a 全量平台配置分类可视化 + 运行时开关项存在', async ({ page }) => {
  await login(page)
  await page.goto('/settings')
  await expect(page.getByRole('heading', { name: '系统设置' })).toBeVisible()

  // 分类侧栏覆盖全量配置域（外观为客户端偏好，其余为平台配置）。
  for (const cat of ['外观', '日志', '运行时', '网络', '备份', '安全 / 系统']) {
    await expect(page.getByRole('button', { name: cat, exact: true })).toBeVisible()
  }

  // 日志分类：可见 log.level 与 debug.mode（FR-225 运行时热重载开关，共用 FR-063 运行时机制）。
  await page.getByRole('button', { name: '日志', exact: true }).click()
  await expect(page.getByText('log.level')).toBeVisible()
  await expect(page.getByText('debug.mode')).toBeVisible()

  // 运行时分类：可见 graceful_stop.timeout（可运行时调整的平台项）。
  await page.getByRole('button', { name: '运行时', exact: true }).click()
  await expect(page.getByText('graceful_stop.timeout')).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptance/FR-063/single-machine-platform-settings.png', fullPage: true })
})

test('FR-063b 运行时调整免重启：改值保存后前端热回读新值', async ({ page }) => {
  await login(page)
  await page.goto('/settings')

  // 切到运行时分类，定位 graceful_stop.timeout 所在行的输入框（按键名文本锚定该行 div，避开重名键）。
  await page.getByRole('button', { name: '运行时', exact: true }).click()
  const row = page.locator('div.px-3.py-2').filter({ hasText: 'graceful_stop.timeout' })
  const box = row.locator('input').first()
  // graceful_stop.timeout 是 Go duration（须带单位，如 30s）；validateSettingDraft 拒绝裸数字，
  // 裸数字会让「保存」保持禁用（前端 422 双闸）。故用合法 duration 驱动。
  await expect(box).toHaveValue('30s')
  await box.fill('60s')

  const saveButton = page.getByRole('button', { name: '保存', exact: true })
  await expect(saveButton).toBeEnabled()
  // 先监听保存请求，确保后续禁用态与回读值对应本次 PUT 成功响应。
  const saveResponsePromise = page.waitForResponse((response) => {
    const request = response.request()
    return request.method() === 'PUT' && new URL(response.url()).pathname === '/api/v1/settings'
  })
  await saveButton.click()
  const saveResponse = await saveResponsePromise
  expect(saveResponse.ok()).toBe(true)

  // 免重启热回读：PUT 成功后草稿清空，保存按钮恢复禁用并回填后端新值。
  await expect(saveButton).toBeDisabled()
  await expect(box).toHaveValue('60s')

  await page.screenshot({ path: '../.tmp/acceptance/FR-063/single-machine-runtime-apply.png', fullPage: true })
})
