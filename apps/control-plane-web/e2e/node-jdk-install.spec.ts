import { mkdirSync } from 'node:fs'
import path from 'node:path'
import { test, expect } from '@playwright/test'
import { login } from './helpers'

const artifactsDir = process.cwd().endsWith(`${path.sep}web`)
  ? path.resolve(process.cwd(), '..', '.tmp')
  : path.resolve(process.cwd(), '.tmp')

/** FR-033 真浏览器纵切：节点 JDK 列表、登记已有 JDK、下载安装 JDK。 */
test.describe('FR-033 JDK 与运行时管理（mock 模式真浏览器）', () => {
  test.beforeEach(async ({ page }) => {
    mkdirSync(artifactsDir, { recursive: true })
    await login(page)
  })

  test('节点 JDK 列表、探测登记与一键下载链路可见可操作', async ({ page }) => {
    await page.goto('/nodes')
    await expect(page.getByRole('heading', { name: '节点管理' })).toBeVisible()
    await page.getByRole('button', { name: '运行时', exact: true }).click() // FR-311：JDK 分段更名运行时

    await expect(page.getByRole('button', { name: '已登记' })).toBeVisible()
    await expect(page.getByText('/opt/jdks/temurin-21')).toBeVisible()
    await expect(page.getByRole('button', { name: /托管\s*2/ })).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr033-e2e-jdk-list.png'), fullPage: true })

    await page.getByRole('button', { name: '登记已有' }).click()
    await expect(page.getByLabel('标记为 Worker 托管（仅作记录）')).toBeVisible()
    await page.getByPlaceholder('/opt/jdks/temurin-21 或 .../bin/java').fill('/opt/jdks/e2e-java-21')
    await page.getByRole('button', { name: '检测' }).click()
    await expect(page.getByText('21.0.4+9')).toBeVisible()
    await page.getByRole('button', { name: '保存', exact: true }).click() // exact：同页 FR-306 有「保存配置」
    await expect(page.getByText('/opt/jdks/e2e-java-21')).toBeVisible()
    await expect(page.getByRole('button', { name: /外部\s*1/ })).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr033-e2e-jdk-register.png'), fullPage: true })

    await page.getByRole('button', { name: '一键下载' }).click()
    await expect(page.getByText(/将安装/)).toBeVisible()
    await page.getByRole('button', { name: '下载安装' }).click()
    await expect(page.getByText('/opt/jdks/Temurin-21')).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr033-e2e-jdk-install.png'), fullPage: true })
  })
})
