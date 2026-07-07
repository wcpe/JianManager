import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-155 平台资产与更新页增强 · 单机（Playwright + mock 模式）验收。
 * 覆盖多页已落地部分：
 *  - /runtime-assets：被引用资产删除禁用 + tooltip
 *  - /database：敏感列脱敏标注
 *  - /system-update：更新说明(changelog) + 回滚
 * （制品/JDK 导入下载进度、全网升级金丝雀分批为 PRD 标注「待补」，不在此断言）
 * 证据落 .tmp/acceptance/FR-155/。
 */

test('FR-155a runtime-assets 被引用删除禁用+tooltip', async ({ page }) => {
  await login(page)
  await page.goto('/runtime-assets')
  await expect(page.getByRole('heading', { name: '运行时与制品' })).toBeVisible()
  await expect(
    page.getByRole('button', { name: '删除', description: /被 \d+ 处引用，删除将被拒绝/ }).first(),
  ).toBeDisabled()
})

test('FR-155b database 敏感列脱敏标注', async ({ page }) => {
  await login(page)
  await page.goto('/database')
  await expect(page.getByRole('heading', { name: '数据库' })).toBeVisible()
  await expect(page.getByText('只读浏览平台数据库的表与数据，敏感列已脱敏')).toBeVisible()
})

test('FR-155c system-update 更新说明+回滚', async ({ page }) => {
  await login(page)
  await page.goto('/system-update')
  await expect(page.getByRole('heading', { name: '系统更新' })).toBeVisible()
  await expect(page.getByText('更新说明').first()).toBeVisible()
  await expect(page.getByRole('button', { name: /回滚 v/ }).first()).toBeVisible()
  await page.screenshot({ path: '../.tmp/acceptance/FR-155/single-machine-system-update.png', fullPage: true })
})
