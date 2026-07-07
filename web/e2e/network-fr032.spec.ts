import { mkdirSync } from 'node:fs'
import path from 'node:path'
import { test, expect } from '@playwright/test'
import { login } from './helpers'

const artifactsDir = process.cwd().endsWith(`${path.sep}web`)
  ? path.resolve(process.cwd(), '..', '.tmp')
  : path.resolve(process.cwd(), '.tmp')

/** FR-032 真浏览器纵切：Network 软标签、proxy↔backend 拓扑、节点端口占用。 */
test.describe('FR-032 群组服关系模型（mock 模式真浏览器）', () => {
  test.beforeEach(async ({ page }) => {
    mkdirSync(artifactsDir, { recursive: true })
    await login(page)
  })

  test('Network 成员管理与批量停止链路', async ({ page }) => {
    await page.goto('/networks')
    await expect(page.getByRole('heading', { name: '群组管理' })).toBeVisible()
    await expect(page.getByText('survival', { exact: true })).toBeVisible()
    await expect(page.getByText('3 个成员')).toBeVisible()

    await page.getByRole('button', { name: /survival 3 个成员/ }).click()
    await expect(page.getByRole('heading', { name: 'survival' })).toBeVisible()
    await expect(page.getByText('survival-proxy')).toBeVisible()
    await expect(page.getByText('survival-lobby')).toBeVisible()
    await expect(page.getByText('survival-world')).toBeVisible()

    await page.getByRole('checkbox', { name: 'creative-proxy' }).click()
    await page.getByRole('button', { name: '加入所选 (1)' }).click()
    await expect(page.getByText('4 个成员', { exact: true }).first()).toBeVisible()

    await page.getByRole('button', { name: '全部停止' }).click()
    await expect(page.getByText('成功 4 · 失败 0')).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr032-e2e-network-members.png'), fullPage: true })
  })

  test('拓扑视图与节点端口占用可见', async ({ page }) => {
    await page.goto('/networks/topology')
    await expect(page.getByRole('heading', { name: '群组管理' })).toBeVisible()
    await expect(page.getByRole('img', { name: '群组服拓扑' })).toBeVisible()
    await expect(page.locator('svg text').filter({ hasText: 'survival-proxy' })).toBeVisible()
    await expect(page.locator('svg text').filter({ hasText: 'survival-lobby' })).toBeVisible()
    await expect(page.locator('svg text').filter({ hasText: 'creative-proxy' })).toBeVisible()

    await page.screenshot({ path: path.join(artifactsDir, 'fr032-e2e-topology.png'), fullPage: true })

    await page.goto('/nodes')
    await expect(page.getByRole('heading', { name: '节点管理' })).toBeVisible()
    await page.getByRole('button', { name: '端口', exact: true }).click()
    await expect(page.getByText('端口占用')).toBeVisible()
    await expect(page.getByText('分配范围：server 25565+（每段 100 个）')).toBeVisible()
    await expect(page.getByText('survival-proxy')).toBeVisible()
    await expect(page.getByText('survival-lobby')).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr032-e2e-node-ports.png'), fullPage: true })
  })
})
