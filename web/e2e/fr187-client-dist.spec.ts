import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-187 客户端分发迁「运营」域 + 全流程向导重做 · 单机（Playwright + mock 模式）验收。
 * 覆盖：频道列表 + 流程图入口 + 频道工作台就绪度步骤器 + 空状态引导 + 建密钥模态化。
 * 证据落 .tmp/acceptance/FR-187/。
 */

test('FR-187 客户端分发 频道就绪度步骤器 + 空状态引导', async ({ page }) => {
  await login(page)
  await page.goto('/client-channels')

  await expect(page.getByRole('heading', { name: '客户端分发' })).toBeVisible()
  await expect(page.getByRole('button', { name: '新增频道' })).toBeVisible()
  await expect(page.getByRole('button', { name: /分发是怎么跑起来的/ })).toBeVisible() // FR-194 流程图入口

  // 进未发布频道 → 就绪度步骤器 + 空状态引导
  await page.getByRole('button', { name: /survival-s2.*就绪度 1\/4/ }).click()
  await expect(page.getByRole('heading', { name: '接入就绪度' })).toBeVisible()
  await expect(page.getByText('就绪度 1/4')).toBeVisible()
  await expect(page.getByText('拉取密钥').first()).toBeVisible()
  await expect(page.getByText('发布版本').first()).toBeVisible()
  await expect(page.getByRole('button', { name: '创建密钥' }).first()).toBeVisible() // 建密钥模态入口

  await page.screenshot({ path: '../.tmp/acceptance/FR-187/single-machine-channel-readiness.png', fullPage: true })
})
