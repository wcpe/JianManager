import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-194 客户端分发页内嵌端到端流程图 · 单机（Playwright + mock 模式）验收。
 * 运维向大白话：首次发布 / 日常更新两段 + 密钥不可丢/整合包只发一次/楔子固定核心可换 要点。纯前端。
 * 证据落 .tmp/acceptance/FR-194/。
 */

test('FR-194 客户端分发流程图 首次发布/日常更新', async ({ page }) => {
  await login(page)
  await page.goto('/client-channels')

  await page.getByRole('button', { name: /分发是怎么跑起来的/ }).click()
  await expect(page.getByRole('heading', { name: /首次发布/ })).toBeVisible()
  await expect(page.getByRole('heading', { name: /日常更新/ })).toBeVisible()
  await expect(page.getByText(/门禁卡.*务必存好.*丢了玩家就断更/)).toBeVisible() // 密钥不可丢
  await expect(page.getByText(/整合包只发一次/).first()).toBeVisible() // 整合包只发一次
  await expect(page.getByText(/楔子固定不变.*只是「更新核心」/)).toBeVisible() // 楔子固定核心可换

  await page.screenshot({ path: '../.tmp/acceptance/FR-194/single-machine-flow-diagram.png', fullPage: true })
})
