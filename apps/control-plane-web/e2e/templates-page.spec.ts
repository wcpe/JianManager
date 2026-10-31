import { test, expect } from '@playwright/test'
import { login } from './helpers'

/** 
 *模板应用到实例 + 变量填充预览 · 单机（Playwright + mock 模式）验收。
 * 覆盖：模板卡片版本/更新时间；「一键部署」→ 部署对话框 预填 + 占位变量填充 + startCommand 复制/展开。
 * 证据落 .tmp/acceptance。
 */

test('模板一键部署 + 变量填充预览 + startCommand 复制', async ({ page }) => {
  await login(page)
  await page.goto('/templates')

  await expect(page.getByRole('heading', { name: '服务端模板' })).toBeVisible()
  await expect(page.getByText('Paper 1.21')).toBeVisible()
  await expect(page.getByText('Java 21').first()).toBeVisible() // 版本（更新时间见真机快照/组件测）

  // 【为何限定 `button.w-full`】「一键部署」这个可及名会匹配到 4 个元素：
  // 页顶「一键开服」统计卡里的 <Zap> 图标（16×16、元文本）也是 button 且排在最前，
  // `.first()` 会点到它 —— 点了没反应，报错却是「对话框不存在」。
  // 真正带部署动作的是卡片底部那个满宽按钮。
  await page.locator('button.w-full').filter({ hasText: '一键部署' }).first().click()
  const dialog = page.getByRole('dialog', { name: /部署「Paper 1.21」/ })
  await expect(dialog).toBeVisible()
  // 变量填充预览
  await expect(dialog.getByText('变量填充')).toBeVisible()
  await expect(dialog.getByText('模板含占位变量，填好后将代入启动命令')).toBeVisible()
  // startCommand 展开 + 复制
  await expect(dialog.getByText('java -Xmx{{ram}}G -jar paper.jar nogui')).toBeVisible()
  await expect(dialog.getByRole('button', { name: '复制启动命令' })).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptancesingle-machine-template-deploy.png', fullPage: true })
})
