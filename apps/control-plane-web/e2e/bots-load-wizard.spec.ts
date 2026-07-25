import { test, expect } from '@playwright/test'
import { login, selectComboboxOption } from './helpers'

/**
 * FR-371 压测模板创建向导 · 单机（Playwright + VITE_MOCK 模式）验收。
 * 覆盖：模板 tab 列表、新建模板、从模板运行向导、会话 tab 列表。
 */
test.describe('FR-371 压测模板创建向导', () => {
  test.beforeEach(async ({ page }) => {
    await login(page)
  })

  test('模板 tab 展示种子模板列表', async ({ page }) => {
    await page.goto('/bots?tab=templates')
    // 等待模板 tab 激活
    await expect(page.getByRole('tab', { name: '命令模板' })).toHaveAttribute('data-state', 'active')
    // 种子模板 command-orchestration-v1 应出现在表格中
    await expect(page.getByText('command-orchestration-v1')).toBeVisible({ timeout: 10_000 })
    // 至少一行数据（表体行存在）
    await expect(page.locator('table tbody tr').first()).toBeVisible()
  })

  test('新建模板并出现在列表中', async ({ page }) => {
    await page.goto('/bots?tab=templates')
    await expect(page.getByRole('tab', { name: '命令模板' })).toHaveAttribute('data-state', 'active')

    const uniqueName = `e2e-tpl-${Date.now()}`
    await page.getByRole('button', { name: '新建模板' }).click()
    await expect(page.getByRole('dialog')).toBeVisible()
    await page.locator('#tpl-name').fill(uniqueName)
    await page.getByRole('button', { name: '保存', exact: true }).click()

    // 对话框关闭表示保存成功
    await expect(page.getByRole('dialog')).toBeHidden({ timeout: 10_000 })
    // 新模板出现在列表
    await expect(page.getByText(uniqueName)).toBeVisible({ timeout: 10_000 })
  })

  test('从模板运行向导并保存为会话', async ({ page }) => {
    await page.goto('/bots?tab=templates')
    await expect(page.getByRole('tab', { name: '命令模板' })).toHaveAttribute('data-state', 'active')
    // 等待模板列表加载
    await expect(page.getByText('command-orchestration-v1')).toBeVisible({ timeout: 10_000 })

    // 点击第一行模板的「从模板运行」按钮（aria-label）
    await page.getByRole('button', { name: '从模板运行' }).first().click()
    await expect(page.getByRole('dialog')).toBeVisible()

    // 步骤 1：选择实例
    await selectComboboxOption(page, '选择实例', 'survival-1')
    // 下一步
    await page.getByRole('button', { name: '下一步' }).click()

    // 步骤 2：连接配置（默认值已随实例自动填充），下一步
    await page.getByRole('button', { name: '下一步' }).click()

    // 步骤 3：命令编排，下一步
    await page.getByRole('button', { name: '下一步' }).click()

    // 步骤 4：负载曲线 + 阈值，下一步
    await page.getByRole('button', { name: '下一步' }).click()

    // 步骤 5：预检页，点击「仅保存」创建运行（不执行预检+启动）
    await page.getByRole('button', { name: '仅保存' }).click()

    // 保存后导航到会话 tab
    await expect(page.getByRole('tab', { name: '压测会话' })).toHaveAttribute('data-state', 'active', { timeout: 10_000 })
    // 会话列表至少一行
    await expect(page.locator('table tbody tr').first()).toBeVisible({ timeout: 10_000 })
  })

  test('会话 tab 展示演示会话', async ({ page }) => {
    await page.goto('/bots?tab=sessions')
    await expect(page.getByRole('tab', { name: '压测会话' })).toHaveAttribute('data-state', 'active')
    // 种子 V2 演示会话 namePrefix=load，列表首列展示 namePrefix
    await expect(page.locator('table tbody tr').first()).toBeVisible({ timeout: 10_000 })
    await expect(page.getByText('load', { exact: true })).toBeVisible()
  })
})
