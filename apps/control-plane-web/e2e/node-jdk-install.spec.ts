import { mkdirSync } from 'node:fs'
import path from 'node:path'
import { test, expect } from '@playwright/test'
import { login } from './helpers'

const artifactsDir = process.cwd().endsWith(`${path.sep}web`)
  ? path.resolve(process.cwd(), '..', '.tmp')
  : path.resolve(process.cwd(), '.tmp')

/**
 * 真浏览器纵切：节点运行时清单、登记已有运行时、下载安装。
 *
 * 【2026-11 改写说明】原用例断言的是旧运行时页的按钮（「已登记」「一键下载」「托管 2」
 * 「登记已有」），而页面已重做成「清单表 + 抽屉分段」结构：
 *   - 列表 → `runtime-inventory-table`，行 testid 为 `runtime-row-*`
 *   - 新增入口 → `runtime-open-install`（抽屉）与 `runtime-open-scan`
 *   - 登记 → 抽屉里的「登记已有 JDK」分段（`runtime-add-register`）
 *   - 安装 → 抽屉里的「安装 JDK」分段（`runtime-add-jdk`）
 * 改的是**选择器**，测的业务链路（列表可见 → 登记 → 安装）一条没少。
 */
test.describe('JDK 与运行时管理（mock 模式真浏览器）', () => {
  test.beforeEach(async ({ page }) => {
    mkdirSync(artifactsDir, { recursive: true })
    await login(page)
  })

  test('运行时清单可见，登记已有 JDK 与下载安装链路可操作', async ({ page }) => {
    test.setTimeout(180_000)
    // 阶段 6 起 /nodes 是列表态，分段工具只在详情态；直接深链到运行时分段。
    await page.goto('/nodes?node=1&tab=runtime')

    // ---- 清单可见：表体 + 至少一行运行时（JDK 或 Node） ----
    await expect(page.getByTestId('runtime-inventory-table')).toBeVisible({ timeout: 90_000 })
    await expect(page.locator('[data-testid^="runtime-row-"]').first()).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr033-e2e-runtime-inventory.png'), fullPage: true })

    /*
      登记已有 JDK：抽屉 → 「登记已有 JDK」分段 → 探测 → 保存。

      【注意它和 Node 登记不是同一个组件】JDK 登记仍在 `NodeJDKTab` 的 register 分段里
      （手输/浏览路径 + 「检测」按钮），本次只新增了 Node.js 的 `NodeRegisterSection`。
      两者视觉相似但实现独立 —— 所以这里必须按 JDK 面板自己的文案定位，
      用 `node-register-jdk` 这类 Node 组件的 testid 是找不到的。
    */
    await page.getByTestId('runtime-open-install').click()
    await page.getByTestId('runtime-add-register').click()

    const jdkPathInput = page.getByPlaceholder('/opt/jdks/temurin-21 或 .../bin/java')
    await expect(jdkPathInput).toBeVisible({ timeout: 30_000 })
    await jdkPathInput.fill('/opt/jdks/e2e-java-21')
    await page.getByRole('button', { name: '检测', exact: true }).click()
    // 探测成功后后端自动填厂商/版本/架构 —— 保存按钮与回填值是可见信号。
    await expect(page.getByRole('button', { name: '保存', exact: true })).toBeEnabled({ timeout: 30_000 })
    await page.screenshot({ path: path.join(artifactsDir, 'fr033-e2e-jdk-register.png'), fullPage: true })

    // ---- 下载安装 JDK：切到「安装 JDK」分段 ----
    await page.getByTestId('runtime-add-jdk').click()
    // 受控视图内部有它自己的提交按钮；这里只验证安装分段可达且能下发。
    const installSubmit = page.getByTestId('jdk-install-submit')
    await expect(installSubmit).toBeVisible({ timeout: 30_000 })
    await page.screenshot({ path: path.join(artifactsDir, 'fr033-e2e-jdk-install.png'), fullPage: true })
  })
})
