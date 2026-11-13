import { test, expect } from '@playwright/test'
import { login, selectComboboxOption } from './helpers'

/** 
 *备份完整性校验和审计服务端分页与导出 · 单机（Playwright + mock）验收（前端消费面）。
 * 后端由 Go backup_test.go（Checksum 落库/恢复传参）、audit_test.go（分页 envelope / NDJSON 导出 / format 校验）覆盖。
 * 证据落 .tmp/acceptance|172/。
 */

test('备份列表展示校验和', async ({ page }) => {
  await login(page)
  await page.goto('/backups')
  await selectComboboxOption(page, '选择实例', 'survival-1')
  await expect(page.getByText('校验和').first()).toBeVisible() // 校验和列
  await page.screenshot({ path: '../.tmp/acceptancesingle-machine-backup-checksum.png', fullPage: true })
})

test('审计真实总数分页 + 导出入口', async ({ page }) => {
  await login(page)
  await page.goto('/audit')
  await expect(page.getByText(/共 \d+ 条/)).toBeVisible() // 真实命中总数（分页 envelope）
  await expect(page.getByRole('button', { name: '加载更多' })).toBeVisible() // 分页
  await expect(page.getByRole('button', { name: '导出' })).toBeVisible() // 导出 endpoint 入口
  await page.screenshot({ path: '../.tmp/acceptancesingle-machine-audit-pagination.png', fullPage: true })
})
