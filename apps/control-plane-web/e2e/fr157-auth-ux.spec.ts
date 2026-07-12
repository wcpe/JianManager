import { test, expect } from '@playwright/test'

/**
 * FR-157 认证体验增强 · 单机（Playwright + mock 模式）验收。
 * 覆盖：登录密码框 eye 显隐切换。
 * （Setup 实时强度条 + 规则 checklist + confirm 不一致聚焦：mock 默认 setupRequired:false 会跳登录页，
 *  由 password-strength.test.ts 单测 + SetupPage.dom.test.tsx 组件测覆盖）
 * 证据落 .tmp/acceptance/FR-157/。
 */

test('FR-157 登录密码框 eye 显隐切换', async ({ page }) => {
  await page.goto('/login')
  const pwd = page.getByLabel('密码', { exact: true })
  await pwd.fill('secret123')

  // 初始为密码态（type=password）
  await expect(pwd).toHaveAttribute('type', 'password')
  await expect(page.getByRole('button', { name: '显示密码' })).toBeVisible()

  // 点 eye → 切为明文 + 按钮变「隐藏密码」
  await page.getByRole('button', { name: '显示密码' }).click()
  await expect(pwd).toHaveAttribute('type', 'text')
  await expect(page.getByRole('button', { name: '隐藏密码' })).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptance/FR-157/single-machine-login-eye.png', fullPage: true })
})
