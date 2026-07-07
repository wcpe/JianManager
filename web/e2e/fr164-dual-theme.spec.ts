import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-164 全局双主题（Jian 绿 / 青绿）+ 明暗模式 · 单机（Playwright + mock 模式）验收。
 * 覆盖：CSS 变量驱动、一处切换全站、持久（localStorage）。
 * 证据落 .tmp/acceptance/FR-164/。
 */

test('FR-164 双色主题 + 明暗模式切换 + 持久', async ({ page }) => {
  await login(page)

  const primaryBefore = await page.evaluate(() =>
    getComputedStyle(document.documentElement).getPropertyValue('--primary').trim(),
  )

  // 切青绿 → --primary 变 + localStorage.colorTheme=teal
  await page.getByRole('button', { name: '青绿', exact: true }).click()
  await expect
    .poll(() => page.evaluate(() => localStorage.getItem('colorTheme')))
    .toBe('teal')
  const primaryAfter = await page.evaluate(() =>
    getComputedStyle(document.documentElement).getPropertyValue('--primary').trim(),
  )
  expect(primaryAfter).not.toBe(primaryBefore) // CSS 变量驱动换色

  // 切深色 → html.dark + localStorage.theme=dark
  await page.getByRole('button', { name: '切换主题' }).click()
  await page.getByRole('menuitem', { name: '深色' }).click()
  await expect(page.locator('html')).toHaveClass(/dark/)
  await expect.poll(() => page.evaluate(() => localStorage.getItem('theme'))).toBe('dark')

  await page.screenshot({ path: '../.tmp/acceptance/FR-164/single-machine-dark-teal.png', fullPage: true })
})
