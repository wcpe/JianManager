import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-132 侧栏底部控件图标化（主题/语言/退出）+ 三态直选 + 底部布局 · 单机（Playwright + mock）验收。
 * 现貌（随 FR-164/FR-162 演进）：主题=ThemeSwitcher（Sun/Moon/Monitor 三态直选），语言=图标+语言名 dropdown，
 * 退出登录已迁全局顶栏账户菜单（FR-162 接管，底部不再放退出）；底部「版本左下 · 开源许可右下」。
 * 覆盖：① 底部主题/语言图标控件存在；② 主题三态可直选（切到 dark 后 <html> 加 dark 类）；
 * ③ 底部版本号 + 开源许可入口（右下）；④ 退出不在底部、在顶栏账户菜单。
 * 证据落 .tmp/acceptance/FR-132/。
 */

test('FR-132 底部主题/语言图标 + 三态直选主题 + 版本/开源许可 + 退出迁顶栏', async ({ page }) => {
  await login(page)
  const aside = page.locator('aside').first()

  // ① 底部主题切换按钮（图标）+ 主题色圆点直选（承 FR-164/132）
  await expect(aside.getByRole('button', { name: '切换主题' })).toBeVisible()
  await expect(aside.getByRole('button', { name: 'Jian 绿' })).toBeVisible()
  // 语言切换（图标 + 语言名，中文默认）
  await expect(aside.getByRole('button', { name: '中文', exact: true })).toBeVisible()

  // ③ 底部版本号（左下）+ 开源许可入口（右下），许可指向 /licenses
  // 注：侧栏另有一处「开源许可」导航项（jm-nav-link），底部页脚入口用非 nav-link 精确定位。
  await expect(aside.getByText(/^v\d+\.\d+/)).toBeVisible()
  const licenseFooterLink = aside.locator('a[href="/licenses"]:not(.jm-nav-link)')
  await expect(licenseFooterLink).toBeVisible()
  await expect(licenseFooterLink).toContainText('开源许可')

  // ④ 退出登录不在侧栏底部（已迁 FR-162 顶栏账户菜单）
  await expect(aside.getByRole('button', { name: '退出登录' })).toHaveCount(0)

  await page.screenshot({ path: '../.tmp/acceptance/FR-132/single-machine-bottom-controls.png', fullPage: false })

  // ② 主题三态直选：打开切换器 → 选「深色」→ <html> 应用 dark 类（非盲循环）
  await aside.getByRole('button', { name: '切换主题' }).click()
  await page.getByRole('menuitem', { name: '深色' }).click()
  await expect.poll(() => page.evaluate(() => document.documentElement.classList.contains('dark'))).toBe(true)
  await page.screenshot({ path: '../.tmp/acceptance/FR-132/single-machine-dark-applied.png', fullPage: false })
})
