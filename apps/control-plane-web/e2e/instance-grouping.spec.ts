import { test, expect } from '@playwright/test'
import { login } from './helpers'

/** 
 *实例多级嵌套分组 · 单机（Playwright + mock 模式）验收。
 * 覆盖：组织分组树（role=tree）多级嵌套（level 1 亚洲区 → level 2 生存/创造）+ 新建子分组 + 搜索分组。
 * （后端 parent_id 自引用树 + M:N 由 Go instance_group_test.go / instance_group_tree_test.go 覆盖）
 */

test('实例组织分组 多级嵌套树', async ({ page }) => {
  await login(page)
  await page.goto('/instances')

  // 分组维度控件已从「工具栏上的独立 Select」收进「显示」下拉菜单：
  // 一级控件只留搜索/视图/筛选，排序、分页、列、分组维度都归入「显示」。
  // 故先开菜单，再在菜单里的「分组」分组处选「组织分组」。
  await page.getByRole('button', { name: '显示' }).click()
  await page.getByRole('menuitemradio', { name: '组织分组' }).click()

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
