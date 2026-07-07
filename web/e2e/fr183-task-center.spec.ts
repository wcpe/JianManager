import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-183 全局任务中心 + 完成站内信 · 单机（Playwright + mock 模式）验收。
 * 覆盖：任务中心列表（kind/node/state/progress）+ 状态/节点筛选 + 强制停止（运行态）。
 * （job 模型 + Worker→CP 进度上报 + 完成/失败站内信由 Go task_test.go 覆盖）
 * 证据落 .tmp/acceptance/FR-183/。
 */

test('FR-183 任务中心 列表+筛选+进度+强制停止', async ({ page }) => {
  await login(page)
  await page.goto('/tasks')

  await expect(page.getByRole('heading', { name: '任务中心' })).toBeVisible()
  // 状态 / 节点 筛选
  await expect(page.getByRole('combobox').filter({ hasText: '全部状态' })).toBeVisible()
  await expect(page.getByRole('combobox').filter({ hasText: '全部节点' })).toBeVisible()
  // 任务行（kind + node + state + progress）
  await expect(page.getByRole('button', { name: /安装 JDK Temurin 21.*已完成 100%/ })).toBeVisible()
  await expect(page.getByRole('button', { name: /已失败/ }).first()).toBeVisible()
  // 运行态任务可强制停止
  await expect(page.getByRole('button', { name: '强制停止' }).first()).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptance/FR-183/single-machine-tasks.png', fullPage: true })
})
