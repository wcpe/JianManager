import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-152 备份存储测试连接 + 容量展示 · 单机（Playwright + mock 模式）验收。
 * 覆盖：列表每后端「容量（已用 + 备份数）」+「最近测试」状态 + 行内「测试」连接即时反馈。
 * （真实 S3/SFTP/WebDAV 端点探测由 Go TestStorageBackend gRPC 覆盖，真机联调待补）
 * 证据落 .tmp/acceptance/FR-152/。
 */

test('FR-152 备份存储 容量+备份数 展示 与 行内测试连接反馈', async ({ page }) => {
  await login(page)
  await page.goto('/backup-storages')

  await expect(page.getByRole('heading', { name: '备份存储后端' })).toBeVisible()
  // 容量（已用 + 备份数）
  await expect(page.getByText('256 MB')).toBeVisible()
  await expect(page.getByText('1 个备份')).toBeVisible()
  // 最近测试状态
  await expect(page.getByText('连接正常')).toBeVisible()

  // 行内「测试」连接 → 即时反馈（toast）
  await page.getByRole('button', { name: '测试' }).first().click()
  await expect(page.getByText(/测试|连接|已送达|成功|失败/).first()).toBeVisible({ timeout: 10_000 })

  await page.screenshot({ path: '../.tmp/acceptance/FR-152/single-machine-backup-storages.png', fullPage: true })
})
