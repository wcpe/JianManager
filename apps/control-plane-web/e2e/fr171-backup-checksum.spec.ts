import { test, expect } from '@playwright/test'
import { login, selectComboboxOption } from './helpers'

/**
 * FR-171 备份完整性校验和 · 单机（Playwright + mock 模式）验收。
 * 校验和展示：列表/详情显示 sha256 前缀（mock seed checksum='a'*64 → 'aaaaaaaaaaaa...'）。
 * 校验逻辑（compute/verify/篡改检出）与远程 round-trip 由 Go 测 + TestS3_RealMinIO 覆盖。
 */

test('FR-171 备份校验和展示（校验和列 + sha256 前缀）', async ({ page }) => {
  await login(page)
  await page.goto('/backups')
  // 备份列表按实例作用域：搜索并选中 survival-1（mock seed 有 checksum='a'*64 的备份）。
  await selectComboboxOption(page, '选择实例', 'survival-1')
  await expect(page.getByText('校验和').first()).toBeVisible()
  await expect(page.getByText('aaaaaaaaaaaa...').first()).toBeVisible()
  await page.screenshot({ path: '../.tmp/acceptance/FR-171/single-machine-checksum.png', fullPage: true })
})
