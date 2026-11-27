import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * 节点运行时页 · 单机（Playwright + mock 模式）验收。
 *
 * 【2026-11 改写说明】原用例断言旧页面的「一键下载」「登记已有」「托管」三个按钮，
 * 页面重做后这些入口变成「清单表工具条 + 抽屉分段」：
 *   - 扫描发现 → `runtime-open-scan`
 *   - 新增运行时（安装 / 登记都在里面）→ `runtime-open-install`
 *   - 托管/外部区分 → 清单表上方的来源 chip
 * 本用例改为验证这几条入口真实可达、抽屉内分段齐全，
 * 以及「测试节点存活」这个连通性动作仍在（它在 JDK 安装分段内）。
 */
test('节点运行时页：清单 + 扫描/新增入口 + 抽屉分段齐全 + 连通性', async ({ page }) => {
  test.setTimeout(180_000)
  await login(page)
  // 阶段 6 起 /nodes 是列表态，分段工具只在详情态；直接深链到运行时分段。
  await page.goto('/nodes?node=1&tab=runtime')

  // ---- 清单与两个入口 ----
  await expect(page.getByTestId('runtime-inventory-table')).toBeVisible({ timeout: 90_000 })
  await expect(page.getByTestId('runtime-open-scan')).toBeVisible()
  await expect(page.getByTestId('runtime-open-install')).toBeVisible()

  // ---- 抽屉分段齐全：安装 JDK / 安装 Node.js / 登记已有 JDK / 登记已有 Node.js / 扫描发现 ----
  await page.getByTestId('runtime-open-install').click()
  for (const id of [
    'runtime-add-jdk',
    'runtime-add-node',
    'runtime-add-register',
    'runtime-add-register-node',
    'runtime-add-scan',
  ]) {
    await expect(page.getByTestId(id)).toBeVisible({ timeout: 30_000 })
  }

  // ---- 安装分段：版本下拉 + 提交按钮 + 连通性动作 ----
  await page.getByTestId('runtime-add-jdk').click()
  await expect(page.getByTestId('jdk-install-submit')).toBeVisible({ timeout: 30_000 })
  await expect(page.getByRole('button', { name: /测试节点存活/ })).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptance-single-machine-jdk-download.png', fullPage: true })
})
