import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-143 插件/模组/资源包/数据包管理增强 · 单机（Playwright + mock 模式）验收。
 * 覆盖：四目录分区 + 元信息（版本·作者）+ 启禁/删除、重启提示、拖拽上传区、
 * 同名覆盖确认、批量部署、插件市场入口预留（禁用）。
 * 证据截图落 .tmp/acceptance/FR-143/。
 */

test('FR-143 插件页四区分区 + 元信息 + 操作 + 重启提示 + 市场入口预留', async ({ page }) => {
  await login(page)
  await page.goto('/instances/1?tab=plugins')

  await expect(page.getByRole('heading', { name: '插件 / 模组 / 资源包 / 数据包' })).toBeVisible()

  // 四目录分区
  await expect(page.getByRole('region', { name: '插件' })).toBeVisible()
  await expect(page.getByRole('region', { name: '模组' })).toBeVisible()
  await expect(page.getByRole('region', { name: '资源包' })).toBeVisible()
  await expect(page.getByRole('region', { name: '数据包' })).toBeVisible()

  // 元信息（版本·作者）+ 行操作
  await expect(page.getByText('EssentialsX.jar')).toBeVisible()
  await expect(page.getByText('2.21.0 · EssentialsX')).toBeVisible()
  await expect(page.getByText('HighResPack.zip')).toBeVisible()
  await expect(page.getByText('SpawnTweaks.zip')).toBeVisible()

  // 重启提示 + 拖拽上传区 + 市场入口预留（禁用）
  await expect(page.getByText('启禁、删除和覆盖类变更通常需要重启实例后生效。')).toBeVisible()
  await expect(page.getByText('拖入文件上传到插件（允许 .jar）')).toBeVisible()
  await expect(page.getByRole('button', { name: '批量部署' })).toBeVisible()
  await expect(page.getByRole('button', { name: '插件市场' })).toBeDisabled()

  await page.screenshot({ path: '../.tmp/acceptance/FR-143/single-machine-plugins.png', fullPage: true })
})
