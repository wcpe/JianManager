import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-191 客户端分发发布/上传/预览定向重做 · 单机（Playwright + mock 模式）验收。
 * 覆盖：发布改独立页面（非模态）+ 本地暂存点发布才上传 + zip 自动按结构编排 + 文件树预览。
 * 证据落 .tmp/acceptance/FR-191/。
 */

test('FR-191 发布独立页 + 本地暂存 + zip 编排', async ({ page }) => {
  await login(page)
  await page.goto('/client-channels/1/publish')

  await expect(page.getByRole('heading', { name: '发布新版本' })).toBeVisible() // 独立页面
  await expect(page.getByText(/本地暂存并编排（此时不上传），点「发布」才批量上传/)).toBeVisible()
  await expect(page.getByText(/支持散文件、整个文件夹（保留目录结构）与 ZIP 整合包/)).toBeVisible()
  await expect(page.getByText(/上传 \.zip 会在浏览器内解包，按包内目录结构自动编排/)).toBeVisible() // zip 编排

  await page.screenshot({ path: '../.tmp/acceptance/FR-191/single-machine-publish-page.png', fullPage: true })
})
