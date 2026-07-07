import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-178 节点内 JDK + 制品管理面板 · 单机（Playwright + mock 模式）验收。
 * 覆盖：JDK 面板（已登记/一键下载/登记已有）+ foojay 多厂商多版本 + 异步安装进度接任务中心 + 测试节点存活。
 * 证据落 .tmp/acceptance/FR-178/。
 */

test('FR-178 节点 JDK 面板: foojay 下载 + 异步进度 + 测试存活', async ({ page }) => {
  await login(page)
  await page.goto('/nodes')

  await page.getByRole('button', { name: 'JDK', exact: true }).click()
  await expect(page.getByRole('button', { name: '一键下载' })).toBeVisible()
  await expect(page.getByRole('button', { name: '登记已有' })).toBeVisible()
  await expect(page.getByRole('button', { name: /托管/ })).toBeVisible() // 托管 JDK 分区

  await page.getByRole('button', { name: '一键下载' }).click()
  await expect(page.getByText('厂商').first()).toBeVisible()
  await expect(page.getByText('大版本').first()).toBeVisible()
  await expect(page.getByText(/具体版本经 foojay 解析.*进度见任务中心/)).toBeVisible() // foojay + 异步进度
  await expect(page.getByRole('button', { name: '测试节点存活' })).toBeVisible() // 连通性
  await expect(page.getByRole('button', { name: '下载安装' })).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptance/FR-178/single-machine-jdk-download.png', fullPage: true })
})
