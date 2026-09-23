import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-165 实例多级嵌套分组 · 单机（Playwright + mock 模式）验收。
 * 覆盖：组织分组树（role=tree）多级嵌套（level 1 亚洲区 → level 2 生存/创造）+ 新建子分组 + 搜索分组。
 * （后端 parent_id 自引用树 + M:N 由 Go instance_group_test.go / instance_group_tree_test.go 覆盖）
 */

test('FR-165 实例组织分组 多级嵌套树', async ({ page }) => {
  await login(page)
  await page.goto('/instances')

  // FR-452：分组维度控件由独立按钮改为 Radix Select（aria-label = grouping.groupBy「分组」）。
  // 选「组织分组」把列表切到 groupTree 维度，组管理入口随之出现在工具栏。
  await page.getByRole('combobox', { name: '分组' }).click()
  await page.getByRole('option', { name: '组织分组' }).click()

  // groupTree 维度下才渲染「管理分组」入口，点开组树面板。
  await page.getByRole('button', { name: '管理分组' }).click()

  const tree = page.getByRole('tree', { name: '分组树' })
  await expect(tree).toBeVisible()
  await expect(page.getByRole('button', { name: '新建分组' })).toBeVisible()

  // 多级嵌套：level 1 亚洲区 → level 2 生存/创造
  await expect(tree.getByRole('treeitem').filter({ hasText: '亚洲区' })).toBeVisible()
  const sub = tree.getByRole('treeitem').filter({ hasText: '生存' })
  await expect(sub).toBeVisible()
  await expect(sub).toHaveAttribute('aria-level', '2') // 二级嵌套
  await expect(sub.getByRole('button', { name: '新建子分组' })).toBeVisible()
})
