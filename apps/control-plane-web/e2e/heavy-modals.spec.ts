import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-189 重度模态重做（添加节点 + 创建实例）· 单机（Playwright + mock 模式）验收。
 * 覆盖：AddNode 自适应壳模态 + 「自动安装/手动连接」Tab（Linux/Windows 命令）。
 * （创建实例已改独立向导页 FR-230，由 InstanceWizardPage.dom.test.tsx 覆盖）
 * 证据落 .tmp/acceptance/FR-189/。
 */

test('FR-189 添加节点模态 自动安装/手动连接 Tab', async ({ page }) => {
  await login(page)
  await page.goto('/nodes')

  await page.getByRole('button', { name: '添加节点' }).click()
  const dlg = page.getByRole('dialog', { name: '添加节点' })
  await expect(dlg).toBeVisible()
  await dlg.getByRole('button', { name: '生成一键命令' }).click()

  const cmdDlg = page.getByRole('dialog', { name: /一键安装命令/ })
  await expect(cmdDlg.getByRole('tab', { name: '自动安装' })).toBeVisible()
  await expect(cmdDlg.getByRole('tab', { name: '手动连接' })).toBeVisible()
  await expect(cmdDlg.getByText('Linux / macOS').first()).toBeVisible()
  await expect(cmdDlg.getByText(/Windows \(PowerShell\)/).first()).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptance/FR-189/single-machine-addnode.png', fullPage: true })
})
