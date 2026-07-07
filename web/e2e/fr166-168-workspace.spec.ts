import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-166/167/168 可组合卡片工作区 / 跨实例超级工作台 / 工作区导播台 · 单机（Playwright + mock）验收。
 * 共享 WorkspaceCanvas 引擎（7 功能卡 + 预设持久化）；/super 跨实例拼合；/director 场景轮播。
 * 证据落 .tmp/acceptance/FR-166|167|168/。
 */

test('FR-167 超级工作台: 实例库 + 另存为预设 + 可组合画布', async ({ page }) => {
  await login(page)
  await page.goto('/super')
  await expect(page.getByText('实例库').first()).toBeVisible() // 跨实例实例库拖拽源
  await expect(page.getByRole('button', { name: '另存为预设' })).toBeVisible() // 预设持久化（FR-166）
  await expect(page.getByText(/拖实例加默认卡组.*拖功能加单卡/)).toBeVisible() // 卡片=实例×功能 拼合
  await page.screenshot({ path: '../.tmp/acceptance/FR-167/single-machine-super.png', fullPage: false })
})

test('FR-168 导播台: 轮播 + 场景切换 + 添加场景', async ({ page }) => {
  await login(page)
  await page.goto('/director')
  await expect(page.getByRole('button', { name: '轮播' })).toBeVisible() // 定时轮播
  await expect(page.getByRole('button', { name: '下一个场景' })).toBeVisible() // 瞬切
  await expect(page.getByRole('button', { name: '添加场景' })).toBeVisible()
  await expect(page.getByText(/先到超级工作台拼好布局.*另存为预设/)).toBeVisible() // 预设→场景工作流
  await page.screenshot({ path: '../.tmp/acceptance/FR-168/single-machine-director.png', fullPage: false })
})

test('FR-166 可组合卡片工作区: 功能卡在服务器控制台渲染', async ({ page }) => {
  await login(page)
  // 7 功能卡之一（监控卡）在控制台分区渲染（可组合卡片正文 WorkspaceCardBody）
  await page.goto('/instances/1?tab=metrics')
  await expect(page.getByText('当前健康', { exact: true })).toBeVisible()
  // /super 提供该实例的可组合画布引擎（预设持久化）
  await page.goto('/super')
  await expect(page.getByRole('button', { name: '另存为预设' })).toBeVisible()
})
