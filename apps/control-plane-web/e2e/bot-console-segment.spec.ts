import { mkdirSync } from 'node:fs'
import path from 'node:path'
import { test, expect } from '@playwright/test'
import { login } from './helpers'

const artifactsDir = process.cwd().endsWith(`${path.sep}web`)
  ? path.resolve(process.cwd(), '..', '.tmp')
  : path.resolve(process.cwd(), '.tmp')

/** 真浏览器纵切：侧栏资源面板入口、实例内 Bot 分区与批量操作。 */
test.describe('控制台实例内 Bot 管理段（mock 模式真浏览器）', () => {
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

  test('侧栏资源面板入口 → Bot 分区 → 批量设行为', async ({ page }) => {
    /**
     * 入口已随侧栏重构更换：原来的「选择服务器」弹窗（`ServerSelector`）已被删除，
     * 资源定位改为资源面板内的「搜索 + 固定三段（置顶 / 后端分组树 / 标签）」。
     * 同一批变更里 `ops-console-layout.spec.ts` 已改用本路径，而
     * `OpsConsoleShell.dom.test.tsx` / `WorkspaceSidebar.dom.test.tsx` 还各自留着
     * 「不得再出现『选择服务器』」的反向断言。本用例之前一直被这个旧入口卡死（超时 60s）。
     *
     * 下面真正要验的东西没变：进某台实例的控制台 → Bot 分区 → 勾选 → 批量设行为。
     */
    const sidebar = page.locator('[data-slot="console-sidebar"]')
    await expect(sidebar.getByTestId('sidebar-resource-panel')).toBeVisible()
    await sidebar.getByRole('textbox', { name: '搜索服务器' }).fill('survival-1')
    const survivalRow = sidebar.getByText('survival-1').first()
    await expect(survivalRow).toBeVisible({ timeout: 15_000 })
    await page.screenshot({ path: path.join(artifactsDir, 'fr039-e2e-server-selector-entry.png'), fullPage: true })

    await survivalRow.click()
    await expect(page).toHaveURL(/\/instances\/1$/)
    // 顶栏标题只保留实例名，无「服务器控制台 /」前缀
    await expect(page.getByRole('heading', { name: 'survival-1', exact: true })).toBeVisible()
    await expect(page.locator('[data-page="instance-console"]')).toBeVisible()

    // 实例分区为 role=tab（tablist「实例页签」），不再是 button
    await page.getByRole('tab', { name: 'Bot', exact: true }).click()
    await expect(page.getByText('当前筛选 2 个 Bot')).toBeVisible()
    await expect(page.getByText('GuardBot')).toBeVisible()
    await expect(page.getByText('FollowBot')).toBeVisible()
    await expect(page.getByText('共 2 个')).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr039-e2e-bot-segment.png'), fullPage: true })

    await page.getByRole('checkbox', { name: 'GuardBot' }).click()
    /**
     * 批量条按**文案**定位，不用 `data-testid`。
     *
     * 旧的 `bot-batch-bar` / `bot-batch-scope` 两个 testid 已在迁包重写中丢掉
     * （视图现在就在 `components/views/instances/BotSegment.tsx`，全仓搜不到这两个名字），
     * 用例因此卡在 30s 超时。
     *
     * 改用文案定位时要注意两个约束，否则会重蹈 2026-10-20 那次 flaky：
     *  1. 不能靠 CSS 类名反推容器——`rounded-lg` 全页到处都是；
     *  2. 分组块表头也有「批量设行为」按钮，必须先把范围限定到「选中集」那条批量条。
     *
     * “已选 1 个 Bot” 只在选中集上出现（未选中时文案是“当前筛选 N 个 Bot”），
     * 组块表头不可能有它，所以拿它升到最近的批量条容器是安全的。
     * 下面用 `hasText` 从右上往上找包含「批量设行为」的有界容器，而不把整页当容器。
     */
    const batchBar = page
      .locator('div')
      .filter({ hasText: /^已选 1 个 Bot/ })
      .filter({ has: page.getByRole('button', { name: '批量设行为' }) })
      .last()
    await expect(batchBar.getByText('已选 1 个 Bot')).toBeVisible()
    await batchBar.getByRole('combobox').click()
    await page.getByRole('option', { name: '巡逻' }).click()
    await batchBar.getByRole('button', { name: '批量设行为' }).click()
    await expect(page.getByText('成功 1 · 失败 0')).toBeVisible()
    await page.screenshot({ path: path.join(artifactsDir, 'fr039-e2e-batch-result.png'), fullPage: true })
  })
})
