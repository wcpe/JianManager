import { mkdirSync } from 'node:fs'
import path from 'node:path'
import { test, expect } from '@playwright/test'
import { login } from './helpers'

const artifactsDir = process.cwd().endsWith(`${path.sep}web`)
  ? path.resolve(process.cwd(), '..', '.tmp')
  : path.resolve(process.cwd(), '.tmp')

/** 真浏览器纵切：控制台 Shell（方案 C 品牌顶栏）、侧栏资源面板与工作区深链。 */
test.describe('运维控制台布局（mock 模式真浏览器）', () => {
  test.beforeEach(async ({ page }) => {
    mkdirSync(artifactsDir, { recursive: true })
    await page.addInitScript(() => {
      localStorage.setItem('sidebar.collapsed', '0')
      localStorage.removeItem('sidebar.collapsedGroups')
      localStorage.removeItem('sidebar.selectedNodeId')
      localStorage.removeItem('server-selector.favorites')
      localStorage.removeItem('server-selector.recent')
    })
    await login(page)
  })

  test('控制台布局：侧栏资源面板搜索 → 实例工作区深链', async ({ page }) => {
    await expect(page.locator('[data-slot="console-shell"]')).toBeVisible()
    await expect(page.locator('[data-slot="console-sidebar"]')).toBeVisible()
    await expect(page.locator('[data-slot="console-header"]')).toBeVisible()
    await expect(page.locator('[data-slot="console-main"]')).toBeVisible()
    const sidebar = page.locator('[data-slot="console-sidebar"]')
    await expect(sidebar.getByRole('link', { name: '首页', exact: true })).toBeVisible()

    // 侧栏已按**工作区**重构，不再是「域分组头按钮（服务器 / 平台设置 / 客户端分发）+ 子项」
    // 那种两级结构——侧栏内已无任何这类按钮。服务器运维工作区下，域内入口直接平铺为链接，
    // 其中「服务器」「节点」带计数，计数属于链接可访问名的一部分，故不能用 exact。
    // 跨域切换改由顶栏工作区承担（同类适配见用例）。
    await expect(sidebar.getByRole('link', { name: /^服务器/ })).toBeVisible()
    await expect(sidebar.getByRole('link', { name: /^节点/ })).toBeVisible()
    // 【为何不再断言「网络拓扑」链接】拓扑/群组归在「群组与工作台」分组，该分组
    // 不落在侧栏的服务器运维快捷区（实测侧栏链接只有 首页 / 服务器 / 节点 / 开源协议）。
    // 原本那条断言靠的是旧的一级平铺结构，已不成立；跨域入口改由顶栏工作区承担。

    // 资源区仍在同一侧栏内：搜索框 + 分组方式按钮。
    // 【为何不再有「选择服务器」】侧栏重构后资源定位改为
    // 「资源面板内的搜索 + 固定三段（置顶 / 后端分组树 / 标签）」，`ServerSelector`（含
    // 「选择服务器」按钮）已删除——见 WorkspaceSidebar.dom.test 的对齐断言。
    // 分组方式按钮的可及名随当前维度变化（默认「按节点」，菜单里可切「筛选与分组」
    // 的其它维度），故用两选一的正则。
    await expect(sidebar.getByTestId('sidebar-resource-panel')).toBeVisible()
    await expect(sidebar.getByRole('button', { name: /按节点|筛选与分组/ }).first()).toBeVisible()
    await expect(sidebar.getByRole('button', { name: '选择服务器', exact: true })).toHaveCount(0)
    // 方案 C：品牌 Logo 位于顶栏品牌区，节点作用域下拉已下线。
    await expect(page.locator('[data-slot="console-header"]').getByText('JianManager')).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr037-e2e-console-shell.png'), fullPage: true })

    // 经资源面板的搜索框定位实例并进深链（替代已删除的「选择服务器」弹窗路径）。
    await page.locator('[data-slot="console-sidebar"]').getByRole('textbox', { name: '搜索服务器' }).fill('creative-1')
    const hit = page.locator('[data-slot="console-sidebar"]').getByText('creative-1').first()
    await expect(hit).toBeVisible({ timeout: 10_000 })
    await page.screenshot({ path: path.join(artifactsDir, 'fr037-e2e-server-selector.png'), fullPage: true })

    await hit.click()
    await expect(page).toHaveURL(/\/instances\/3$/)
    await expect(page.getByText('creative-1').first()).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr037-e2e-instance-workspace.png'), fullPage: true })
  })
})
