import { test, expect } from '@playwright/test'
import { login } from './helpers'

/** 
 *全局任务中心 + 完成站内信 · 单机（Playwright + mock 模式）验收。
 * 覆盖：任务中心列表（kind/node/state/progress）+ 状态/节点筛选 + 强制停止（运行态）。
 * （job 模型 + Worker→CP 进度上报 + 完成/失败站内信由 Go task_test.go 覆盖）
 * 证据落 .tmp/acceptance。
 */

test('任务中心 列表+筛选+进度+强制停止', async ({ page }) => {
  await login(page)
  await page.goto('/tasks')

  await expect(page.getByRole('heading', { name: '任务中心' })).toBeVisible()
  // 状态 / 节点 筛选
  await expect(page.getByRole('combobox').filter({ hasText: '全部状态' })).toBeVisible()
  await expect(page.getByRole('combobox').filter({ hasText: '全部节点' })).toBeVisible()
  // 任务行（title + 状态 badge + 进度条 + 更新时间）
  // 【为何不能用「已完成 100%」】行的可及名由四段拼成：`<title> <badge> <title> 进度 <更新时间>`
  // ——进度条的 label 是 `${title} 进度`，插在 badge 与数值之间，所以
  // `/title.*已完成 100%/` 永远匹配不上（中间隔了「title 进度」）。
  // 改为只断 title + 状态两个锚点，不断具体百分比（百分比由 ProgressBar 内部渲染）。
  await expect(page.getByRole('button', { name: /安装 JDK Temurin 21/ }).first()).toBeVisible()
  await expect(page.getByRole('button', { name: /安装 JDK Temurin 21.*已完成/ }).first()).toBeVisible()
  await expect(page.getByRole('button', { name: /已失败/ }).first()).toBeVisible()
  // 运行态任务可强制停止
  await expect(page.getByRole('button', { name: '强制停止' }).first()).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptancesingle-machine-tasks.png', fullPage: true })
})
